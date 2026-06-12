package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
)

const testToken = "test-bearer-token-1234567890"

func newTestServer(t *testing.T, bound string) (*Server, *agent.SQLiteEventLog, *GroupStore) {
	t.Helper()
	log, path := newTestLog(t)
	gs := newTestGroups(t, path)
	s := NewServer(Options{Log: log, Groups: gs, Token: testToken, BoundAddr: bound})
	return s, log, gs
}

func contextWithImmediateCancel() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

// do issues a request against the server's auth-wrapped handler with a
// valid bearer token unless override is set.
func do(t *testing.T, s *Server, method, target string, body any, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, rdr)
	req.Header.Set("Authorization", "Bearer "+testToken)
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestAuth_TokenGate(t *testing.T) {
	s, _, _ := newTestServer(t, "")

	// Missing token.
	req := httptest.NewRequest("GET", "/api/threads", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", rec.Code)
	}

	// Wrong token.
	req = httptest.NewRequest("GET", "/api/threads", nil)
	req.Header.Set("Authorization", "Bearer nope")
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong token: got %d, want 401", rec.Code)
	}

	// Right token.
	rec = do(t, s, "GET", "/api/threads", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("good token: got %d, want 200", rec.Code)
	}

	// SSE authenticates via ?token= (EventSource can't set headers).
	req = httptest.NewRequest("GET", "/api/threads/x/stream?token="+testToken, nil)
	rec = httptest.NewRecorder()
	// Use a context we can cancel so the stream handler returns.
	ctx, cancel := contextWithImmediateCancel()
	req = req.WithContext(ctx)
	cancel()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Error("SSE with ?token= should authenticate")
	}
}

func TestAuth_OriginGate(t *testing.T) {
	s, _, _ := newTestServer(t, "127.0.0.1:7777")

	// Default httptest Host is example.com -> rejected.
	rec := do(t, s, "GET", "/api/threads", nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("foreign host: got %d, want 403", rec.Code)
	}

	// Correct bound host -> allowed.
	rec = do(t, s, "GET", "/api/threads", nil, func(r *http.Request) {
		r.Host = "127.0.0.1:7777"
	})
	if rec.Code != http.StatusOK {
		t.Errorf("bound host: got %d, want 200", rec.Code)
	}

	// Cross-origin page -> rejected even with the right host.
	rec = do(t, s, "GET", "/api/threads", nil, func(r *http.Request) {
		r.Host = "127.0.0.1:7777"
		r.Header.Set("Origin", "http://evil.example.com")
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("bad origin: got %d, want 403", rec.Code)
	}
}

func TestThreads_ListWithGroupOverlay(t *testing.T) {
	s, log, gs := newTestServer(t, "")
	seedThread(t, log, "t1", "thread one", "hello")
	seedThread(t, log, "t2", "thread two", "hi there")
	g, _ := gs.Create(t.Context(), "carlos web")
	mustSet(t, gs, "t1", &g.ID)

	rec := do(t, s, "GET", "/api/threads", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d", rec.Code)
	}
	var got []ThreadSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d threads, want 2", len(got))
	}
	byID := map[string]ThreadSummary{}
	for _, s := range got {
		byID[s.ID] = s
	}
	if byID["t1"].GroupID == nil || *byID["t1"].GroupID != g.ID {
		t.Errorf("t1 group overlay missing: %+v", byID["t1"])
	}
	if byID["t2"].GroupID != nil {
		t.Errorf("t2 should be ungrouped, got %v", byID["t2"].GroupID)
	}
	if byID["t1"].Backend != "carlos" {
		t.Errorf("backend = %q, want carlos", byID["t1"].Backend)
	}
	if byID["t1"].Attached {
		t.Error("read-only backend should report attached=false")
	}
	if byID["t1"].State != "running" {
		t.Errorf("state = %q, want running", byID["t1"].State)
	}
}

