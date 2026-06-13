// Regression tests for the web crew column: carlosBackend.Children must
// answer from the agents projection (parent_id lineage), so a thread
// whose sub-agents already FINISHED still reports them - with final
// state and spend - and the SSE stream carries both the connect-time
// children snapshot and live publishChildren updates.
package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/web"
)

func childrenTestConfig() *config.Config {
	return &config.Config{
		UserName:        "george",
		DefaultProvider: "anthropic",
		Providers: map[string]config.ProviderConfig{
			"anthropic": {APIKey: "test-key"},
		},
		Frames: frame.Config{
			Active: "personal",
			List:   []frame.Frame{{Name: "personal"}},
		},
	}
}

func newChildrenTestBackend(t *testing.T) (*carlosBackend, *web.Server, *agent.SQLiteEventLog) {
	t.Helper()
	t.Setenv("CARLOS_FRAME", "")
	log, err := agent.OpenSQLiteEventLog(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	srv := web.NewServer(web.Options{Log: log, Token: "test-token"})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	b, err := newCarlosBackend(ctx, childrenTestConfig(), log, srv)
	if err != nil {
		t.Fatalf("newCarlosBackend: %v", err)
	}
	t.Cleanup(b.Shutdown)
	srv.SetBackend(b)
	return b, srv, log
}

func insertRow(t *testing.T, log *agent.SQLiteEventLog, id, parentID string, state agent.State) {
	t.Helper()
	now := time.Now().UTC()
	root := id
	if parentID != "" {
		root = parentID
	}
	if err := log.InsertAgent(context.Background(), agent.AgentRow{
		ID: id, ParentID: parentID, RootID: root, State: state, Attempt: 1,
		Title: "row " + id, CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

// TestCarlosBackend_ChildrenIncludesFinished is the durable half of the
// crew-column fix: children whose loops terminated long ago (no entry
// in the supervisor's in-memory map) must still be reported, so the
// right panel appears when the user navigates back to the thread.
func TestCarlosBackend_ChildrenIncludesFinished(t *testing.T) {
	b, _, log := newChildrenTestBackend(t)
	insertRow(t, log, "t1", "", agent.StateRunning)
	insertRow(t, log, "c1", "t1", agent.StateDone)
	insertRow(t, log, "c2", "t1", agent.StateFailed)

	kids := b.Children(context.Background(), "t1")
	if len(kids) != 2 {
		t.Fatalf("children = %d (%+v), want 2 finished children", len(kids), kids)
	}
	if kids[0].ID != "c1" || kids[0].State != "done" {
		t.Errorf("kids[0] = %+v, want c1/done", kids[0])
	}
	if kids[1].ID != "c2" || kids[1].State != "failed" {
		t.Errorf("kids[1] = %+v, want c2/failed", kids[1])
	}
	if kids[0].StartedAt == "" {
		t.Error("started_at empty")
	}
}

// TestCarlosBackend_ChildrenWireStateAndSpend pins the ChildSnap field
// mapping the SPA's crew rail renders verbatim: states use the WIRE
// vocabulary (underscore-form via web.WireStateString, never the
// dash-form State.String()), and the spend columns + last-tool
// enrichment pass through. Bad path included: a child with zero events
// and zero spend still maps sanely (empty last_tool, zero tokens/cost)
// instead of being dropped or garbled.
func TestCarlosBackend_ChildrenWireStateAndSpend(t *testing.T) {
	b, _, log := newChildrenTestBackend(t)
	insertRow(t, log, "t1", "", agent.StateRunning)
	insertRow(t, log, "c1", "t1", agent.StateAwaitingInput)
	insertRow(t, log, "c2", "t1", agent.StateDone)

	// c1 ran a tool and burned budget; c2 is the zero-event bad path.
	if err := log.AddAgentUsage(context.Background(), "c1", 800, 400, 3, time.Now().UTC()); err != nil {
		t.Fatalf("seed spend: %v", err)
	}
	payload, _ := json.Marshal(agent.ToolCall{Name: "read_file", Input: []byte(`{}`)})
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: "c1", TS: time.Now().UTC(), Type: agent.EvtToolCall, Payload: payload,
	}); err != nil {
		t.Fatalf("seed tool call: %v", err)
	}

	kids := b.Children(context.Background(), "t1")
	if len(kids) != 2 {
		t.Fatalf("children = %+v, want 2", kids)
	}
	c1, c2 := kids[0], kids[1]
	if c1.State != "awaiting_input" {
		t.Errorf("c1 state = %q, want wire-form awaiting_input (not State.String()'s awaiting-input)", c1.State)
	}
	if c1.Tokens != 1200 || c1.CostCents != 3 {
		t.Errorf("c1 spend = %d tok / %d cents, want 1200 / 3", c1.Tokens, c1.CostCents)
	}
	if c1.LastTool != "read_file" {
		t.Errorf("c1 last_tool = %q, want read_file", c1.LastTool)
	}
	if c2.State != "done" || c2.Tokens != 0 || c2.CostCents != 0 || c2.LastTool != "" {
		t.Errorf("c2 (zero-event child) = %+v, want done with zero spend and empty last_tool", c2)
	}
}

// Bad path: a thread with no lineage answers nil (the HTTP layer maps
// that to {"children": []}); children of OTHER threads never leak in.
func TestCarlosBackend_ChildrenEmptyAndScoped(t *testing.T) {
	b, _, log := newChildrenTestBackend(t)
	insertRow(t, log, "t1", "", agent.StateRunning)
	insertRow(t, log, "t2", "", agent.StateRunning)
	insertRow(t, log, "c1", "t1", agent.StateDone)

	if kids := b.Children(context.Background(), "t2"); len(kids) != 0 {
		t.Errorf("t2 children = %+v, want none (leaked from t1)", kids)
	}
	if kids := b.Children(context.Background(), "ghost"); len(kids) != 0 {
		t.Errorf("ghost children = %+v, want none", kids)
	}
}

// publishChildren must not assume a server-lifetime ctx: a backend built
// without one (defensive nil) falls back to context.Background instead of
// panicking inside the DB read.
func TestCarlosBackend_PublishChildrenNilLifeCtx(t *testing.T) {
	log, err := agent.OpenSQLiteEventLog(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	insertRow(t, log, "t1", "", agent.StateRunning)
	insertRow(t, log, "c1", "t1", agent.StateDone)

	srv := web.NewServer(web.Options{Log: log, Token: "test-token"})
	b := &carlosBackend{log: log, hub: srv} // lifeCtx deliberately nil
	b.publishChildren("t1")                 // must not panic; publish is fire-and-forget
}

// syncRecorder wraps httptest.ResponseRecorder so the SSE handler
// goroutine and the asserting test goroutine never touch the body
// buffer concurrently.
type syncRecorder struct {
	*httptest.ResponseRecorder
	mu sync.Mutex
}

func (r *syncRecorder) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Write(b)
}

func (r *syncRecorder) body() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Body.String()
}

// TestCarlosBackend_SSECarriesChildrenSnapshotAndLivePush covers both
// delivery paths of the crew column:
//   - connect-time: a thread with FINISHED children gets a `children`
//     snapshot in the SSE ephemeral snapshot (the navigate-back case);
//   - live: publishChildren (the supervisor's child notifier) pushes a
//     fresh snapshot to an already-open stream (the spawn-moment case).
func TestCarlosBackend_SSECarriesChildrenSnapshotAndLivePush(t *testing.T) {
	b, srv, log := newChildrenTestBackend(t)
	insertRow(t, log, "t1", "", agent.StateRunning)
	insertRow(t, log, "c1", "t1", agent.StateDone)

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("GET", "/api/threads/t1/stream?token=test-token", nil).WithContext(ctx)
	rec := &syncRecorder{ResponseRecorder: httptest.NewRecorder()}

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Handler().ServeHTTP(rec, req)
	}()

	// Connect-time snapshot: wait for the finished child to appear.
	waitFor := func(substr, what string) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if strings.Contains(rec.body(), substr) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		cancel()
		t.Fatalf("%s: %q never appeared in stream:\n%s", what, substr, rec.body())
	}
	waitFor(`"kind":"children"`, "connect snapshot")
	waitFor(`"id":"c1"`, "finished child in snapshot")

	// Live push: a second child lands (as if mid-turn) and the notifier
	// fires; the open stream must carry the refreshed roster.
	insertRow(t, log, "c2", "t1", agent.StateRunning)
	b.publishChildren("t1")
	waitFor(`"id":"c2"`, "live publishChildren update")

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stream handler did not exit on ctx cancel")
	}
}
