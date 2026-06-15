package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/georgebuilds/carlos/internal/agent"
)

// cc_drive.go: the interactive half of the Claude Code backend (B-3 drive +
// B-4 approval bridge). EnableDrive turns the observe-only adapter into a
// full one: Attach spawns a `claude` subprocess (cc_driver.go), Send pumps
// turns, and every tool the model wants to run routes through a PreToolUse
// hook back into this process, surfacing as the same browser approval the
// carlos backend uses. The persisted transcript still streams from the
// file-tail; this file owns only the live driving + approval.

// ccPending is a tool-approval request blocked on the human's decision. The
// hook's HTTP handler waits on reply; the browser's POST /approvals resolves
// it; detach denies it.
type ccPending struct {
	thread string
	event  WireEvent   // the approval_request, replayed on SSE reconnect
	reply  chan string // "allow" | "deny"
}

// EnableDrive switches the backend from observe-only to interactive. ctx is
// the server lifetime (per-thread processes derive from it, never from a
// request ctx); hub is the SSE fan-out; baseURL + token let the spawned
// hook call back in; exePath is the carlos binary that backs the hook.
func (b *CCBackend) EnableDrive(ctx context.Context, hub *ephemeralHub, baseURL, token, exePath string) {
	b.lifeCtx = ctx
	b.hub = hub
	b.baseURL = strings.TrimRight(baseURL, "/")
	b.token = token
	b.exePath = exePath
	b.newCwd, _ = os.Getwd() // where a freshly created CC session runs
	b.drivers = map[string]*ccDriver{}
	b.pending = map[string]*ccPending{}
}

func (b *CCBackend) driveEnabled() bool { return b.hub != nil }

// CreateCwd is the working directory a freshly created CC session runs in
// (the carlos-web launch dir), exposed so the create handler can resolve the
// new thread's repo (plan WR-0/WA-1). Empty until EnableDrive has run.
func (b *CCBackend) CreateCwd() string { return b.newCwd }

// caps advertises send+approve only when driving is wired; otherwise the
// observe-only set. Stamped onto every summary so the SPA gates the composer
// and approval UI per backend.
func (b *CCBackend) caps() map[string]bool {
	if b.driveEnabled() {
		return map[string]bool{
			"create": true, "send": true, "approve": true,
			"observe": true, "children": false,
		}
	}
	return ccCaps
}

func (b *CCBackend) Caps() map[string]bool { return b.caps() }

func (b *CCBackend) Attached(id string) bool {
	if !b.driveEnabled() {
		return false
	}
	b.driveMu.Lock()
	defer b.driveMu.Unlock()
	_, ok := b.drivers[id]
	return ok
}

// Frame is carried on the per-thread summary (the project basename), so the
// interface method is unused for CC; return "".
func (b *CCBackend) Frame(string) string { return "" }

// Attach spawns the claude subprocess for a session and starts pumping its
// stream. Idempotent. Observe-only mode (no drive) reports ErrUnsupported so
// the SPA keeps the composer disabled.
func (b *CCBackend) Attach(ctx context.Context, id string) error {
	if !b.driveEnabled() {
		return ErrUnsupported
	}
	b.driveMu.Lock()
	if _, ok := b.drivers[id]; ok {
		b.driveMu.Unlock()
		return nil
	}
	b.driveMu.Unlock()

	path, ok := b.pathFor(id)
	if !ok {
		return fmt.Errorf("cc: no such session %q", id)
	}
	uuid := strings.TrimPrefix(id, ccBackendName+":")
	cwd := ""
	if data, err := os.ReadFile(path); err == nil {
		cwd = ccScanSession(data).Cwd
	}

	drv, err := startCCDriver(b.lifeCtx, id, uuid, cwd, b.hookCommand(id), b.hub.Publish, false)
	if err != nil {
		return fmt.Errorf("cc: start driver: %w", err)
	}
	b.driveMu.Lock()
	b.drivers[id] = drv
	b.driveMu.Unlock()
	return nil
}

