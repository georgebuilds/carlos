package chat

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// newHistoryModel wires a chat Model whose transcript is pre-seeded
// with a sequence of user-message turns, so the ↑/↓ history tests can
// drive arrow keys against a known walk-list without going through
// the full event-log round-trip.
func newHistoryModel(t *testing.T, msgs ...string) *Model {
	t.Helper()
	log := openTempLog(t)
	const agentID = "01HV0000000000000000HIST00"
	seedAgent(t, log, agentID, "history", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)
	for _, text := range msgs {
		m.transcript = append(m.transcript, transcriptEntry{
			kind: entryUserMessage,
			ts:   time.Now().UTC(),
			text: text,
		})
	}
	return m
}

// TestChatHistory_UpRecallsMostRecent pins the basic recall: a fresh
// composer with one prior user message in the transcript, ↑ loads
// that message into the textarea and parks the cursor at end-of-line.
func TestChatHistory_UpRecallsMostRecent(t *testing.T) {
	m := newHistoryModel(t, "hello world")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*Model)
	if cmd != nil {
		t.Errorf("↑ should not produce a Cmd; got %T", cmd)
	}
	if v := m.ta.Value(); v != "hello world" {
		t.Errorf("textarea value = %q, want %q", v, "hello world")
	}
	if m.chatHistoryCursor != 0 {
		t.Errorf("cursor = %d, want 0 (most-recent)", m.chatHistoryCursor)
	}
}

// TestChatHistory_UpWalksOlder verifies repeated ↑ walks back through
// the entire user-message history, newest-first, and pins at the
// oldest entry (no further movement) instead of wrapping.
func TestChatHistory_UpWalksOlder(t *testing.T) {
	m := newHistoryModel(t, "oldest", "middle", "newest")

	// ↑ once → "newest"
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*Model)
	if v := m.ta.Value(); v != "newest" {
		t.Errorf("after first ↑: textarea = %q, want %q", v, "newest")
	}
	// ↑ twice → "middle"
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*Model)
	if v := m.ta.Value(); v != "middle" {
		t.Errorf("after second ↑: textarea = %q, want %q", v, "middle")
	}
	// ↑ three times → "oldest"
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*Model)
	if v := m.ta.Value(); v != "oldest" {
		t.Errorf("after third ↑: textarea = %q, want %q", v, "oldest")
	}
	// ↑ four times → still "oldest" (pinned, no wrap)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*Model)
	if v := m.ta.Value(); v != "oldest" {
		t.Errorf("after fourth ↑: textarea = %q, want %q (pinned to oldest)", v, "oldest")
	}
}

