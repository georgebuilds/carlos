package web

import "sync"

// ephemeralHub fans non-persisted wire events (delta, delta_reset,
// approval_request, approval_resolved, children) out to the SSE handlers
// watching a thread. It is the web-side mirror of the event log's
// in-process Subscribe fan-out (F3): non-blocking sends, drop-on-overflow,
// never backpressure into the producing goroutine. A slow browser misses
// a delta and gets resnapshotted on its next reconnect (spec §9.3 step 4,
// §9.4), exactly the log's philosophy.
//
// WebTextSource (W-2) and WebApprover (W-3) publish here; the SSE handler
// subscribes. Persisted events do NOT flow through the hub - those come
// from the event log's own Subscribe so they keep their seq cursor.
type ephemeralHub struct {
	mu   sync.Mutex
	subs map[string]map[chan WireEvent]struct{}
}

func newEphemeralHub() *ephemeralHub {
	return &ephemeralHub{subs: map[string]map[chan WireEvent]struct{}{}}
}

// Publish is the exported entry point for code outside the package (the
// cmd-side interactive backend's WebApprover) to fan an ephemeral event
// out. Internal callers use publish directly.
func (h *ephemeralHub) Publish(ev WireEvent) { h.publish(ev) }

// publish delivers ev to every current subscriber of ev.Thread, dropping
// the event for any subscriber whose buffer is full.
func (h *ephemeralHub) publish(ev WireEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[ev.Thread] {
		select {
		case ch <- ev:
		default:
			// Slow consumer: drop. It resnapshots on reconnect.
		}
	}
}

// subscribe registers a buffered channel for a thread and returns it plus
// an idempotent unsubscribe func that closes the channel.
func (h *ephemeralHub) subscribe(threadID string) (<-chan WireEvent, func()) {
	ch := make(chan WireEvent, 64)
	h.mu.Lock()
	if h.subs[threadID] == nil {
		h.subs[threadID] = map[chan WireEvent]struct{}{}
	}
	h.subs[threadID][ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			if set := h.subs[threadID]; set != nil {
				delete(set, ch)
				if len(set) == 0 {
					delete(h.subs, threadID)
				}
			}
			h.mu.Unlock()
			close(ch)
		})
	}
}