// Detach stops the subprocess and denies any approval still blocked on it.
// Idempotent.
func (b *CCBackend) Detach(id string) error {
	if !b.driveEnabled() {
		return nil
	}
	b.driveMu.Lock()
	drv, ok := b.drivers[id]
	delete(b.drivers, id)
	b.driveMu.Unlock()
	if ok {
		drv.stop()
	}
	b.denyPendingForThread(id)
	return nil
}

// Send pumps a user turn to the attached process. CC persists the message to
// its own JSONL (the file-tail surfaces it), so there is no carlos seq to
// return; 0 is fine (the SPA observes the turn on the stream).
func (b *CCBackend) Send(ctx context.Context, id, text string) (int64, error) {
	if !b.driveEnabled() {
		return 0, ErrUnsupported
	}
	b.driveMu.Lock()
	drv, ok := b.drivers[id]
	b.driveMu.Unlock()
	if !ok {
		return 0, fmt.Errorf("cc: thread not attached")
	}
	return 0, drv.send(text)
}

// RequestApproval is invoked by the hook handler when the model wants to run
// a tool. It surfaces an approval_request on the thread's SSE stream and
// blocks until the browser resolves it (or detach denies it), then echoes
// the resolution. Returns "allow" | "deny" for the hook to relay to claude.
func (b *CCBackend) RequestApproval(thread, name string, input json.RawMessage) string {
	reqID := newReqID()
	ev := WireEvent{
		Thread: thread, TS: rfc3339(nowUTC()), Kind: "approval_request",
		Data: map[string]any{"request_id": reqID, "name": name, "input": ccRawInput(input)},
	}
	p := &ccPending{thread: thread, event: ev, reply: make(chan string, 1)}

	b.driveMu.Lock()
	b.pending[reqID] = p
	b.driveMu.Unlock()

	b.hub.Publish(ev)
	decision := <-p.reply // blocks; detach/resolve send here

	b.driveMu.Lock()
	delete(b.pending, reqID)
	b.driveMu.Unlock()

	b.hub.Publish(WireEvent{
		Thread: thread, TS: rfc3339(nowUTC()), Kind: "approval_resolved",
		Data: map[string]any{"request_id": reqID, "decision": decision},
	})
	return decision
}

// Resolve answers a pending approval from the browser (POST /approvals).
// allow_always collapses to allow for CC (the hook cannot express a durable
// "always"; a per-thread allow cache is a future refinement).
func (b *CCBackend) Resolve(thread, requestID, decision string) error {
	if !b.driveEnabled() {
		return ErrUnsupported
	}
	d := decision
	if d == "allow_always" {
		d = "allow"
	}
	if d != "allow" {
		d = "deny"
	}
	b.driveMu.Lock()
	p, ok := b.pending[requestID]
	b.driveMu.Unlock()
	if !ok {
		return fmt.Errorf("cc: unknown or expired approval %q", requestID)
	}
	select {
	case p.reply <- d:
	default:
	}
	return nil
}

func (b *CCBackend) denyPendingForThread(thread string) {
	b.driveMu.Lock()
	defer b.driveMu.Unlock()
	for _, p := range b.pending {
		if p.thread == thread {
			select {
			case p.reply <- "deny":
			default:
			}
		}
	}
}

// LiveText returns the attached driver's in-flight assistant text for the
// SSE reconnect snapshot.
func (b *CCBackend) LiveText(id string) string {
	if !b.driveEnabled() {
		return ""
	}
	b.driveMu.Lock()
	drv, ok := b.drivers[id]
	b.driveMu.Unlock()
	if !ok {
		return ""
	}
	return drv.liveText()
}

// PendingApprovals returns the open approval_request events for a thread, so
// a reconnecting browser re-sees a tool call still waiting on a decision.
func (b *CCBackend) PendingApprovals(id string) []WireEvent {
	if !b.driveEnabled() {
		return nil
	}
	b.driveMu.Lock()
	defer b.driveMu.Unlock()
	var out []WireEvent
	for _, p := range b.pending {
		if p.thread == id {
			out = append(out, p.event)
		}
	}
	return out
}

