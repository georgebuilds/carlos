package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// On the brand-new-agent branch, ensureDefaultAgent must prune
// orphaned-empty top-level rows that accumulated from prior abrupt
// exits. The /resume picker shouldn't fill up with "(no messages
// yet)" cards the user gets no information from.
func TestEnsureDefaultAgent_PrunesEmptyOrphansOnNewAgent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = agent.CloseStateDB(log) }()

	ctx := context.Background()
	// Seed timestamp well past the production DefaultOrphanPruneAge
	// grace window so the age gate inside DeleteEmptyOrphanedAgents
	// doesn't keep these rows alive. Production callers pass
	// agent.DefaultOrphanPruneAge (7d); we want a clear-cut "past
	// grace" timestamp so the test reflects the prune actually firing.
	old := time.Now().UTC().Add(-30 * 24 * time.Hour).Truncate(time.Millisecond)

	// Seed two orphaned-empty top-level rows — the clutter that
	// accumulates across crashes.
	for _, id := range []string{"orph-1", "orph-2"} {
		if err := log.InsertAgent(ctx, agent.AgentRow{
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

	// Create the new chat session. After this call, the seeded
	// orphans should be gone and the fresh agent should be present.
	// The prune diagnostic must land in the supplied diag buffer (an
	// off-terminal sink), never on stderr where it would corrupt the
	// alt-screen frame.
	var diag bytes.Buffer
	if err := ensureDefaultAgent(ctx, log, "fresh", "anthropic", "claude-opus-4-7", "george", &diag); err != nil {
		t.Fatalf("ensureDefaultAgent: %v", err)
	}

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
		t.Fatal("freshly-created agent missing post-call")
	}
}

// The resume branch (existing > 0 events) must NOT trigger the prune
// path — we never want a chat resume to silently delete neighboring
// sessions as a side effect.
func TestEnsureDefaultAgent_DoesNotPruneOnResume(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = agent.CloseStateDB(log) }()

	ctx := context.Background()

	// First call: creates "chat-a" (also fires prune, but there are
	// no orphans seeded yet, so it's a no-op).
	var diag bytes.Buffer
	if err := ensureDefaultAgent(ctx, log, "chat-a", "anthropic", "claude-opus-4-7", "george", &diag); err != nil {
		t.Fatalf("ensureDefaultAgent create: %v", err)
	}

	// Now seed an orphan in the same DB.
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := log.InsertAgent(ctx, agent.AgentRow{
		ID:              "leftover",
		RootID:          "leftover",
		State:           agent.StateOrphaned,
		Attempt:         1,
		Title:           "abandoned",
		Model:           "m",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("seed leftover: %v", err)
	}

	// Second call with the SAME id — exercises the resume branch
	// (existing events present). Prune must NOT fire.
	if err := ensureDefaultAgent(ctx, log, "chat-a", "anthropic", "claude-opus-4-7", "george", &diag); err != nil {
		t.Fatalf("ensureDefaultAgent resume: %v", err)
	}

	if _, ok, err := log.GetAgent(ctx, "leftover"); err != nil {
		t.Fatalf("get leftover: %v", err)
	} else if !ok {
		t.Fatal("leftover orphan was pruned during resume — should only fire on new-agent branch")
	}
}
