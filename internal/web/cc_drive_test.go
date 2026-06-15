package web

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// driveBackend returns a CCBackend with driving enabled over a throwaway
// hub, so the approval bridge can be exercised without spawning claude.
func driveBackend(t *testing.T) *CCBackend {
	t.Helper()
	b := newCCBackendAt(t.TempDir())
	b.EnableDrive(context.Background(), newEphemeralHub(), "http://127.0.0.1:0", "tok", "/bin/carlos")
	return b
}

func TestCCBackend_CapsReflectDrive(t *testing.T) {
	observe := newCCBackendAt(t.TempDir())
	if observe.Caps()["send"] {
		t.Error("observe-only backend must not advertise send")
	}
	drive := driveBackend(t)
	caps := drive.Caps()
	if !caps["send"] || !caps["approve"] {
		t.Errorf("drive backend caps = %v, want send+approve", caps)
	}
}

// The approval bridge: RequestApproval (the hook's side) blocks until
// Resolve (the browser's side) answers, surfacing approval_request and
// approval_resolved on the hub in between.
func TestCCBackend_ApprovalRoundTrip(t *testing.T) {
	b := driveBackend(t)
	ch, unsub := b.hub.subscribe("cc:t1")
	defer unsub()

	got := make(chan string, 1)
	go func() {
		got <- b.RequestApproval("cc:t1", "Bash", json.RawMessage(`{"command":"ls"}`))
	}()

	// The approval_request reaches the SSE hub with a request_id we can
	// resolve.
	var reqID string
	select {
	case ev := <-ch:
		if ev.Kind != "approval_request" {
			t.Fatalf("first hub event = %q, want approval_request", ev.Kind)
		}
		reqID = ev.Data.(map[string]any)["request_id"].(string)
	case <-time.After(2 * time.Second):
		t.Fatal("no approval_request on the hub")
	}

	// It is visible to a reconnecting client until resolved.
	if pend := b.PendingApprovals("cc:t1"); len(pend) != 1 {
		t.Errorf("PendingApprovals = %d, want 1 while blocked", len(pend))
	}

	if err := b.Resolve("cc:t1", reqID, "allow"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	select {
	case d := <-got:
		if d != "allow" {
			t.Errorf("decision = %q, want allow", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RequestApproval never returned after Resolve")
	}
	if pend := b.PendingApprovals("cc:t1"); len(pend) != 0 {
		t.Errorf("PendingApprovals = %d after resolve, want 0", len(pend))
	}
}

// allow_always collapses to allow for CC (the hook cannot express a durable
// always); deny and unknown decisions deny.
func TestCCBackend_ResolveDecisionMapping(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"allow", "allow"}, {"allow_always", "allow"}, {"deny", "deny"},
	} {
		b := driveBackend(t)
		got := make(chan string, 1)
		go func() { got <- b.RequestApproval("cc:t1", "Edit", nil) }()
		// wait for the pending registration
		var rid string
		for i := 0; i < 100 && rid == ""; i++ {
			b.driveMu.Lock()
			for id := range b.pending {
				rid = id
			}
			b.driveMu.Unlock()
			time.Sleep(time.Millisecond)
		}
		if rid == "" {
			t.Fatal("approval never registered")
		}
		_ = b.Resolve("cc:t1", rid, tc.in)
		if d := <-got; d != tc.want {
			t.Errorf("Resolve(%q) -> %q, want %q", tc.in, d, tc.want)
		}
	}
}

// Detaching a thread denies any approval still blocked on it (so the hook
// unblocks and claude reports the tool as denied).
func TestCCBackend_DetachDeniesPending(t *testing.T) {
	b := driveBackend(t)
	got := make(chan string, 1)
	go func() { got <- b.RequestApproval("cc:t1", "Bash", nil) }()
	for i := 0; i < 100; i++ {
		if len(b.PendingApprovals("cc:t1")) == 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	_ = b.Detach("cc:t1")
	select {
	case d := <-got:
		if d != "deny" {
			t.Errorf("detach decision = %q, want deny", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("detach did not unblock the pending approval")
	}
}

// The HTTP intake handler blocks until the browser resolves, then returns
// the decision the hook relays to claude.
func TestServer_CCHookEndpoint(t *testing.T) {
	log, path := newTestLog(t)
	gs := newTestGroups(t, path)
	cc := driveBackend(t)
	s := NewServer(Options{Log: log, Groups: gs, Token: testToken})
	s.Register(cc)

	// Fire the hook request on a goroutine (it blocks on the human).
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		body := `{"thread":"cc:t1","tool_name":"Bash","tool_input":{"command":"ls"}}`
		req := httptest.NewRequest("POST", "/api/cc/hook", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testToken)
		s.Handler().ServeHTTP(rec, req)
		close(done)
	}()

	// Resolve it from "the browser".
	var rid string
	for i := 0; i < 200 && rid == ""; i++ {
		for _, ev := range cc.PendingApprovals("cc:t1") {
			rid = ev.Data.(map[string]any)["request_id"].(string)
		}
		time.Sleep(time.Millisecond)
	}
	if rid == "" {
		t.Fatal("hook never registered a pending approval")
	}
	_ = cc.Resolve("cc:t1", rid, "deny")

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("hook endpoint never returned")
	}
	if rec.Code != 200 {
		t.Fatalf("hook status = %d", rec.Code)
	}
	var out struct {
		Decision string `json:"decision"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Decision != "deny" {
		t.Errorf("hook decision = %q, want deny", out.Decision)
	}
}
