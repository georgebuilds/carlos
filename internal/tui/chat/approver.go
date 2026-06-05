// TUIApprover bridges agent.Run's synchronous Approver interface to
// the chat TUI's asynchronous render/keypress loop.
//
// agent.Run runs in a background goroutine (chatglue.Loop spins it on
// each EvtUserMessage). When the model wants to call a tool, the
// loop calls Approver.ApproveToolCall(name, input) synchronously and
// blocks on the result. We need a y/N/Always prompt to render in the
// chat view at that moment, accept user input, and unblock the loop.
//
// TUIApprover holds a request channel + a per-session "always" cache.
// ApproveToolCall pushes a request onto the channel and blocks on a
// per-request reply channel. The chat Model pumps the request channel
// into tea.Msgs, renders an overlay, and dispatches a decision back
// via Reply when the user hits a key.
//
// Session lifetime: the "always" cache is map[toolName]bool, reset
// when the process exits. "Always" is per-tool-name, not per-input —
// this is the right ergonomic tradeoff for `read` / `glob` / `grep`
// (low-risk and called often) while keeping `bash` / `write` / `edit`
// honest (the model still gets prompted per call, unless the user
// explicitly opts in).
package chat

import (
	"sync"
)

// ApprovalDecision is the y/N/Always tri-state returned via Reply.
type ApprovalDecision int

const (
	// ApprovalDeny rejects the call. agent.Run reports
	// "(rejected by user)" back to the model so it can adapt.
	ApprovalDeny ApprovalDecision = iota
	// ApprovalAllow approves this single call.
	ApprovalAllow
	// ApprovalAllowAlways approves this call and every subsequent
	// call to the same tool name in this session.
	ApprovalAllowAlways
)

// ApprovalRequest is one in-flight tool-call approval. The TUI reads
// these off the channel returned by Requests(), renders an overlay,
// and pushes a decision back via Reply. The reply channel is buffered
// (cap=1) so Reply never blocks even if the requester has already
// timed out / cancelled — the buffered send is a safe drop.
type ApprovalRequest struct {
	// Tool is the tool name (e.g. "bash", "write").
	Tool string
	// Input is the JSON-encoded tool input. Render it pretty for the
	// overlay; the schema is whatever the tool declared.
	Input []byte
	// reply carries the decision back to ApproveToolCall. Buffered.
	reply chan ApprovalDecision
}

// TUIApprover satisfies agent.Approver and exposes the request stream
// the chat Model consumes. Construct one per chat session.
type TUIApprover struct {
	requests chan *ApprovalRequest
	// closedCh closes when Close is called. ApproveToolCall watches
	// it on both the request-push and reply-receive selects so it
	// can never hang on a shut-down approver, regardless of where in
	// the request lifecycle Close lands.
	closedCh chan struct{}

	mu     sync.Mutex
	always map[string]bool
	closed bool
}

// NewTUIApprover returns a fresh approver with empty "always" cache
// and a buffered request channel (cap=16 — well above the worst-case
// pending approvals; agent.Run is synchronous so backpressure on this
// channel just means a tool call waits).
func NewTUIApprover() *TUIApprover {
	return &TUIApprover{
		requests: make(chan *ApprovalRequest, 16),
		closedCh: make(chan struct{}),
		always:   map[string]bool{},
	}
}

// ApproveToolCall is the agent.Approver impl. Blocks until the user
// (or a prior "Always" decision) resolves the request. Returns false
// on Deny, true on Allow / AllowAlways.
func (t *TUIApprover) ApproveToolCall(name string, input []byte) bool {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return false
	}
	if t.always[name] {
		t.mu.Unlock()
		return true
	}
	t.mu.Unlock()

	req := &ApprovalRequest{
		Tool:  name,
		Input: input,
		reply: make(chan ApprovalDecision, 1),
	}
	// Push-with-cancellation: if Close fired while we were preparing
	// the request, bail out instead of pushing into a buffer no one
	// will drain.
	select {
	case t.requests <- req:
	case <-t.closedCh:
		return false
	}
	// Reply-with-cancellation: same story on the receive side — a
	// Close mid-prompt unblocks us cleanly.
	var decision ApprovalDecision
	select {
	case decision = <-req.reply:
	case <-t.closedCh:
		return false
	}
	if decision == ApprovalAllowAlways {
		t.mu.Lock()
		t.always[name] = true
		t.mu.Unlock()
	}
	return decision != ApprovalDeny
}

// Requests returns the read end of the request channel for the chat
// Model's pump goroutine. The channel closes when Close() is called.
func (t *TUIApprover) Requests() <-chan *ApprovalRequest {
	return t.requests
}

// Reply delivers the user's decision for a specific request. The
// reply channel is buffered so this is non-blocking; calling Reply
// twice on the same request silently drops the second decision
// (defensive — keypress double-fire shouldn't deadlock).
func (t *TUIApprover) Reply(req *ApprovalRequest, decision ApprovalDecision) {
	if req == nil || req.reply == nil {
		return
	}
	select {
	case req.reply <- decision:
	default:
	}
}

// Close marks the approver as shut down. Subsequent ApproveToolCall
// invocations return false immediately without queueing. Already-
// queued requests get drained and denied so their requester
// unblocks instead of hanging on an undelivered prompt.
//
// Idempotent; safe to call from a defer alongside an explicit
// shutdown path.
func (t *TUIApprover) Close() {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	close(t.closedCh)
	t.mu.Unlock()
	// Closing closedCh is enough to unblock any ApproveToolCall
	// stuck on push or reply; this drain is belt-and-suspenders for
	// the corner where a request landed in the channel BEFORE Close
	// and no chat-side pump existed (e.g. headless tests). Each
	// drained request gets a Deny on its reply chan in case the
	// requester is the rare goroutine waiting on reply but not on
	// closedCh (none today, but cheap insurance).
	for {
		select {
		case req := <-t.requests:
			if req != nil && req.reply != nil {
				select {
				case req.reply <- ApprovalDeny:
				default:
				}
			}
		default:
			return
		}
	}
}
