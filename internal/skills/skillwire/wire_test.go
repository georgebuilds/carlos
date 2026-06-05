package skillwire_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/skills"
	"github.com/georgebuilds/carlos/internal/skills/skillwire"
)

func newLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	tmp := t.TempDir()
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(tmp, "artifacts"))
	dbPath := filepath.Join(tmp, "state.db")
	log, err := agent.OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

// seedAgent satisfies the artifacts table's FOREIGN KEY constraint
// (artifact rows reference an agents row). Mirrors the helper in
// internal/agent/lifecycle_test.go but copied here because that helper
// lives in the agent package's test binary, not exported.
func seedAgent(t *testing.T, ctx context.Context, log *agent.SQLiteEventLog, id string) {
	t.Helper()
	created, err := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: id, RootID: id, Title: id, Model: "fake",
	})
	if err != nil {
		t.Fatalf("seed: marshal: %v", err)
	}
	if _, err := log.Append(ctx, agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: agent.EvtStateChange, Payload: created,
	}); err != nil {
		t.Fatalf("seed: append: %v", err)
	}
	now := time.Now().UTC()
	row := agent.AgentRow{
		ID:              id,
		RootID:          id,
		State:           agent.StateRunning,
		Attempt:         1,
		Title:           id,
		Model:           "fake",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastHeartbeatAt: now,
	}
	if err := log.InsertAgent(ctx, row); err != nil {
		t.Fatalf("seed: insert agent: %v", err)
	}
}

func TestProposeSkill_HappyPath(t *testing.T) {
	log := newLog(t)
	ctx := context.Background()
	seedAgent(t, ctx, log, "agent-1")
	p := &skills.Proposal{
		Name:        "react-test-debug",
		Description: "Use when a React test is flaky",
		Body:        "1. run jest --verbose\n2. ...",
		InducerName: "anthropic:claude",
	}
	ref, err := skillwire.ProposeSkill(ctx, log, "agent-1", p)
	if err != nil {
		t.Fatalf("ProposeSkill: %v", err)
	}
	if ref.Kind != agent.ArtifactKindSkillProposal {
		t.Errorf("kind: want %q got %q", agent.ArtifactKindSkillProposal, ref.Kind)
	}
	// The proposal must be in the approval queue now.
	pending, err := agent.ListPendingApprovals(ctx, log)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("want 1 pending, got %d", len(pending))
	}
	if pending[0].Title != "skill: react-test-debug" {
		t.Errorf("title: %q", pending[0].Title)
	}
}

func TestProposeSkill_NilProposal(t *testing.T) {
	log := newLog(t)
	_, err := skillwire.ProposeSkill(context.Background(), log, "a", nil)
	if err == nil {
		t.Error("want nil-proposal error")
	}
}

