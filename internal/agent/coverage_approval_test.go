package agent_test

// Coverage for ListPendingApprovals error / skip / ordering branches:
//
//   - closed-DB query error.
//   - malformed proposal and resolution payloads are skipped (not fatal).
//   - sortByProposedAt orders by ProposedAt (distinct timestamps) and
//     breaks ties on Ref.ID.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

func mkProposalPayload(t *testing.T, refID string) []byte {
	t.Helper()
	payload, err := json.Marshal(agent.ApprovalProposalPayload{
		Title: "plan " + refID,
		Ref:   mkRef(refID),
	})
	if err != nil {
		t.Fatalf("marshal proposal: %v", err)
	}
	return payload
}

func TestListPendingApprovals_ClosedDBErrors(t *testing.T) {
	log := closedLog(t, nil)
	if _, err := agent.ListPendingApprovals(context.Background(), log); err == nil {
		t.Fatal("ListPendingApprovals on closed DB should error")
	}
}

// TestListPendingApprovals_SkipsMalformedPayloads appends a proposal and a
// resolution event with non-JSON payloads directly; both should be
// skipped without erroring the queue.
func TestListPendingApprovals_SkipsMalformedPayloads(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()

	// One good proposal so the list is non-empty after the skips.
	good := mkRef("good-art")
	if _, err := agent.ProposeApproval(ctx, log, "agent-1", "good plan", good); err != nil {
		t.Fatalf("propose good: %v", err)
	}

	// Malformed proposal payload.
	if _, err := log.Append(ctx, agent.Event{
		AgentID: "agent-1", TS: time.Now().UTC(), Type: agent.EvtApprovalProposed, Payload: []byte("not json {"),
	}); err != nil {
		t.Fatalf("append bad proposal: %v", err)
	}
	// Malformed resolution payload.
	if _, err := log.Append(ctx, agent.Event{
		AgentID: "user", TS: time.Now().UTC(), Type: agent.EvtApprovalAccepted, Payload: []byte("not json {"),
	}); err != nil {
		t.Fatalf("append bad resolution: %v", err)
	}

	pending, err := agent.ListPendingApprovals(ctx, log)
	if err != nil {
		t.Fatalf("list should tolerate malformed payloads: %v", err)
	}
	if len(pending) != 1 || pending[0].Ref.ID != "good-art" {
		t.Fatalf("expected only the good proposal, got %+v", pending)
	}
}

// TestListPendingApprovals_OrdersByProposedAtAndID seeds three proposals:
// two with the same (truncated) timestamp and one earlier. The earlier
// one must sort first; the tie pair must break on Ref.ID. Because
// ProposeApproval stamps time.Now, we instead append crafted proposal
// events with explicit timestamps through the log so we control ordering.
func TestListPendingApprovals_OrdersByProposedAtAndID(t *testing.T) {
	log := openLog(t)
	ctx := context.Background()

	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Two proposals at the SAME timestamp (tie → break on Ref.ID), one
	// LATER. We append directly to control TS precisely.
	type prop struct {
		id string
		ts time.Time
	}
	for _, p := range []prop{
		{"zzz-same-ts", t0},          // tie, higher id
		{"aaa-same-ts", t0},          // tie, lower id → should come before zzz
		{"later", t0.Add(time.Hour)}, // strictly later → sorts last
	} {
		payload := mkProposalPayload(t, p.id)
		if _, err := log.Append(ctx, agent.Event{
			AgentID: "agent-1", TS: p.ts, Type: agent.EvtApprovalProposed, Payload: payload,
		}); err != nil {
			t.Fatalf("append %s: %v", p.id, err)
		}
	}

	pending, err := agent.ListPendingApprovals(ctx, log)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pending) != 3 {
		t.Fatalf("want 3 pending, got %d", len(pending))
	}
	// Tie pair first (same ts), ordered by id: aaa then zzz, then later.
	if pending[0].Ref.ID != "aaa-same-ts" {
		t.Errorf("pending[0] = %s, want aaa-same-ts (tie broken by id)", pending[0].Ref.ID)
	}
	if pending[1].Ref.ID != "zzz-same-ts" {
		t.Errorf("pending[1] = %s, want zzz-same-ts", pending[1].Ref.ID)
	}
	if pending[2].Ref.ID != "later" {
		t.Errorf("pending[2] = %s, want later (latest ProposedAt last)", pending[2].Ref.ID)
	}
}
