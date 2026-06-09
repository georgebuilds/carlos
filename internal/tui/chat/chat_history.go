package chat

import "strings"

// Chat-input history: terminal-style ↑/↓ recall of previously
// submitted user messages, mirroring the bash / zsh / fish / Claude
// Code / aider / OpenCode / Crush convention so muscle memory carries
// across coding-agent TUIs. The composer already wires ↑/↓ for two
// other history-shaped affordances — `!`-prefixed shell history
// (Phase U S7, see chat.go around the `case "up"` block) and the
// slash autocomplete suggest list (handleSlashSuggestKey). Both keep
// their priority; chat history fires only when neither is active and
// the textarea is single-line, so multi-line cursor nav still works.
//
// Source of truth is the in-memory transcript (entryUserMessage
// entries). That gives us free persistence across resume — the
// transcript hydrates from the event log on backfill — without
// introducing a second history store. Side effect: messages from
// other agents in a frame switch are NOT inherited; each session
// walks its own transcript.

// chatHistoryReset returns the cursor to the "not walking" state and
// drops the saved draft. Call after submit() so the next ↑ restarts
// from the most recent entry, and on any composer state change that
// would make the walk feel stale (frame switch, /clear).
func (m *Model) chatHistoryReset() {
	m.chatHistoryCursor = -1
	m.chatHistoryDraft = ""
}

// chatHistoryEntries walks the transcript newest-first and returns
// the texts of every user message, with consecutive duplicates
// collapsed (the "I sent the same thing twice in a row" case is a
// fat-fingered Enter, not two distinct intents). Returned slice is
// fresh; callers are free to keep it.
func (m *Model) chatHistoryEntries() []string {
	out := make([]string, 0, len(m.transcript))
	prev := ""
	for i := len(m.transcript) - 1; i >= 0; i-- {
		e := m.transcript[i]
		if e.kind != entryUserMessage {
			continue
		}
		text := e.text
		if text == "" || text == prev {
			continue
		}
		out = append(out, text)
		prev = text
	}
	return out
}

// chatHistoryUp walks one step toward older messages. First press
// stashes the current textarea value as the draft so a later ↓ past
// the most-recent entry can restore it. Past the oldest entry the
// call is a no-op (stays pinned to the oldest). Returns true when the
// textarea actually changed so the caller can decide whether to skip
// the textarea's native cursor handling.
func (m *Model) chatHistoryUp() bool {
	entries := m.chatHistoryEntries()
	if len(entries) == 0 {
		return false
	}
	if m.chatHistoryCursor == -1 {
		m.chatHistoryDraft = m.ta.Value()
	}
	next := m.chatHistoryCursor + 1
	if next >= len(entries) {
		next = len(entries) - 1
	}
	if next == m.chatHistoryCursor {
		return false
	}
	m.chatHistoryCursor = next
	m.ta.SetValue(entries[next])
	m.ta.CursorEnd()
	return true
}

// chatHistoryDown walks one step toward newer messages. Stepping past
// the most-recent entry restores the stashed draft (or empties the
// textarea if there was none) and exits the walk so future ↑ starts
// fresh. Returns true when the textarea actually changed.
func (m *Model) chatHistoryDown() bool {
	if m.chatHistoryCursor == -1 {
		return false
	}
	if m.chatHistoryCursor == 0 {
		m.ta.SetValue(m.chatHistoryDraft)
		m.ta.CursorEnd()
		m.chatHistoryReset()
		return true
	}
	entries := m.chatHistoryEntries()
	m.chatHistoryCursor--
	// Guard against the transcript shrinking under us (e.g. /clear
	// mid-walk). If the cursor is past the slice tail, restore the
	// draft instead of indexing out of bounds.
	if m.chatHistoryCursor >= len(entries) {
		m.ta.SetValue(m.chatHistoryDraft)
		m.ta.CursorEnd()
		m.chatHistoryReset()
		return true
	}
	m.ta.SetValue(entries[m.chatHistoryCursor])
	m.ta.CursorEnd()
	return true
}

// chatHistoryShouldEngage decides whether ↑/↓ should walk chat
// history versus letting the textarea's native cursor nav win. The
// rule mirrors zsh's autosuggest behavior: empty composer or a
// single-line buffer routes to history; a multi-line buffer means
// the user is mid-compose and arrow keys belong to the cursor.
// Slash autocomplete is also routed earlier when its match list has
// multiple entries (handled in chat.go's up/down handlers).
//
// Shell-prefixed composers (the `!` mode) also bail here, in addition
// to the dedicated shell-history branch in chat.go. Without this
// belt-and-suspenders the `m.shellHistory != nil` guard would let a
// shell-prefixed line fall through and get clobbered with a recalled
// chat message — wrong even in dev-aid builds where the shell-history
// file isn't wired.
func (m *Model) chatHistoryShouldEngage() bool {
	if m.readOnly {
		return false
	}
	val := m.ta.Value()
	if strings.Contains(val, "\n") {
		return false
	}
	if hasShellPrefix(val) {
		return false
	}
	return true
}
