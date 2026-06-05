package agent_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

func newApprovalLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	log, err := agent.OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func mkRef(id string) agent.ArtifactRef {
	return agent.ArtifactRef{
		ID:        id,
		AgentID:   "agent-1",
		Path:      "/tmp/fake/" + id,
		Kind:      "plan",
		SHA256:    "deadbeef",
		Size:      42,
		CreatedAt: time.Now().UTC(),
	}
}

func TestApproval_ProposeThenList(t *testing.T) {
	log := newApprovalLog(t)
	ctx := context.Background()

	r1 := mkRef("art-a")
	if _, err := agent.ProposeApproval(ctx, log, "agent-1", "refactor parser", r1); err != nil {
		t.Fatal(err)
	}
	r2 := mkRef("art-b")
	if _, err := agent.ProposeApproval(ctx, log, "agent-1", "extract helper", r2); err != nil {
		t.Fatal(err)
	}

	pending, err := agent.ListPendingApprovals(ctx, log)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending count: want 2 got %d", len(pending))
	}
	// Oldest first.
	if pending[0].Ref.ID != "art-a" {
		t.Errorf("queue order: want art-a first, got %s", pending[0].Ref.ID)
	}
	if pending[0].Title != "refactor parser" {
		t.Errorf("title: want %q got %q", "refactor parser", pending[0].Title)
	}
}

func TestApproval_AcceptRemovesFromPending(t *testing.T) {
	log := newApprovalLog(t)
	ctx := context.Background()
	ref := mkRef("art-x")
	_, _ = agent.ProposeApproval(ctx, log, "agent-1", "fix bug", ref)

	if _, err := agent.AcceptApproval(ctx, log, ref.ID, "looks good"); err != nil {
		t.Fatal(err)
	}
	pending, _ := agent.ListPendingApprovals(ctx, log)
	if len(pending) != 0 {
		t.Errorf("pending after accept: want 0 got %d", len(pending))
	}
}

func TestApproval_RejectRemovesFromPending(t *testing.T) {
	log := newApprovalLog(t)
	ctx := context.Background()
	ref := mkRef("art-y")
	_, _ = agent.ProposeApproval(ctx, log, "agent-1", "risky change", ref)

	if _, err := agent.RejectApproval(ctx, log, ref.ID, "scope creep"); err != nil {
		t.Fatal(err)
	}
	pending, _ := agent.ListPendingApprovals(ctx, log)
	if len(pending) != 0 {
		t.Errorf("pending after reject: want 0 got %d", len(pending))
	}
}

func TestApproval_MultipleProposalsAndResolutions(t *testing.T) {
	log := newApprovalLog(t)
	ctx := context.Background()
	// 4 proposals, 2 resolved (one accept, one reject), 2 remain.
	refs := []agent.ArtifactRef{mkRef("a"), mkRef("b"), mkRef("c"), mkRef("d")}
	for i, r := range refs {
		title := []string{"plan a", "plan b", "plan c", "plan d"}[i]
		_, _ = agent.ProposeApproval(ctx, log, "agent-1", title, r)
	}
	_, _ = agent.AcceptApproval(ctx, log, "b", "")
	_, _ = agent.RejectApproval(ctx, log, "d", "no")

	pending, _ := agent.ListPendingApprovals(ctx, log)
	if len(pending) != 2 {
		t.Fatalf("pending: want 2 got %d", len(pending))
	}
	got := map[string]bool{}
	for _, p := range pending {
		got[p.Ref.ID] = true
	}
	if !got["a"] || !got["c"] {
		t.Errorf("pending should be {a,c}, got %v", got)
	}
}

func TestApproval_EmptyTitleRejected(t *testing.T) {
	log := newApprovalLog(t)
	if _, err := agent.ProposeApproval(context.Background(), log, "a", "", mkRef("x")); err == nil {
		t.Error("expected error on empty title")
	}
}

func TestApproval_EmptyRefIDRejected(t *testing.T) {
	log := newApprovalLog(t)
	ref := agent.ArtifactRef{ID: ""}
	if _, err := agent.ProposeApproval(context.Background(), log, "a", "t", ref); err == nil {
		t.Error("expected error on empty ref ID")
	}
}

func TestApproval_EmptyResolutionIDRejected(t *testing.T) {
	log := newApprovalLog(t)
	if _, err := agent.AcceptApproval(context.Background(), log, "", "ok"); err == nil {
		t.Error("expected error on empty accept ID")
	}
	if _, err := agent.RejectApproval(context.Background(), log, "", "no"); err == nil {
		t.Error("expected error on empty reject ID")
	}
}

func TestApproval_DoubleResolutionIsIdempotent(t *testing.T) {
	// Two accepts on the same ID — second is a no-op for queueness.
	log := newApprovalLog(t)
	ctx := context.Background()
	ref := mkRef("art-z")
	_, _ = agent.ProposeApproval(ctx, log, "agent-1", "t", ref)
	_, _ = agent.AcceptApproval(ctx, log, ref.ID, "")
	_, _ = agent.AcceptApproval(ctx, log, ref.ID, "again")
	pending, _ := agent.ListPendingApprovals(ctx, log)
	if len(pending) != 0 {
		t.Errorf("after double accept: want 0 got %d", len(pending))
	}
}