func TestThreads_ListShowsConversationsAndLiveBlanksHidesSpawnsAndDeadEmpties(t *testing.T) {
	s, log, _ := newTestServer(t, "")
	seedThread(t, log, "chat1", "chat with george", "hey carlos")     // conversation w/ messages
	seedBlankThread(t, log, "blank1", "untitled thread")              // fresh LIVE empty conversation
	seedNonConversationRoot(t, log, "res1", "research: webgpu")       // research root (content, no msgs)
	seedNonConversationRoot(t, log, "task1", "Generate a puppy name") // sub-agent/headless task (content)
	seedTerminalEmpty(t, log, "orphan1", "chat with george")          // abandoned empty session
	// A done task root with no events at all (the a-... rows in real data).
	seedEmptyWithState(t, log, "donetask1", "Think of a spaceship name", agent.StateDone)

	rec := do(t, s, "GET", "/api/threads", nil)
	var got []ThreadSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ids := map[string]bool{}
	for _, t := range got {
		ids[t.ID] = true
	}
	// Real conversation and a still-live blank conversation stay.
	if !ids["chat1"] {
		t.Error("chat conversation must be listed")
	}
	if !ids["blank1"] {
		t.Error("a freshly minted LIVE blank conversation must stay visible")
	}
	// Spawned roots (content, no user msgs) and terminal empties are hidden.
	for _, hidden := range []string{"res1", "task1", "orphan1", "donetask1"} {
		if ids[hidden] {
			t.Errorf("%q must be hidden from the roster, got list %v", hidden, ids)
		}
	}
	if len(got) != 2 {
		t.Fatalf("got %d threads, want 2 (chat + live blank)", len(got))
	}
}

func TestThreads_ListFailsWhenLogClosed(t *testing.T) {
	s, log, _ := newTestServer(t, "")
	_ = log.Close() // ListUserSessions now errors
	rec := do(t, s, "GET", "/api/threads", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("got %d, want 500", rec.Code)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("list_failed")) {
		t.Errorf("body %q missing list_failed", rec.Body.String())
	}
}

func TestThreads_EventsFromAndLimit(t *testing.T) {
	s, log, _ := newTestServer(t, "")
	seedThread(t, log, "t1", "one", "first") // seq 1 (user_message)
	appendEvent(t, log, "t1", agent.EvtAssistantMessage, agent.MessagePayload{Text: "reply one"})
	appendEvent(t, log, "t1", agent.EvtUserMessage, agent.MessagePayload{Text: "second"})

	rec := do(t, s, "GET", "/api/threads/t1/events?from=0", nil)
	var all []WireEvent
	json.Unmarshal(rec.Body.Bytes(), &all)
	if len(all) != 3 {
		t.Fatalf("from=0 got %d events, want 3", len(all))
	}

	// limit=1 returns just the first forwarded event.
	rec = do(t, s, "GET", "/api/threads/t1/events?from=0&limit=1", nil)
	var lim []WireEvent
	json.Unmarshal(rec.Body.Bytes(), &lim)
	if len(lim) != 1 {
		t.Fatalf("limit=1 got %d, want 1", len(lim))
	}

	// from=<seq of first> skips it.
	rec = do(t, s, "GET", "/api/threads/t1/events?from=1", nil)
	var rest []WireEvent
	json.Unmarshal(rec.Body.Bytes(), &rest)
	for _, e := range rest {
		if e.Seq <= 1 {
			t.Errorf("from=1 should exclude seq<=1, got seq %d", e.Seq)
		}
	}
}

// deleteBackend is a stub whose Delete returns a configured result, for
// exercising the handler's error mapping.
type deleteBackend struct {
	readOnlyBackend
	n   int
	err error
}

func (b deleteBackend) Delete(string) (int, error) { return b.n, b.err }

