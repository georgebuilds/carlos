package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// seedOldOrphan inserts a top-level orphaned-empty agent row whose
// updated_at is 30 days in the past - well beyond the production
// DefaultOrphanPruneAge (7d) grace window, so the janitor's age gate
// does not keep it alive.
func seedOldOrphan(t *testing.T, log *agent.SQLiteEventLog, id string) {
	t.Helper()
	old := time.Now().UTC().Add(-30 * 24 * time.Hour).Truncate(time.Millisecond)
	if err := log.InsertAgent(context.Background(), agent.AgentRow{
		ID:              id,
		RootID:          id,
		State:           agent.StateOrphaned,
		Attempt:         1,
		Title:           "abandoned",
		Model:           "m",
		CreatedAt:       old,
		UpdatedAt:       old,
		LastHeartbeatAt: old,
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// Slice 9f moved the janitor prune OUT of ensureDefaultAgent (it was the
// only unbounded pre-frame cost on the boot path). The contract now is:
// ensureDefaultAgent reports created=true on the brand-new branch, and
// the caller fires pruneEmptyOrphans/startJanitorPrune on that flag -
// same cadence as the old inline prune, just off the critical path.
// This test pins the composed behaviour the boot path relies on.
func TestEnsureDefaultAgent_CreatedFlagDrivesJanitorPrune(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = agent.CloseStateDB(log) }()

	ctx := context.Background()
	// Seed two orphaned-empty top-level rows - the clutter that
	// accumulates across crashes.
	seedOldOrphan(t, log, "orph-1")
	seedOldOrphan(t, log, "orph-2")

	// Create the new chat session: must take the brand-new branch.
	created, err := ensureDefaultAgent(ctx, log, "fresh", "anthropic", "claude-opus-4-7", "george")
	if err != nil {
		t.Fatalf("ensureDefaultAgent: %v", err)
	}
	if !created {
		t.Fatal("brand-new session must report created=true (janitor trigger)")
	}

	// The prune itself no longer runs inside ensureDefaultAgent.
	for _, id := range []string{"orph-1", "orph-2"} {
		if _, ok, err := log.GetAgent(ctx, id); err != nil {
			t.Fatalf("get %s: %v", id, err)
		} else if !ok {
			t.Errorf("%s pruned inside ensureDefaultAgent - the janitor must be caller-driven now", id)
		}
	}

	// The caller-side janitor (what runDefault kicks on created=true)
	// must prune them, and the diagnostic must land in the supplied
	// diag sink (an off-terminal writer), never on stderr where it
	// would corrupt the alt-screen frame.
	var diag bytes.Buffer
	pruneEmptyOrphans(ctx, log, &diag)

	if got := diag.String(); !strings.Contains(got, "pruned 2 empty orphaned session(s)") {
		t.Errorf("prune diagnostic missing from diag buffer; got %q", got)
	}
	for _, id := range []string{"orph-1", "orph-2"} {
		if _, ok, err := log.GetAgent(ctx, id); err != nil {
			t.Fatalf("get %s: %v", id, err)
		} else if ok {
			t.Errorf("%s should have been pruned but remains", id)
		}
	}
	if _, ok, err := log.GetAgent(ctx, "fresh"); err != nil {
		t.Fatalf("get fresh: %v", err)
	} else if !ok {
		t.Fatal("freshly-created agent missing post-prune - the janitor must never eat the live session")
	}
}

// The resume branch (existing > 0 events) must report created=false so
// the caller does NOT trigger the janitor - we never want a chat resume
// to silently delete neighboring sessions as a side effect.
func TestEnsureDefaultAgent_ResumeReportsNotCreated(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = agent.CloseStateDB(log) }()

	ctx := context.Background()

	// First call: creates "chat-a" (brand-new branch).
	created, err := ensureDefaultAgent(ctx, log, "chat-a", "anthropic", "claude-opus-4-7", "george")
	if err != nil {
		t.Fatalf("ensureDefaultAgent create: %v", err)
	}
	if !created {
		t.Fatal("first call should report created=true")
	}

	// Second call with the SAME id - exercises the resume branch
	// (existing events present). created must be false so the boot
	// path's janitor gate stays shut.
	created, err = ensureDefaultAgent(ctx, log, "chat-a", "anthropic", "claude-opus-4-7", "george")
	if err != nil {
		t.Fatalf("ensureDefaultAgent resume: %v", err)
	}
	if created {
		t.Fatal("resume must report created=false - janitor only fires on the new-agent branch")
	}
}

// startJanitorPrune is the async wrapper the boot path uses: it must run
// the same prune on a goroutine, complete before wg.Wait returns, and
// leave the live agent untouched. This is the concurrent-with-live-TUI
// shape: the fresh running agent and the goroutine share one DB handle.
func TestStartJanitorPrune_BackgroundPruneCompletesUnderWaitGroup(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = agent.CloseStateDB(log) }()

	ctx := context.Background()
	seedOldOrphan(t, log, "orph-bg")
	if _, err := ensureDefaultAgent(ctx, log, "live", "anthropic", "claude-opus-4-7", "george"); err != nil {
		t.Fatalf("ensureDefaultAgent: %v", err)
	}

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		diag bytes.Buffer
	)
	// lockedWriter mirrors production's append-mode *os.File sink in
	// being safe to write from the janitor goroutine.
	startJanitorPrune(ctx, log, writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return diag.Write(p)
	}), &wg)
	wg.Wait()

	mu.Lock()
	got := diag.String()
	mu.Unlock()
	if !strings.Contains(got, "pruned 1 empty orphaned session(s)") {
		t.Errorf("background prune diagnostic missing; got %q", got)
	}
	if _, ok, err := log.GetAgent(ctx, "orph-bg"); err != nil {
		t.Fatalf("get orph-bg: %v", err)
	} else if ok {
		t.Error("orph-bg should have been pruned by the background janitor")
	}
	if _, ok, err := log.GetAgent(ctx, "live"); err != nil {
		t.Fatalf("get live: %v", err)
	} else if !ok {
		t.Fatal("live agent must survive the background janitor")
	}
}

// pruneEmptyOrphans must surface (not swallow, not panic on) a prune
// failure - the bad path the boot must shrug off. A closed DB makes
// DeleteEmptyOrphanedAgents fail at BeginTx.
func TestPruneEmptyOrphans_ErrorLandsInDiag(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	_ = agent.CloseStateDB(log)

	var diag bytes.Buffer
	pruneEmptyOrphans(context.Background(), log, &diag)
	if got := diag.String(); !strings.Contains(got, "prune empty orphans:") {
		t.Errorf("expected prune error diagnostic, got %q", got)
	}
}

// writerFunc adapts a func to io.Writer for test sinks.
type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
