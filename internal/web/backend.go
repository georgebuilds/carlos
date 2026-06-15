package web

import (
	"context"
	"errors"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// ErrUnsupported is returned by the read-only backend for interactive
// operations (attach, send, approve, create). The HTTP layer maps it to
// 501 Not Implemented so a browser running against a read-only server
// gets an honest answer rather than a silent no-op.
var ErrUnsupported = errors.New("web: interactive backend not wired (read-only mode)")

// ErrThreadOwned is returned when an interactive attach is refused because
// another process is heartbeating the thread (the single-owner invariant,
// spec §11.1). The HTTP layer maps it to 409 thread_owned.
var ErrThreadOwned = errors.New("web: thread owned by another process")

// ChildSnap is the per-thread sub-agent roster row (spec §8 children
// kind / F11). Mirrors agent.ChildSnapshot but stays a web-owned type so
// the wire contract never leaks an internal struct.
type ChildSnap struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	Title     string `json:"title"`
	LastTool  string `json:"last_tool"`
	Tokens    int    `json:"tokens"`
	CostCents int    `json:"cost_cents"`
	StartedAt string `json:"started_at"`
}

// Backend is the per-runtime seam (spec §12 ThreadBackend, adapted to the
// in-process v1 shape). It owns BOTH the read surface (Name/ListThreads/
// GetThread/ReadEvents/Subscribe) and the interactive, side-effectful
// operations, so a non-carlos backend with no SQLiteEventLog (Claude Code,
// opencode) can be added without the HTTP handlers special-casing carlos.
// The group overlay stays in the handler (web-roster metadata, spans
// backends). cmd/carlos provides the real carlos implementation over the
// runtime; the read-only server and tests embed CarlosReader for the read
// half and stub the interactive half.
type Backend interface {
	// Name is the wire backend tag (spec §8.2 backend field), e.g.
	// "carlos". It replaces the value formerly hardcoded in the handler.
	Name() string

	// Caps advertises which operations this backend supports, so the UI
	// can gate buttons (spec §8.2 capabilities, §12 BackendCaps).
	Caps() map[string]bool

	// --- Read surface (spec §12). Owned by the backend so a non-carlos
	// backend with no SQLiteEventLog can implement it over its own source.
	// carlos's implementation (CarlosReader) wraps the event log. ---

	// ListThreads returns the roster projection (conversations only, with
	// the per-thread attachment overlay). The group overlay is applied by
	// the handler, not here.
	ListThreads(ctx context.Context) ([]ThreadSummary, error)
	// GetThread returns one thread's summary, ok=false when absent.
	GetThread(ctx context.Context, id string) (ThreadSummary, bool, error)
	// ReadEvents returns persisted wire events with seq in (from, upTo);
	// upTo==0 means no upper bound. Used by the events endpoint and the SSE
	// backfill / gap-repair window.
	ReadEvents(ctx context.Context, id string, from, upTo int64) ([]WireEvent, error)
	// Subscribe returns a live wire-event stream plus an idempotent
	// unsubscribe (spec §9.3). Persisted events carry Seq for the
	// reconnect cursor; ephemeral kinds flow through the hub, not here.
	Subscribe(id string) (<-chan WireEvent, func(), error)

	// Attached reports whether this process is interactively driving the
	// thread (its chatglue.Loop is running here).
	Attached(threadID string) bool

	// Frame returns the frame resolved for an attached thread, or "" when
	// detached (spec §11.4).
	Frame(threadID string) string

	// Attach starts the interactive loop + heartbeat for the thread.
	// Idempotent (re-attach of an owned thread is a no-op). Returns
	// ErrThreadOwned when a fresh foreign heartbeat holds the thread.
	Attach(ctx context.Context, threadID string) error

	// Detach stops the loop + heartbeat. Idempotent.
	Detach(threadID string) error

	// Send appends an EvtUserMessage and returns its seq. Requires the
	// thread to be attached (spec §9.2).
	Send(ctx context.Context, threadID, text string) (int64, error)

	// Resolve answers a pending tool-approval request (spec §10).
	// decision is "deny" | "allow" | "allow_always".
	Resolve(threadID, requestID, decision string) error

	// CreateThread mints + ensures a new thread and returns its summary
	// (already wire-shaped, attachment overlay stamped). Returning a
	// ThreadSummary rather than an agent.Session keeps the handler free of
	// carlos-internal types so a foreign backend can implement create.
	CreateThread(ctx context.Context, title string) (ThreadSummary, error)

	// Delete permanently removes a thread and its sub-agent lineage,
	// returning the number of agent rows deleted. It detaches the thread
	// first if this process is driving it. Surfaces agent.ErrSessionLive
	// when another live process holds the thread (the caller maps it to
	// 409) and agent.ErrSessionNotFound for an unknown id.
	Delete(threadID string) (int, error)

	// Children returns the live sub-agent snapshot for the thread (F11).
	Children(ctx context.Context, threadID string) []ChildSnap

	// LiveText returns the in-flight assistant delta buffer, for the SSE
	// reconnect snapshot (spec §9.3 step 4).
	LiveText(threadID string) string

	// PendingApprovals returns the currently-open approval_request wire
	// events for the SSE reconnect snapshot (spec §9.3 step 4).
	PendingApprovals(threadID string) []WireEvent
}

