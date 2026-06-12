package web

// Regression coverage for the sub-agent roster bug: the left-bar roster
// (GET /api/threads) must list ONLY top-level conversations - never
// spawned children, live or finished - while a child's transcript stays
// readable by id (GET /api/threads/{id}/events) for lineage inspection,
// and the children endpoint surfaces finished crews.

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// seedChild inserts a sub-agent row (parent_id set, root_id = parent)
// in the given state - the lineage shape Supervisor.Spawn writes now
// that the Agent tool threads the thread id through ctx.
func seedChild(t *testing.T, log *agent.SQLiteEventLog, id, parentID string, state agent.State) {
	t.Helper()
	now := time.Now().UTC()
	if err := log.InsertAgent(context.Background(), agent.AgentRow{
		ID: id, ParentID: parentID, RootID: parentID, State: state, Attempt: 1,
		Title: "sub " + id, CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("insert child %s: %v", id, err)
	}
}

func TestThreads_RosterExcludesSubAgents(t *testing.T) {
	s, log, _ := newTestServer(t, "")
	seedThread(t, log, "t1", "the conversation", "spawn two agents")
	// Two children: one still running, one finished. Neither may appear.
	seedChild(t, log, "c1", "t1", agent.StateRunning)
	seedChild(t, log, "c2", "t1", agent.StateDone)

	rec := do(t, s, "GET", "/api/threads", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d", rec.Code)
	}
	var got []ThreadSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 1 || got[0].ID != "t1" {
		ids := make([]string, 0, len(got))
		for _, ts := range got {
			ids = append(ids, ts.ID)
		}
		t.Errorf("roster = %v, want [t1] only (children leaked into the left bar)", ids)
	}
}

func TestThreads_GetThreadHidesSubAgents(t *testing.T) {
	// The thread-summary detail endpoint serves conversations; a child id
	// is not a conversation. (Inspection goes through /events - below.)
	s, log, _ := newTestServer(t, "")
	seedThread(t, log, "t1", "the conversation", "hello")
	seedChild(t, log, "c1", "t1", agent.StateDone)

	if rec := do(t, s, "GET", "/api/threads/t1", nil); rec.Code != http.StatusOK {
		t.Errorf("get parent: got %d, want 200", rec.Code)
	}
	if rec := do(t, s, "GET", "/api/threads/c1", nil); rec.Code != http.StatusNotFound {
		t.Errorf("get child: got %d, want 404 (children are not threads)", rec.Code)
	}
}

func TestThreads_ChildEventsStayReadable(t *testing.T) {
	// Lineage inspection: a child's transcript is still readable by id
	// even though the child is excluded from the roster.
	s, log, _ := newTestServer(t, "")
	seedThread(t, log, "t1", "the conversation", "hello")
	seedChild(t, log, "c1", "t1", agent.StateDone)
	appendEvent(t, log, "c1", agent.EvtAssistantMessage, agent.MessagePayload{Text: "child findings"})

	rec := do(t, s, "GET", "/api/threads/c1/events", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("child events: got %d, want 200", rec.Code)
	}
	var evs []WireEvent
	if err := json.Unmarshal(rec.Body.Bytes(), &evs); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(evs) != 1 || evs[0].Kind != "assistant_message" {
		t.Errorf("child events = %+v, want the assistant_message", evs)
	}
}

// childrenBackend stubs Backend.Children with a fixed snapshot - the
// shape the cmd-side backend returns for a thread whose children have
// all finished.
type childrenBackend struct {
	readOnlyBackend
	kids []ChildSnap
}

func (b childrenBackend) Children(context.Context, string) []ChildSnap { return b.kids }

func TestChildren_EndpointReturnsFinishedChildren(t *testing.T) {
	log, path := newTestLog(t)
	gs := newTestGroups(t, path)
	finished := []ChildSnap{
		{ID: "c1", State: "done", Title: "first", LastTool: "read_file", Tokens: 1200, CostCents: 3, StartedAt: "2026-06-12T00:00:00Z"},
		{ID: "c2", State: "failed", Title: "second", Tokens: 400, CostCents: 1, StartedAt: "2026-06-12T00:00:01Z"},
	}
	s := NewServer(Options{Log: log, Groups: gs, Token: testToken, Backend: childrenBackend{kids: finished}})

	rec := do(t, s, "GET", "/api/threads/t1/children", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("children: got %d", rec.Code)
	}
	var body struct {
		Children []ChildSnap `json:"children"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.Children) != 2 {
		t.Fatalf("children = %d, want 2", len(body.Children))
	}
	if body.Children[0].State != "done" || body.Children[1].State != "failed" {
		t.Errorf("states = [%s %s], want [done failed]", body.Children[0].State, body.Children[1].State)
	}
}

func TestChildren_EndpointEmptyListNotNull(t *testing.T) {
	// A childless thread answers {"children": []} - never null - so the
	// SPA can assign without a guard.
	s, _, _ := newTestServer(t, "")
	rec := do(t, s, "GET", "/api/threads/t1/children", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("children: got %d", rec.Code)
	}
	if got := rec.Body.String(); !json.Valid([]byte(got)) || got == "" {
		t.Fatalf("invalid body %q", got)
	}
	var body map[string]json.RawMessage
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if string(body["children"]) == "null" {
		t.Error(`children = null, want []`)
	}
}

func TestChildrenEvent_WireShape(t *testing.T) {
	now := time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC)
	ev := ChildrenEvent("t1", []ChildSnap{{ID: "c1", State: "done"}}, now)
	if ev.Kind != "children" || ev.Thread != "t1" || ev.Seq != 0 {
		t.Errorf("envelope = %+v, want ephemeral children event for t1", ev)
	}
	data, ok := ev.Data.(map[string]any)
	if !ok {
		t.Fatalf("data type %T", ev.Data)
	}
	kids, ok := data["children"].([]ChildSnap)
	if !ok || len(kids) != 1 || kids[0].ID != "c1" {
		t.Errorf("data.children = %#v", data["children"])
	}
	// Nil snapshot must serialize as [] (the column collapses on empty,
	// it never sees null).
	empty := ChildrenEvent("t1", nil, now)
	b, _ := json.Marshal(empty)
	if string(b) == "" || !json.Valid(b) {
		t.Fatal("marshal failed")
	}
	var round struct {
		Data struct {
			Children []ChildSnap `json:"children"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if round.Data.Children == nil {
		t.Error("nil kids serialized as null, want []")
	}
}
