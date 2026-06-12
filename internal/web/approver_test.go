package web

import (
	"sync"
	"testing"
	"time"
)

// collectPub returns a publish func that appends to a slice under a lock,
// plus an accessor.
func collectPub() (func(WireEvent), func() []WireEvent) {
	var mu sync.Mutex
	var got []WireEvent
	return func(ev WireEvent) {
			mu.Lock()
			got = append(got, ev)
			mu.Unlock()
		}, func() []WireEvent {
			mu.Lock()
			defer mu.Unlock()
			out := make([]WireEvent, len(got))
			copy(out, got)
			return out
		}
}

func TestWebApprover_AllowOnce(t *testing.T) {
	pub, _ := collectPub()
	a := NewWebApprover("t1", pub)

	result := make(chan bool, 1)
	go func() { result <- a.ApproveToolCall("Bash", []byte(`{"command":"ls"}`)) }()

	reqID := waitForPending(t, a)
	if err := a.Resolve(reqID, "allow"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := <-result; !got {
		t.Error("allow should approve the call")
	}
}

func TestWebApprover_Deny(t *testing.T) {
	pub, _ := collectPub()
	a := NewWebApprover("t1", pub)
	result := make(chan bool, 1)
	go func() { result <- a.ApproveToolCall("Bash", nil) }()
	reqID := waitForPending(t, a)
	if err := a.Resolve(reqID, "deny"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := <-result; got {
		t.Error("deny should reject the call")
	}
}

func TestWebApprover_AllowAlwaysCachesPerTool(t *testing.T) {
	pub, _ := collectPub()
	a := NewWebApprover("t1", pub)

	// First call blocks until resolved with allow_always.
	result := make(chan bool, 1)
	go func() { result <- a.ApproveToolCall("Bash", nil) }()
	reqID := waitForPending(t, a)
	if err := a.Resolve(reqID, "allow_always"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := <-result; !got {
		t.Error("allow_always should approve the first call")
	}

	// Subsequent Bash calls short-circuit without a pending request.
	done := make(chan bool, 1)
	go func() { done <- a.ApproveToolCall("Bash", nil) }()
	select {
	case got := <-done:
		if !got {
			t.Error("cached tool should auto-approve")
		}
	case <-time.After(time.Second):
		t.Error("cached Bash call should not block on a new request")
	}

	// A different tool still prompts.
	if len(a.Pending()) != 0 {
		t.Errorf("no requests should be pending, got %d", len(a.Pending()))
	}
	other := make(chan bool, 1)
	go func() { other <- a.ApproveToolCall("Write", nil) }()
	waitForPending(t, a) // Write is not cached -> a request appears
}

func TestWebApprover_CloseDeniesPending(t *testing.T) {
	pub, _ := collectPub()
	a := NewWebApprover("t1", pub)
	result := make(chan bool, 1)
	go func() { result <- a.ApproveToolCall("Bash", nil) }()
	waitForPending(t, a)

	a.Close()
	select {
	case got := <-result:
		if got {
			t.Error("close should deny the pending call (loop must not hang)")
		}
	case <-time.After(time.Second):
		t.Error("close did not unblock the pending approval")
	}
	a.Close() // idempotent
}

func TestWebApprover_ResolveUnknownErrors(t *testing.T) {
	pub, _ := collectPub()
	a := NewWebApprover("t1", pub)
	if err := a.Resolve("req_999", "allow"); err == nil {
		t.Error("resolving an unknown request should error (maps to 404)")
	}
}

func TestWebApprover_PublishesRequestAndResolved(t *testing.T) {
	pub, snap := collectPub()
	a := NewWebApprover("t1", pub)
	result := make(chan bool, 1)
	go func() { result <- a.ApproveToolCall("Bash", []byte(`{"command":"ls"}`)) }()
	reqID := waitForPending(t, a)
	_ = a.Resolve(reqID, "allow")
	<-result

	var sawReq, sawResolved bool
	for _, ev := range snap() {
		switch ev.Kind {
		case "approval_request":
			sawReq = true
		case "approval_resolved":
			sawResolved = true
		}
	}
	if !sawReq || !sawResolved {
		t.Errorf("expected request+resolved fan-out, got req=%v resolved=%v", sawReq, sawResolved)
	}
}

// waitForPending polls until exactly one approval is pending and returns
// its request id.
func waitForPending(t *testing.T, a *WebApprover) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		p := a.Pending()
		if len(p) == 1 {
			return p[0].Data.(map[string]any)["request_id"].(string)
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("no pending approval appeared")
	return ""
}
