// Package chat hosts the single-agent conversational TUI surface.
//
// Slice 1e ships this in three layers:
//
//  1. A read-only viewer that replays an agent's event log into a
//     transcript and subscribes for live updates.
//  2. A textarea input line that, on submit, appends a `user_message`
//     event to the log. The actual provider dispatch is Slice 1f's
//     responsibility; we only own the read + write of events.
//  3. A slash-command pre-check that intercepts `/...` input before it
//     ever becomes a model-bound message.
//
// The architectural commitment drives every choice in here: the event
// log is the source of truth. The chat view is a
// projection; it never owns state, and it never writes anything that
// isn't an event row plus an in-memory render of those events.
package chat

import "sync"

// TextSource is the seam Slice 1f will plug into. Per the event-log
// write discipline ("do not write a row per token; coalesce in
// memory"), streaming assistant text deltas are NOT persisted to the
// event log per-token. The chat view still needs to render the live assistant
// text somehow, so this interface gives Slice 1f a single contact point
// to publish coalesced deltas into.
//
// Contract:
//
//   - Get(agentID) returns the currently accumulated assistant text for
//     that agent, as a single string. Implementations MUST be safe to
//     call concurrently with Append / Reset.
//   - Append(agentID, delta) tacks delta onto the buffer.
//   - Reset(agentID) clears the buffer (called at turn boundaries — when
//     the assistant's reply is "sealed" into the event log via whatever
//     compacted form Slice 1f settles on, the live buffer is dropped so
//     the next turn starts fresh).
//
// The chat view polls Get via a Subscribe-style channel? No — Slice 1f
// will be the publisher, and it sends a `TextUpdatedMsg` (defined in
// messages.go) into the bubbletea program. The chat view's Update reads
// the latest Get on every render. Pull-on-render keeps the wire surface
// small; we don't try to model a streaming subscription here.
type TextSource interface {
	Get(agentID string) string
	Append(agentID, delta string)
	Reset(agentID string)
}

// MemTextSource is the in-memory implementation used by tests and by
// the development `carlos chat` subcommand. Production usage (real
// provider stream) plugs the same interface in Slice 1f.
//
// Concurrency: a single sync.RWMutex guards the map; reads are common
// (every render frame), writes are streaming-cadence (~250ms or token).
type MemTextSource struct {
	mu   sync.RWMutex
	bufs map[string]string
}

// NewMemTextSource returns an empty in-memory TextSource.
func NewMemTextSource() *MemTextSource {
	return &MemTextSource{bufs: map[string]string{}}
}

// Get returns the accumulated text for agentID, or "" if there is none.
func (m *MemTextSource) Get(agentID string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.bufs[agentID]
}

// Append concatenates delta to the buffer for agentID.
func (m *MemTextSource) Append(agentID, delta string) {
	if delta == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bufs[agentID] += delta
}

// Reset clears the buffer for agentID. Idempotent.
func (m *MemTextSource) Reset(agentID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.bufs, agentID)
}

// Compile-time check.
var _ TextSource = (*MemTextSource)(nil)
