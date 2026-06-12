package web

import "sync"

// WebTextSource implements chatglue's TextSource seam (Append/Reset,
// structural) plus Get and a fan-out of live assistant deltas to the SSE
// hub. It is the web mirror of chat.MemTextSource: a per-thread delta
// buffer the reconnect snapshot reads via Get (spec §9.3 step 4), and a
// live fan-out so attached browsers see tokens as they stream (F4, §9.4).
//
// Deltas never touch the event log (F4); only the sealed assistant turn
// lands as EvtAssistantMessage. Slow SSE clients drop-and-resnapshot in
// the hub, never backpressuring the loop.
type WebTextSource struct {
	hub *ephemeralHub
	mu  sync.Mutex
	buf map[string]string
}

// NewWebTextSource returns a TextSource that fans out to hub.
func NewWebTextSource(hub *ephemeralHub) *WebTextSource {
	return &WebTextSource{hub: hub, buf: map[string]string{}}
}

// Append accumulates a delta for the thread and fans it out live.
func (s *WebTextSource) Append(agentID, delta string) {
	if delta == "" {
		return
	}
	s.mu.Lock()
	s.buf[agentID] += delta
	s.mu.Unlock()
	s.hub.publish(WireEvent{
		Thread: agentID,
		TS:     rfc3339(nowUTC()),
		Kind:   "delta",
		Data:   map[string]any{"text": delta},
	})
}

// Reset clears the thread's buffer (the turn sealed) and tells clients to
// drop their live buffer; the transcript already carries the
// assistant_message.
func (s *WebTextSource) Reset(agentID string) {
	s.mu.Lock()
	delete(s.buf, agentID)
	s.mu.Unlock()
	s.hub.publish(WireEvent{
		Thread: agentID,
		TS:     rfc3339(nowUTC()),
		Kind:   "delta_reset",
		Data:   map[string]any{},
	})
}

// Get returns the in-flight delta buffer for the thread (the reconnect
// snapshot source).
func (s *WebTextSource) Get(agentID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf[agentID]
}