// CreateThread starts a NEW Claude Code session in the carlos-web launch
// directory: mint a fresh session id, spawn the driver in new-session mode
// (claude --session-id), and auto-attach so it is immediately interactive.
// The on-disk JSONL appears once the first turn runs; the file-tail Subscribe
// (cc.go) handles a file that shows up after attach.
func (b *CCBackend) CreateThread(ctx context.Context, title string) (ThreadSummary, error) {
	if !b.driveEnabled() {
		return ThreadSummary{}, ErrUnsupported
	}
	uuid, err := uuidV4()
	if err != nil {
		return ThreadSummary{}, err
	}
	id := ccBackendName + ":" + uuid
	cwd := b.newCwd

	drv, err := startCCDriver(b.lifeCtx, id, uuid, cwd, b.hookCommand(id), b.hub.Publish, true)
	if err != nil {
		return ThreadSummary{}, fmt.Errorf("cc: start session: %w", err)
	}
	b.driveMu.Lock()
	b.drivers[id] = drv
	b.driveMu.Unlock()

	t := title
	if t == "" {
		t = "new claude code session"
	}
	frame := ""
	if cwd != "" {
		frame = filepath.Base(cwd)
	}
	now := rfc3339(nowUTC())
	return ThreadSummary{
		ID: id, Title: t, Model: "", State: "running",
		Attached: true, CreatedAt: now, UpdatedAt: now,
		Frame: frame, Backend: ccBackendName, Capabilities: b.caps(),
	}, nil
}

// Delete removes the on-disk Claude Code session file (the explicit,
// destructive "delete session" action, distinct from the default hide).
// This DOES remove it from Claude Code too, so the SPA guards it behind a
// confirm. Detaches a running driver first. Returns ErrSessionNotFound when
// no file backs the id.
func (b *CCBackend) Delete(id string) (int, error) {
	path, ok := b.pathFor(id)
	if !ok {
		return 0, agent.ErrSessionNotFound
	}
	_ = b.Detach(id) // stop a driver if we are driving it
	if err := os.Remove(path); err != nil {
		return 0, fmt.Errorf("cc: delete session file: %w", err)
	}
	b.mu.Lock()
	delete(b.index, id)
	delete(b.cache, path)
	b.mu.Unlock()
	return 1, nil
}

// Children stays unsupported: CC sub-agent surfacing is deferred.
func (b *CCBackend) Children(context.Context, string) []ChildSnap { return nil }

// uuidV4 generates a random RFC 4122 v4 UUID for a new CC session id.
func uuidV4() (string, error) {
	var u [16]byte
	if _, err := rand.Read(u[:]); err != nil {
		return "", err
	}
	u[6] = (u[6] & 0x0f) | 0x40 // version 4
	u[8] = (u[8] & 0x3f) | 0x80 // variant 10x
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16]), nil
}

// hookCommand builds the PreToolUse hook command string for a thread: the
// carlos binary in cc-hook mode, told where to call back and which thread it
// is gating. Values are shell-safe (a localhost URL, a base64url token, a
// "cc:<uuid>" id), so no quoting is needed for the claude-run command.
func (b *CCBackend) hookCommand(threadID string) string {
	return fmt.Sprintf("%s cc-hook --url %s/api/cc/hook --token %s --thread %s",
		b.exePath, b.baseURL, b.token, threadID)
}

func newReqID() string {
	var raw [12]byte
	_, _ = rand.Read(raw[:])
	return "ccreq_" + hex.EncodeToString(raw[:])
}

// handleCCHook is the approval intake the spawned hook POSTs to. It blocks
// until the human decides, then returns the decision. Token-gated like every
// /api route; the thread id selects the CC backend.
func (s *Server) handleCCHook(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Thread    string          `json:"thread"`
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Thread == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "thread is required")
		return
	}
	be, ok := s.registry.For(body.Thread)
	cc, isCC := be.(*CCBackend)
	if !ok || !isCC || !cc.driveEnabled() {
		writeErr(w, http.StatusNotFound, "unknown_backend", "not a drivable cc thread")
		return
	}
	decision := cc.RequestApproval(body.Thread, body.ToolName, body.ToolInput)
	writeJSON(w, http.StatusOK, map[string]any{"decision": decision})
}