// TestChatHistory_DownReturnsToEmptyDraft walks ↑ to recall a message
// then ↓ past the most-recent entry to restore the empty draft and
// exit the walk. After exit the cursor is back to -1 so the next ↑
// starts fresh.
func TestChatHistory_DownReturnsToEmptyDraft(t *testing.T) {
	m := newHistoryModel(t, "hello")

	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if v := m.ta.Value(); v != "hello" {
		t.Fatalf("setup: ↑ should have loaded %q, got %q", "hello", v)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(*Model)
	if v := m.ta.Value(); v != "" {
		t.Errorf("after ↓: textarea = %q, want empty (draft restored)", v)
	}
	if m.chatHistoryCursor != -1 {
		t.Errorf("cursor = %d, want -1 (walk exited)", m.chatHistoryCursor)
	}
}

// TestChatHistory_PreservesTypedDraft is the regression test for the
// "I started typing a new message, hit ↑ by accident, want my draft
// back" workflow. ↓ past most-recent must restore the exact text the
// user had in the composer when they first pressed ↑.
func TestChatHistory_PreservesTypedDraft(t *testing.T) {
	m := newHistoryModel(t, "old message")
	m.ta.SetValue("in progress draft")

	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if v := m.ta.Value(); v != "old message" {
		t.Fatalf("setup: ↑ should have loaded %q, got %q", "old message", v)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(*Model)
	if v := m.ta.Value(); v != "in progress draft" {
		t.Errorf("after ↓: textarea = %q, want %q (draft restored)", v, "in progress draft")
	}
}

// TestChatHistory_NoOpOnEmptyTranscript guards the cold-start path:
// fresh chat with zero prior user messages, ↑ must do nothing
// (textarea stays empty, cursor stays -1, no Cmd).
func TestChatHistory_NoOpOnEmptyTranscript(t *testing.T) {
	m := newHistoryModel(t /* no messages */)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*Model)
	if v := m.ta.Value(); v != "" {
		t.Errorf("↑ on empty history should be a no-op; textarea = %q", v)
	}
	if m.chatHistoryCursor != -1 {
		t.Errorf("cursor = %d, want -1 (no walk started)", m.chatHistoryCursor)
	}
}

// TestChatHistory_DedupsConsecutiveDuplicates verifies that the same
// message submitted back-to-back collapses to a single walk entry,
// since the duplicate is almost always an accidental double-Enter
// rather than two distinct intents.
func TestChatHistory_DedupsConsecutiveDuplicates(t *testing.T) {
	m := newHistoryModel(t, "first", "second", "second", "second", "third")

	walks := []string{"third", "second", "first"}
	for i, want := range walks {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
		m = updated.(*Model)
		if got := m.ta.Value(); got != want {
			t.Errorf("walk step %d: got %q, want %q (consecutive dups should collapse)", i, got, want)
		}
	}
}

// TestChatHistory_SubmitResetsCursor checks that after the user
// submits a recalled message, the next ↑ restarts from the freshly-
// appended entry rather than continuing the previous walk mid-way.
func TestChatHistory_SubmitResetsCursor(t *testing.T) {
	m := newHistoryModel(t, "old1", "old2")
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.Update(tea.KeyMsg{Type: tea.KeyUp}) // now on "old1"

	if m.chatHistoryCursor != 1 {
		t.Fatalf("setup: expected cursor=1 after two ↑; got %d", m.chatHistoryCursor)
	}
	// Submit the recalled message.
	m.ta.SetValue("just-submitted")
	if cmd := m.submit(); cmd != nil {
		// drain side-effect; the log write is unrelated to this assertion.
		_ = cmd()
	}
	if m.chatHistoryCursor != -1 {
		t.Errorf("submit should reset cursor to -1; got %d", m.chatHistoryCursor)
	}
}

// TestChatHistory_MultiLineLetsTextareaWin verifies the cursor-nav
// guarantee: when the composer holds multi-line input, ↑/↓ belong to
// the textarea (cursor moves between lines) and chat history is
// NOT engaged. Otherwise users mid-composing a multi-line message
// would lose their work the moment they pressed ↑.
func TestChatHistory_MultiLineLetsTextareaWin(t *testing.T) {
	m := newHistoryModel(t, "previous")
	m.ta.SetValue("line1\nline2\nline3")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*Model)
	if v := m.ta.Value(); v != "line1\nline2\nline3" {
		t.Errorf("↑ on multi-line composer should not engage history; textarea = %q", v)
	}
	if m.chatHistoryCursor != -1 {
		t.Errorf("cursor = %d, want -1 (history must not engage)", m.chatHistoryCursor)
	}
}

// TestChatHistory_ShellPrefixOwnsArrow guards Phase U S7's contract:
// when the composer starts with `!`, ↑/↓ walk shell history, not chat
// history. Without shellHistory wired the branch falls through to
// chat history (which is the right behavior for tests + dev-aid
// builds without a shell-history file); we assert the chat-history
// path runs only AFTER the shell-history check.
func TestChatHistory_ShellPrefixDoesNotEngageWhenShellHistoryWired(t *testing.T) {
	m := newHistoryModel(t, "previous chat message")
	m.shellHistory = nil // explicit: shell wiring absent
	m.ta.SetValue("!ls -la")

	// With no shell history wired and a `!` buffer, hasShellPrefix()
	// is true but the first branch's `m.shellHistory != nil` guard
	// fails — so we fall through. Our chat-history engage gate then
	// keeps the multi-line check false (single line) but the user is
	// in shell mode. We need to NOT recall a chat message because it
	// would clobber the `!ls -la` they were typing. So the engage
	// check should also bail on shell-prefixed input.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(*Model)
	if v := m.ta.Value(); v != "!ls -la" {
		t.Errorf("shell-prefixed composer should not be replaced by chat history; textarea = %q", v)
	}
}

// TestChatHistory_DownOutsideWalkIsNoOp pins the "↓ without a prior
// ↑ does nothing" rule. Otherwise typing some text and pressing ↓
// would wipe it.
func TestChatHistory_DownOutsideWalkIsNoOp(t *testing.T) {
	m := newHistoryModel(t, "x")
	m.ta.SetValue("typed text")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(*Model)
	if v := m.ta.Value(); v != "typed text" {
		t.Errorf("↓ outside a walk should not touch the textarea; got %q", v)
	}
}

// TestChatHistory_ClearResetsCursor regression-tests the case where
// /clear empties the transcript mid-walk. The cursor must reset so
// the next ↑ doesn't index into a stale-length history.
func TestChatHistory_ClearResetsCursor(t *testing.T) {
	m := newHistoryModel(t, "msg1", "msg2")
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.chatHistoryCursor != 0 {
		t.Fatalf("setup: cursor should be 0 after ↑; got %d", m.chatHistoryCursor)
	}

	// Trigger /clear via the slash dispatch path.
	m.ta.SetValue("/clear")
	if cmd := m.submit(); cmd != nil {
		_ = cmd()
	}
	if m.chatHistoryCursor != -1 {
		t.Errorf("/clear should reset cursor to -1; got %d", m.chatHistoryCursor)
	}
}
