package memory_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/memory"
)

// agentSink wraps a *agent.SQLiteEventLog to satisfy
// memory.ProposalSink. This is the same adapter shape cmd/carlos
// foreground wiring uses; we replicate it here so tests cover the
// full propose-approval round-trip end-to-end through the real
// agent.WriteArtifact + agent.ProposeApproval entry points.
type agentSink struct{ log *agent.SQLiteEventLog }

func (a *agentSink) WriteProposalArtifact(ctx context.Context, agentID, kind string, body []byte) (memory.ProposalRef, error) {
	ref, err := agent.WriteArtifact(ctx, a.log, agentID, kind, body)
	if err != nil {
		return memory.ProposalRef{}, err
	}
	return memory.ProposalRef{
		ID:      ref.ID,
		AgentID: ref.AgentID,
		Path:    ref.Path,
		Kind:    ref.Kind,
		SHA256:  ref.SHA256,
		Size:    ref.Size,
	}, nil
}

func (a *agentSink) ProposeApproval(ctx context.Context, agentID, title string, ref memory.ProposalRef) error {
	_, err := agent.ProposeApproval(ctx, a.log, agentID, title, agent.ArtifactRef{
		ID:      ref.ID,
		AgentID: ref.AgentID,
		Path:    ref.Path,
		Kind:    ref.Kind,
		SHA256:  ref.SHA256,
		Size:    ref.Size,
	})
	return err
}