// readOnlyCaps is the capability set advertised when no interactive
// backend is wired: the detached read path works fully, every interactive
// operation reports ErrUnsupported.
var readOnlyCaps = map[string]bool{
	"create": false, "send": false, "approve": false,
	"observe": true, "children": false,
}

// readOnlyBackend is the default when no interactive backend is wired: the
// read surface works fully (list, events, SSE replay of persisted events,
// groups) via the embedded CarlosReader, while every interactive operation
// reports ErrUnsupported. The runtime-backed carlosBackend (cmd/carlos)
// embeds the same reader with a live attachment oracle. Heartbeat-based
// foreign-owner detection is done by the interactive backend, not here.
type readOnlyBackend struct {
	*CarlosReader
}

// newReadOnlyBackend builds the default backend over the event log with a
// constant (false, "") attachment oracle: nothing is attached in read-only
// mode, so every thread reads detached with an empty frame.
func newReadOnlyBackend(log *agent.SQLiteEventLog) readOnlyBackend {
	return readOnlyBackend{
		CarlosReader: NewCarlosReader(log, "carlos", readOnlyCaps,
			func(string) (bool, string) { return false, "" }),
	}
}

func (readOnlyBackend) Caps() map[string]bool                { return readOnlyCaps }
func (readOnlyBackend) Attached(string) bool                 { return false }
func (readOnlyBackend) Frame(string) string                  { return "" }
func (readOnlyBackend) Attach(context.Context, string) error { return ErrUnsupported }
func (readOnlyBackend) Detach(string) error                  { return ErrUnsupported }
func (readOnlyBackend) Send(context.Context, string, string) (int64, error) {
	return 0, ErrUnsupported
}
func (readOnlyBackend) Resolve(string, string, string) error { return ErrUnsupported }
func (readOnlyBackend) CreateThread(context.Context, string) (ThreadSummary, error) {
	return ThreadSummary{}, ErrUnsupported
}
func (readOnlyBackend) Delete(string) (int, error)                   { return 0, ErrUnsupported }
func (readOnlyBackend) Children(context.Context, string) []ChildSnap { return nil }
func (readOnlyBackend) LiveText(string) string                       { return "" }
func (readOnlyBackend) PendingApprovals(string) []WireEvent          { return nil }

// ChildrenEvent builds a `children` wire event from a backend snapshot.
// Exported so the cmd-side interactive backend can publish live crew
// updates through the hub (Server.Hub().Publish) when the supervisor
// reports a child lifecycle edge; the SSE reconnect snapshot uses the
// same constructor so both paths emit an identical shape.
func ChildrenEvent(threadID string, kids []ChildSnap, now time.Time) WireEvent {
	if kids == nil {
		kids = []ChildSnap{}
	}
	return WireEvent{
		Thread: threadID,
		TS:     rfc3339(now),
		Kind:   "children",
		Data:   map[string]any{"children": kids},
	}
}
