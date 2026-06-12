package web

import (
	"fmt"
	"sync"
)

// WebApprover bridges the synchronous, blocking agent.Approver contract
// (ApproveToolCall runs on the loop goroutine and blocks) to the async
// HTTP surface, mirroring chat.TUIApprover (F5). It is bound to one
// thread: cmd/carlos constructs one per attached thread and wires it as
// the LayeredApprover fallback (D10).
//
// Mechanics (spec §10):
//  1. ApproveToolCall checks the per-thread "always" cache, else mints a
//     request_id, registers a reply channel, fans out approval_request on
//     the thread's SSE stream, and blocks on reply / closedCh.
//  2. Resolve sends the decision on the reply channel (buffered cap 1, so
//     a late resolution is a safe drop) and fans out approval_resolved.
//  3. allow_always populates the per-thread per-tool cache (session
//     lifetime, in-memory) - identical ergonomics to the TUI.
//  4. Close (detach/shutdown) unblocks every pending call as a deny, so
//     agent.Run reports "(rejected by user)" rather than hanging.
type WebApprover struct {
	hub      string // threadID this approver is bound to
	pub      func(WireEvent)
	mu       sync.Mutex
	seq      int
	pending  map[string]*pendingApproval
	always   map[string]bool // tool name -> always allow
	closed   chan struct{}
	closeOne sync.Once
}

type pendingApproval struct {
	name  string
	input []byte
	reply chan bool
}

// NewWebApprover binds an approver to threadID, publishing approval events
// through pub (the ephemeral hub's publish).
func NewWebApprover(threadID string, pub func(WireEvent)) *WebApprover {
	return &WebApprover{
		hub:     threadID,
		pub:     pub,
		pending: map[string]*pendingApproval{},
		always:  map[string]bool{},
		closed:  make(chan struct{}),
	}
}

// ApproveToolCall implements agent.Approver. It blocks until the user
// resolves the request via Resolve, the "always" cache short-circuits, or
// the approver is closed (detach/shutdown -> deny).
func (a *WebApprover) ApproveToolCall(name string, input []byte) bool {
	a.mu.Lock()
	if a.always[name] {
		a.mu.Unlock()
		return true
	}
	a.seq++
	reqID := fmt.Sprintf("req_%d", a.seq)
	p := &pendingApproval{name: name, input: input, reply: make(chan bool, 1)}
	a.pending[reqID] = p
	a.mu.Unlock()

	a.pub(approvalRequestEvent(a.hub, reqID, name, input))

	select {
	case ok := <-p.reply:
		return ok
	case <-a.closed:
		// Detach/shutdown: clean up and deny so the loop never hangs.
		a.mu.Lock()
		delete(a.pending, reqID)
		a.mu.Unlock()
		return false
	}
}

// Resolve answers a pending request. decision is "deny" | "allow" |
// "allow_always". Unknown request ids return an error the HTTP layer maps
// to 404 (expired/already resolved). Two-tab consistency: the
// approval_resolved fan-out clears the prompt on every connected client.
func (a *WebApprover) Resolve(requestID, decision string) error {
	a.mu.Lock()
	p, ok := a.pending[requestID]
	if !ok {
		a.mu.Unlock()
		return fmt.Errorf("web: unknown or expired approval %q", requestID)
	}
	delete(a.pending, requestID)
	allow := decision == "allow" || decision == "allow_always"
	if decision == "allow_always" {
		a.always[p.name] = true
	}
	a.mu.Unlock()

	// Buffered cap 1: if the loop already gave up (closed), this is a safe
	// drop rather than a blocked send (same trick as approver.go).
	select {
	case p.reply <- allow:
	default:
	}
	a.pub(approvalResolvedEvent(a.hub, requestID, decision))
	return nil
}

// Pending returns the currently-open approval requests as wire events, for
// the SSE reconnect snapshot (spec §9.3 step 4).
func (a *WebApprover) Pending() []WireEvent {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]WireEvent, 0, len(a.pending))
	for reqID, p := range a.pending {
		out = append(out, approvalRequestEvent(a.hub, reqID, p.name, p.input))
	}
	return out
}

// Close unblocks every pending call as a deny. Idempotent.
func (a *WebApprover) Close() {
	a.closeOne.Do(func() { close(a.closed) })
}

func approvalRequestEvent(threadID, reqID, name string, input []byte) WireEvent {
	return WireEvent{
		Thread: threadID,
		TS:     rfc3339(nowUTC()),
		Kind:   "approval_request",
		Data: map[string]any{
			"request_id": reqID,
			"name":       name,
			"input":      rawInput(input),
		},
	}
}

func approvalResolvedEvent(threadID, reqID, decision string) WireEvent {
	return WireEvent{
		Thread: threadID,
		TS:     rfc3339(nowUTC()),
		Kind:   "approval_resolved",
		Data:   map[string]any{"request_id": reqID, "decision": decision},
	}
}
