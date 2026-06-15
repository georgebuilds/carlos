package web

import (
	"context"
	"sync"

	"github.com/georgebuilds/carlos/internal/agent"
)

// AttachOracle answers, for a thread id, whether THIS process is
// interactively driving it and (if so) the frame it resolved at attach.
// It is injected into CarlosReader so the read path can stamp the
// per-thread Attached/Frame overlay without the reader knowing about the
// interactive backend that owns it. The read-only default injects a
// constant (false, ""); the cmd-side carlosBackend injects a closure over
// its own attach table. This indirection is what lets both the read-only
// server and the interactive backend share one reader without the Go
// embedding virtual-dispatch trap (a reader method cannot call an
// embedder's override, but it CAN call an injected func).
type AttachOracle func(id string) (attached bool, frame string)

// CarlosReader owns the carlos read path: list, get, read events, and live
// subscribe, projected into the normalized wire vocabulary. It is the half
// of a carlos Backend that needs only the event log (no supervisor, no
// loop), so the read-only `carlos web` and the full interactive backend
// embed the same value. Multi-backend (Claude Code, opencode) implements
// the same Backend read methods over its own source; nothing here is
// special-cased in the HTTP handlers.
type CarlosReader struct {
	log    *agent.SQLiteEventLog
	name   string
	caps   map[string]bool
	oracle AttachOracle
}

// NewCarlosReader builds the reader. name is the wire backend tag
// ("carlos"); caps is the capability map stamped onto every summary this
// reader produces (the owning backend's caps, shared across its threads in
// v1); oracle supplies the per-thread attachment overlay (pass a constant
// (false,"") for read-only mode).
func NewCarlosReader(log *agent.SQLiteEventLog, name string, caps map[string]bool, oracle AttachOracle) *CarlosReader {
	return &CarlosReader{log: log, name: name, caps: caps, oracle: oracle}
}

// Name is the wire backend tag (spec §8.2 backend field).
func (r *CarlosReader) Name() string { return r.name }

// summary projects an agent.Session into a ThreadSummary, stamping the
// attachment overlay from the oracle and the backend tag + caps. GroupID
// is left nil: the group overlay is web-roster metadata applied by the
// handler (it spans backends), never a backend concern.
func (r *CarlosReader) summary(sess agent.Session) ThreadSummary {
	attached, frame := false, ""
	if r.oracle != nil {
		attached, frame = r.oracle(sess.ID)
	}
	return ThreadSummary{
		ID:           sess.ID,
		Title:        sess.Title,
		Model:        sess.Model,
		State:        wireState(sess.State),
		Attached:     attached,
		CreatedAt:    rfc3339(sess.CreatedAt),
		UpdatedAt:    rfc3339(sess.UpdatedAt),
		Preview:      sess.Preview,
		UserMsgs:     sess.UserMsgs,
		Frame:        frame,
		Backend:      r.name,
		Capabilities: r.caps,
	}
}

// ListThreads returns the roster: every top-level conversation, with the
// per-thread attachment overlay. The conversations-only filter (carried
// verbatim from the old handler) shows a thread when it is a real
// conversation (>=1 user message) OR a still-live blank one, hiding
// content-bearing spawns (research roots, headless runs) and terminal
// empties. UserMsgs>0 short-circuits, so the content query only runs for
// the rare zero-message non-terminal thread.
func (r *CarlosReader) ListThreads(ctx context.Context) ([]ThreadSummary, error) {
	sessions, err := agent.ListUserSessions(ctx, r.log, "")
	if err != nil {
		return nil, err
	}
	out := make([]ThreadSummary, 0, len(sessions))
	for _, sess := range sessions {
		keep := sess.UserMsgs > 0 ||
			(!sess.State.IsTerminal() && !r.hasContentEvents(ctx, sess.ID))
		if !keep {
			continue
		}
		out = append(out, r.summary(sess))
	}
	return out, nil
}

// GetThread returns one thread's summary, or ok=false when absent. Unlike
// ListThreads it does NOT apply the conversations-only filter (the old
// handler returned any matching top-level row); the caller overlays group
// membership.
func (r *CarlosReader) GetThread(ctx context.Context, id string) (ThreadSummary, bool, error) {
	sessions, err := agent.ListUserSessions(ctx, r.log, "")
	if err != nil {
		return ThreadSummary{}, false, err
	}
	for _, sess := range sessions {
		if sess.ID == id {
			return r.summary(sess), true, nil
		}
	}
	return ThreadSummary{}, false, nil
}

// ReadEvents returns persisted wire events with seq in (from, upTo), or
// (from, end] when upTo==0. Non-forwarded event types (eventToWire ok=false)
// are dropped. Serves both the events REST endpoint (upTo==0, the handler
// then applies the limit) and the SSE backfill / gap-repair window.
func (r *CarlosReader) ReadEvents(ctx context.Context, id string, from, upTo int64) ([]WireEvent, error) {
	events, err := r.log.Read(ctx, id, from)
	if err != nil {
		return nil, err
	}
	out := make([]WireEvent, 0, len(events))
	for _, ev := range events {
		if upTo > 0 && ev.Seq >= upTo {
			break
		}
		if we, ok := eventToWire(ev); ok {
			out = append(out, we)
		}
	}
	return out, nil
}

// Subscribe returns a live stream of normalized wire events plus an
// idempotent unsubscribe. It wraps log.Subscribe (the in-process, cap-64,
// drop-on-overflow fan-out, F3) with one translation goroutine that maps
// each agent.Event to its WireEvent and forwards only forwardable kinds.
// The cap-64 drop semantics live upstream on the agent.Event channel and
// are unchanged; the gap-repair the SSE handler performs keys on
// WireEvent.Seq, which every persisted event carries.
func (r *CarlosReader) Subscribe(id string) (<-chan WireEvent, func(), error) {
	logCh, logUnsub, err := r.log.Subscribe(id)
	if err != nil {
		return nil, nil, err
	}
	out := make(chan WireEvent, 64)
	done := make(chan struct{})
	go func() {
		defer close(out)
		for {
			select {
			case <-done:
				return
			case ev, ok := <-logCh:
				if !ok {
					return
				}
				we, ok := eventToWire(ev)
				if !ok {
					continue
				}
				select {
				case out <- we:
				case <-done:
					return
				}
			}
		}
	}()
	var once sync.Once
	unsub := func() {
		once.Do(func() {
			close(done)
			logUnsub()
		})
	}
	return out, unsub, nil
}

// hasContentEvents reports whether the thread has produced any assistant,
// tool, or research events: the signature of a programmatic run (research
// root, headless `please`, scheduled job) as opposed to a blank, freshly
// created conversation that carries only lifecycle bookkeeping. Used by the
// conversations-only filter to keep blank conversations visible while
// hiding spawned roots. Best-effort: a query error returns false (show the
// thread) rather than hiding it.
func (r *CarlosReader) hasContentEvents(ctx context.Context, agentID string) bool {
	var exists int
	err := r.log.DB().QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM events
			 WHERE agent_id = ?
			   AND type IN ('assistant_message', 'tool_call', 'tool_result', 'research_phase')
			 LIMIT 1)`, agentID).Scan(&exists)
	if err != nil {
		return false
	}
	return exists == 1
}