// newAgentLogAndStore opens one SQLite event log + a memory Store
// sharing the same DB handle, seeds a "user" agent row (FK target
// for ProposeFactWrite's artifact insert), and isolates the
// artifact base under t.TempDir() so the test doesn't leak blobs.
// Returns the log (for ListPendingApprovals), the store, and an
// agentSink wired to that log (for ProposeFactWrite calls).
func newAgentLogAndStore(t *testing.T) (*agent.SQLiteEventLog, *memory.Store, *agentSink) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	log, err := agent.OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("OpenSQLiteEventLog: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	store, err := memory.NewStore(log.DB())
	if err != nil {
		t.Fatalf("NewStore on shared db: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	// Seed the synthetic "user" agent row so artifacts.agent_id FK
	// lands. We bypass seedAgent (unexported in agent_test) by going
	// through InsertAgent directly with a minimal AgentRow.
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := log.InsertAgent(context.Background(), agent.AgentRow{
		ID:              "user",
		RootID:          "user",
		State:           agent.StateRunning,
		Attempt:         1,
		Title:           "user",
		Model:           "n/a",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("InsertAgent: %v", err)
	}

	// Isolate artifact base so WriteArtifact's MkdirArtifactBase
	// doesn't touch ~/.carlos/artifacts/.
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(dir, "artifacts"))
	return log, store, &agentSink{log: log}
}

// TestGetFact_MissingReturnsFalse verifies the documented miss shape:
// no error, found=false, empty value.
func TestGetFact_MissingReturnsFalse(t *testing.T) {
	_, store, _ := newAgentLogAndStore(t)
	v, ok, err := store.GetFact(context.Background(), "name")
	if err != nil {
		t.Fatalf("GetFact: %v", err)
	}
	if ok {
		t.Errorf("expected found=false, got value=%q", v)
	}
}

// TestGetFact_EmptyKeyRejected guards against the easy bug of an
// empty key matching every row in a future LIKE refactor.
func TestGetFact_EmptyKeyRejected(t *testing.T) {
	_, store, _ := newAgentLogAndStore(t)
	if _, _, err := store.GetFact(context.Background(), ""); err == nil {
		t.Error("expected error on empty key")
	}
}

// TestListFacts_EmptyStore verifies that ListFacts on a fresh DB
// returns nil/empty without erroring (no rows is normal at startup).
func TestListFacts_EmptyStore(t *testing.T) {
	_, store, _ := newAgentLogAndStore(t)
	out, err := store.ListFacts(context.Background())
	if err != nil {
		t.Fatalf("ListFacts: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("want empty, got %d facts", len(out))
	}
}

// TestApplyFact_InsertAndList verifies the happy path: ApplyFact a
// fact, ListFacts sees it with the right source.
func TestApplyFact_InsertAndList(t *testing.T) {
	_, store, _ := newAgentLogAndStore(t)
	ctx := context.Background()
	if err := store.ApplyFact(ctx, "name", "George", memory.FactSourceUser); err != nil {
		t.Fatalf("ApplyFact: %v", err)
	}
	out, err := store.ListFacts(ctx)
	if err != nil {
		t.Fatalf("ListFacts: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 fact, got %d", len(out))
	}
	if out[0].Key != "name" || out[0].Value != "George" {
		t.Errorf("unexpected fact: %+v", out[0])
	}
	if out[0].Source != memory.FactSourceUser {
		t.Errorf("source: want %q got %q", memory.FactSourceUser, out[0].Source)
	}
	if out[0].UpdatedAt.IsZero() {
		t.Error("UpdatedAt should be set")
	}
}

// TestApplyFact_UpsertsExistingKey verifies the UPSERT contract: a
// second ApplyFact with the same key replaces value + bumps
// updated_at + writes the new source.
func TestApplyFact_UpsertsExistingKey(t *testing.T) {
	_, store, _ := newAgentLogAndStore(t)
	ctx := context.Background()
	if err := store.ApplyFact(ctx, "tz", "UTC", memory.FactSourceUser); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyFact(ctx, "tz", "America/New_York", memory.FactSourceAgentAccepted); err != nil {
		t.Fatal(err)
	}
	v, ok, err := store.GetFact(ctx, "tz")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("fact should exist after upsert")
	}
	if v != "America/New_York" {
		t.Errorf("value: want America/New_York got %q", v)
	}
	out, _ := store.ListFacts(ctx)
	if len(out) != 1 {
		t.Errorf("upsert should keep one row, got %d", len(out))
	}
	if out[0].Source != memory.FactSourceAgentAccepted {
		t.Errorf("source: want %q got %q", memory.FactSourceAgentAccepted, out[0].Source)
	}
}

// TestApplyFact_RejectsEmpty guards against blank-key / blank-value
// rows.
func TestApplyFact_RejectsEmpty(t *testing.T) {
	_, store, _ := newAgentLogAndStore(t)
	ctx := context.Background()
	if err := store.ApplyFact(ctx, "", "v", "u"); err == nil {
		t.Error("expected error on empty key")
	}
	if err := store.ApplyFact(ctx, "k", "", "u"); err == nil {
		t.Error("expected error on empty value")
	}
}

// TestProposeFactWrite_CreatesArtifactAndApproval is the centerpiece
// test for slice 7i: a proposal must (a) materialize an artifact
// blob, (b) leave a pending approval in the queue, and (c) NOT
// touch the user_model table.
func TestProposeFactWrite_CreatesArtifactAndApproval(t *testing.T) {
	log, store, sink := newAgentLogAndStore(t)
	ctx := context.Background()

	refID, err := store.ProposeFactWrite(ctx, sink, "current_project", "anneal", "user mentioned shipping a release tonight")
	if err != nil {
		t.Fatalf("ProposeFactWrite: %v", err)
	}
	if refID == "" {
		t.Error("expected non-empty artifact ref id")
	}

	// (a) The artifact blob exists on disk and decodes back to the
	//     FactProposal we proposed.
	base := os.Getenv("CARLOS_ARTIFACT_BASE")
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatalf("read artifact dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 artifact blob, got %d", len(entries))
	}
	blob, err := os.ReadFile(filepath.Join(base, entries[0].Name()))
	if err != nil {
		t.Fatalf("read blob: %v", err)
	}
	var got memory.FactProposal
	if err := json.Unmarshal(blob, &got); err != nil {
		t.Fatalf("unmarshal blob: %v (raw: %s)", err, blob)
	}
	if got.Key != "current_project" || got.Value != "anneal" {
		t.Errorf("blob contents: %+v", got)
	}
	if got.Rationale == "" {
		t.Errorf("rationale should be preserved, got %+v", got)
	}

	// (b) The approval queue surfaces the pending review.
	pending, err := agent.ListPendingApprovals(ctx, log)
	if err != nil {
		t.Fatalf("ListPendingApprovals: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending approval, got %d", len(pending))
	}
	if pending[0].Title != "user-model update: current_project" {
		t.Errorf("title: %q", pending[0].Title)
	}
	if pending[0].Ref.ID != refID {
		t.Errorf("approval ref ID mismatch: want %s got %s", refID, pending[0].Ref.ID)
	}

	// (c) The user_model table is still empty — we propose, we do
	//     not silently write.
	facts, _ := store.ListFacts(ctx)
	if len(facts) != 0 {
		t.Errorf("user_model should be empty until ApplyFact runs, got %d rows", len(facts))
	}
}

// TestProposeFactWrite_RejectsEmpty guards the input validation
// guards.
func TestProposeFactWrite_RejectsEmpty(t *testing.T) {
	_, store, sink := newAgentLogAndStore(t)
	ctx := context.Background()
	if _, err := store.ProposeFactWrite(ctx, sink, "", "v", ""); err == nil {
		t.Error("expected error on empty key")
	}
	if _, err := store.ProposeFactWrite(ctx, sink, "k", "", ""); err == nil {
		t.Error("expected error on empty value")
	}
}

// TestProposeFactWrite_NilSinkRejected guards the doc'd contract.
func TestProposeFactWrite_NilSinkRejected(t *testing.T) {
	_, store, _ := newAgentLogAndStore(t)
	if _, err := store.ProposeFactWrite(context.Background(), nil, "k", "v", ""); err == nil {
		t.Error("expected error on nil sink")
	}
}

// TestListFacts_OrderingByKey verifies the documented ASC ordering
// so CLI / TUI output is deterministic.
func TestListFacts_OrderingByKey(t *testing.T) {
	_, store, _ := newAgentLogAndStore(t)
	ctx := context.Background()
	for _, k := range []string{"zebra", "alpha", "mango"} {
		if err := store.ApplyFact(ctx, k, "v", memory.FactSourceUser); err != nil {
			t.Fatal(err)
		}
	}
	out, _ := store.ListFacts(ctx)
	if len(out) != 3 {
		t.Fatalf("want 3, got %d", len(out))
	}
	want := []string{"alpha", "mango", "zebra"}
	for i, w := range want {
		if out[i].Key != w {
			t.Errorf("at %d: want %s got %s", i, w, out[i].Key)
		}
	}
}