func TestThreads_DeleteHandlerMapsResults(t *testing.T) {
	mk := func(n int, err error) *Server {
		log, path := newTestLog(t)
		gs := newTestGroups(t, path)
		return NewServer(Options{Log: log, Groups: gs, Token: testToken, Backend: deleteBackend{n: n, err: err}})
	}
	cases := []struct {
		name    string
		n       int
		err     error
		want    int
		wantKey string // a substring expected in the body
	}{
		{"success", 3, nil, http.StatusOK, `"deleted":3`},
		{"live", 0, agent.ErrSessionLive, http.StatusConflict, "thread_live"},
		{"not found", 0, agent.ErrSessionNotFound, http.StatusNotFound, "not_found"},
		{"unsupported", 0, ErrUnsupported, http.StatusNotImplemented, "unsupported"},
		{"other", 0, errors.New("boom"), http.StatusInternalServerError, "delete_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := mk(tc.n, tc.err)
			rec := do(t, s, "DELETE", "/api/threads/t1", nil)
			if rec.Code != tc.want {
				t.Fatalf("got %d, want %d", rec.Code, tc.want)
			}
			if !bytes.Contains(rec.Body.Bytes(), []byte(tc.wantKey)) {
				t.Errorf("body %q missing %q", rec.Body.String(), tc.wantKey)
			}
		})
	}
}

func TestThreads_DeleteReadOnlyIs501(t *testing.T) {
	s, log, _ := newTestServer(t, "")
	seedThread(t, log, "t1", "one", "hi")
	rec := do(t, s, "DELETE", "/api/threads/t1", nil)
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("read-only delete: got %d, want 501", rec.Code)
	}
}

func TestThreads_InteractiveOpsAre501InReadOnly(t *testing.T) {
	s, log, _ := newTestServer(t, "")
	seedThread(t, log, "t1", "one", "hi")
	for _, tc := range []struct {
		method, target string
		body           any
	}{
		{"POST", "/api/threads/t1/attach", nil},
		{"POST", "/api/threads/t1/messages", map[string]string{"text": "hi"}},
		{"POST", "/api/threads", map[string]string{"title": "new"}},
	} {
		rec := do(t, s, tc.method, tc.target, tc.body)
		if rec.Code != http.StatusNotImplemented {
			t.Errorf("%s %s: got %d, want 501", tc.method, tc.target, rec.Code)
		}
	}
}

func TestGroups_CRUDEndpoints(t *testing.T) {
	s, log, _ := newTestServer(t, "")
	seedThread(t, log, "t1", "one", "hi")

	// Create.
	rec := do(t, s, "POST", "/api/groups", map[string]string{"name": "anneal"})
	if rec.Code != http.StatusOK {
		t.Fatalf("create group: %d", rec.Code)
	}
	var g Group
	json.Unmarshal(rec.Body.Bytes(), &g)

	// Empty name -> 400.
	rec = do(t, s, "POST", "/api/groups", map[string]string{"name": ""})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty name: got %d, want 400", rec.Code)
	}

	// Assign thread.
	rec = do(t, s, "PUT", "/api/threads/t1/group", map[string]any{"group_id": g.ID})
	if rec.Code != http.StatusNoContent {
		t.Errorf("assign: got %d, want 204", rec.Code)
	}

	// Assign to unknown group -> 404.
	rec = do(t, s, "PUT", "/api/threads/t1/group", map[string]any{"group_id": "nope"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("assign unknown: got %d, want 404", rec.Code)
	}

	// Patch rename.
	rec = do(t, s, "PATCH", "/api/groups/"+g.ID, map[string]any{"name": "anneal-renamed"})
	if rec.Code != http.StatusOK {
		t.Errorf("patch: got %d", rec.Code)
	}

	// Patch unknown -> 404.
	rec = do(t, s, "PATCH", "/api/groups/nope", map[string]any{"name": "x"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("patch unknown: got %d, want 404", rec.Code)
	}

	// Delete reverts members.
	rec = do(t, s, "DELETE", "/api/groups/"+g.ID, nil)
	if rec.Code != http.StatusNoContent {
		t.Errorf("delete: got %d, want 204", rec.Code)
	}
	rec = do(t, s, "GET", "/api/groups", nil)
	var list []Group
	json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 0 {
		t.Errorf("after delete got %d groups, want 0", len(list))
	}
}
