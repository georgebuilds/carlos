package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// sseHeartbeat is the idle keep-alive cadence (spec §9.3 step 6): a
// comment line every ~25s so idle proxies/timeouts don't drop the stream.
const sseHeartbeat = 25 * time.Second

// handleStream: GET /api/threads/{id}/stream?from=<seq>&token=…
//
// Protocol (spec §9.3):
//  1. Subscribe first (log + ephemeral hub), buffering arrivals.
//  2. Backfill via Read(from), emitting persisted events with id:<seq>.
//  3. Splice: drain the buffered persisted events, dropping seq <= last
//     backfilled (dedupe), then go live.
//  4. Snapshot ephemerals: current live delta, any pending approvals, a
//     children snapshot.
//  5. Gap repair: on a persisted seq jump > 1, re-Read the gap in order.
//  6. Heartbeat comment every ~25s.
//
// Reconnect rides EventSource's native Last-Event-ID: the browser resends
// the last persisted seq, which we read as `from`. Ephemeral kinds carry
// no id, so they never corrupt the cursor (step 4 reconstructs them).
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "no_flush", "streaming unsupported")
		return
	}
	id := r.PathValue("id")

	// `from` comes from the query, but a reconnecting EventSource carries
	// Last-Event-ID (the last persisted seq) which takes precedence.
	from := parseInt(r.URL.Query().Get("from"), 0)
	if lid := r.Header.Get("Last-Event-ID"); lid != "" {
		from = parseInt(lid, from)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // hint to disable proxy buffering
	w.WriteHeader(http.StatusOK)

	ctx := r.Context()

	// 1. Subscribe BEFORE backfill so nothing appended during backfill is
	//    lost (it lands in the buffer and the splice dedupes it).
	logCh, logUnsub, err := s.log.Subscribe(id)
	if err != nil {
		// Can't subscribe: degrade to a one-shot backfill so the client
		// at least sees the transcript.
		s.backfill(ctx, w, flusher, id, from, 0)
		return
	}
	defer logUnsub()
	ephCh, ephUnsub := s.hub.subscribe(id)
	defer ephUnsub()

	// 2. Backfill.
	last := s.backfill(ctx, w, flusher, id, from, 0)

	// 3 + 4. Splice happens implicitly in the live loop: we drop any
	//    buffered persisted event with seq <= last. First emit the
	//    ephemeral snapshot so a mid-turn reconnect shows in-flight state.
	s.emitEphemeralSnapshot(ctx, w, flusher, id)

	// 5 + 6. Live.
	ticker := time.NewTicker(sseHeartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case ev, ok := <-ephCh:
			if !ok {
				ephCh = nil
				continue
			}
			writeSSE(w, ev)
			flusher.Flush()
		case ev, ok := <-logCh:
			if !ok {
				logCh = nil
				continue
			}
			// Splice dedupe: skip anything already backfilled.
			if ev.Seq <= last {
				continue
			}
			// Gap repair: a jump means the 64-deep channel dropped
			// events. Re-Read the gap and emit in order (F3).
			if last > 0 && ev.Seq > last+1 {
				last = s.backfill(ctx, w, flusher, id, last, ev.Seq)
			}
			if we, ok := eventToWire(ev); ok {
				writeSSE(w, we)
				flusher.Flush()
			}
			if ev.Seq > last {
				last = ev.Seq
			}
		}
	}
}

// backfill reads persisted events in (from, upTo] (upTo==0 means no upper
// bound) and writes them as SSE frames. Returns the highest seq emitted
// (or `from` if none).
func (s *Server) backfill(ctx context.Context, w http.ResponseWriter, f http.Flusher, id string, from, upTo int64) int64 {
	events, err := s.log.Read(ctx, id, from)
	if err != nil {
		return from
	}
	last := from
	for _, ev := range events {
		if upTo > 0 && ev.Seq >= upTo {
			break
		}
		if we, ok := eventToWire(ev); ok {
			writeSSE(w, we)
		}
		if ev.Seq > last {
			last = ev.Seq
		}
	}
	f.Flush()
	return last
}

// emitEphemeralSnapshot reconstructs the non-persisted state on (re)connect
// (spec §9.3 step 4): the in-flight delta buffer, any pending approval
// requests, and a children snapshot. Ephemeral frames carry no id.
func (s *Server) emitEphemeralSnapshot(ctx context.Context, w http.ResponseWriter, f http.Flusher, id string) {
	if txt := s.backend.LiveText(id); txt != "" {
		writeSSE(w, WireEvent{Thread: id, TS: rfc3339(nowUTC()), Kind: "delta", Data: map[string]any{"text": txt}})
	}
	for _, ap := range s.backend.PendingApprovals(id) {
		writeSSE(w, ap)
	}
	if kids := s.backend.Children(ctx, id); len(kids) > 0 {
		writeSSE(w, ChildrenEvent(id, kids, nowUTC()))
	}
	f.Flush()
}

// writeSSE serializes a wire event as an SSE frame. Persisted events
// (seq > 0) carry id:<seq> so EventSource resume works; ephemeral events
// omit it so they never advance the client's Last-Event-ID cursor.
func writeSSE(w http.ResponseWriter, ev WireEvent) {
	if ev.Seq > 0 {
		fmt.Fprintf(w, "id: %d\n", ev.Seq)
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", b)
}

// nowUTC is a tiny indirection so tests stay deterministic if needed; it
// is the only clock read in the SSE path.
func nowUTC() time.Time { return time.Now().UTC() }