// TestPromoteAccepted_HappyPath: end-to-end — Propose, then promote
// the artifact; a real SKILL.md lands on disk under the configured
// convention path.
func TestPromoteAccepted_HappyPath(t *testing.T) {
	log := newLog(t)
	ctx := context.Background()
	seedAgent(t, ctx, log, "agent-1")

	homeDir := t.TempDir()
	// Reuse CARLOS_ARTIFACT_BASE-based home so ReadArtifact resolves
	// (skillwire derives basePath from params.Home unless overridden).
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(homeDir, ".carlos", "artifacts"))

	p := &skills.Proposal{
		Name:        "promote-me",
		Description: "Use when testing the promote path",
		Body:        "step\n",
		InducerName: "anthropic:claude",
	}
	ref, err := skillwire.ProposeSkill(ctx, log, "agent-1", p)
	if err != nil {
		t.Fatalf("ProposeSkill: %v", err)
	}

	// Simulate user accept.
	if _, err := agent.AcceptApproval(ctx, log, ref.ID, "looks good"); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Skills: config.SkillsConfig{Convention: config.SkillsConventionAgents}}
	dir, err := skillwire.PromoteAccepted(ctx, log, ref, skillwire.PromoteParams{
		Cfg:         cfg,
		Home:        homeDir,
		ProjectRoot: "",
		JudgeModel:  "openai:gpt-4o",
	})
	if err != nil {
		t.Fatalf("PromoteAccepted: %v", err)
	}

	wantDir := filepath.Join(homeDir, ".agents", "skills", "promote-me")
	if dir != wantDir {
		t.Errorf("dir: want %q got %q", wantDir, dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err != nil {
		t.Errorf("SKILL.md not on disk: %v", err)
	}

	// Confirm the loaded skill carries judge_model + provenance.
	loaded, err := skills.LoadSkill(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Provenance != skills.ProvInduced {
		t.Errorf("provenance: %q", loaded.Provenance)
	}
	if loaded.JudgeModel != "openai:gpt-4o" {
		t.Errorf("judge_model: %q", loaded.JudgeModel)
	}
}

// TestPromoteAccepted_WrongKind: artifact of a non-skill_proposal kind
// is rejected.
func TestPromoteAccepted_WrongKind(t *testing.T) {
	log := newLog(t)
	ctx := context.Background()
	seedAgent(t, ctx, log, "agent-1")
	// Write a non-skill artifact directly.
	ref, err := agent.WriteArtifact(ctx, log, "agent-1", agent.ArtifactKindFile, []byte(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = skillwire.PromoteAccepted(ctx, log, ref, skillwire.PromoteParams{Home: t.TempDir()})
	if err == nil {
		t.Error("want kind-mismatch error")
	}
}

func TestProposalTitle(t *testing.T) {
	got := skillwire.ProposalTitle(&skills.Proposal{Name: "x"})
	if got != "skill: x" {
		t.Errorf("want 'skill: x', got %q", got)
	}
	if skillwire.ProposalTitle(nil) != "skill: (unnamed)" {
		t.Errorf("nil case")
	}
}

// TestMetrics_AcceptanceRate: propose 4, accept 2, reject 1; rate is
// 2/3 (pending excluded).
func TestMetrics_AcceptanceRate(t *testing.T) {
	log := newLog(t)
	ctx := context.Background()
	seedAgent(t, ctx, log, "agent-1")
	for _, name := range []string{"a", "b", "c", "d"} {
		_, err := skillwire.ProposeSkill(ctx, log, "agent-1", &skills.Proposal{
			Name:        name,
			Description: "Use when " + name,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	pending, _ := agent.ListPendingApprovals(ctx, log)
	if len(pending) != 4 {
		t.Fatalf("want 4 pending, got %d", len(pending))
	}
	// Accept first two by their artifact ID.
	_, _ = agent.AcceptApproval(ctx, log, pending[0].Ref.ID, "")
	_, _ = agent.AcceptApproval(ctx, log, pending[1].Ref.ID, "")
	_, _ = agent.RejectApproval(ctx, log, pending[2].Ref.ID, "nope")
	// pending[3] remains pending.

	m := skillwire.NewMetrics()
	rate, err := m.AcceptanceRate(ctx, log)
	if err != nil {
		t.Fatal(err)
	}
	if rate < 0.66 || rate > 0.67 {
		t.Errorf("want ~0.667, got %v", rate)
	}
}

func TestMetrics_Snapshot(t *testing.T) {
	log := newLog(t)
	ctx := context.Background()
	seedAgent(t, ctx, log, "agent-1")
	_, err := skillwire.ProposeSkill(ctx, log, "agent-1", &skills.Proposal{
		Name:        "x",
		Description: "Use when x",
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC)
	// Library with two active + one stale + one archived.
	lib := &skills.Library{Active: []*skills.Skill{
		{Name: "a", Description: "Use when a", Created: now.AddDate(0, 0, -120), ReuseCount: 4},
		{Name: "b", Description: "Use when b", Created: now.AddDate(0, 0, -45), Status: skills.StatusStale},
		{Name: "c", Description: "Use when c", Created: now.AddDate(0, 0, -100), Status: skills.StatusArchived},
	}}
	m := skillwire.NewMetrics()
	rep, err := m.Snapshot(ctx, log, lib, now)
	if err != nil {
		t.Fatal(err)
	}
	if rep.ActiveSkills != 1 {
		t.Errorf("active: want 1, got %d", rep.ActiveSkills)
	}
	if rep.StaleSkills != 1 {
		t.Errorf("stale: want 1, got %d", rep.StaleSkills)
	}
	if rep.ArchivedSkills != 1 {
		t.Errorf("archived: want 1, got %d", rep.ArchivedSkills)
	}
	if rep.TotalReuseCount != 4 {
		t.Errorf("reuse: want 4, got %d", rep.TotalReuseCount)
	}
	if rep.Pending != 1 {
		t.Errorf("pending: want 1, got %d", rep.Pending)
	}
	// Survival: a is 120d old, active → in 30/60/90 cohorts.
	// b is 45d old, stale (still counted as active for survival).
	// c is archived, excluded.
	if rep.Survival30dCount != 2 {
		t.Errorf("survival30d count: want 2, got %d", rep.Survival30dCount)
	}
	if rep.Survival90dCount != 1 {
		t.Errorf("survival90d count: want 1, got %d", rep.Survival90dCount)
	}
}
