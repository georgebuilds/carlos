// Phase 9 slice 9j - `/compact` Claude Code parity.
//
// /compact summarizes the chat's conversation history and resets the
// model's context to the summary, freeing space for new turns. /clear
// is for "forget everything"; /compact is for "remember the gist,
// drop the details".
//
// Flow:
//
//  1. Build a []providers.Message from the chat transcript (user +
//     assistant rows only; tool / steering / state-change rows skip).
//  2. Call m.summarizer.Summarize(ctx, history) - memory.Summarizer.
//  3. Append EvtSessionReset (same marker /clear uses; this is what
//     tells chatglue.buildHistory to truncate the projection).
//  4. Append a synthetic EvtUserMessage carrying "[compacted summary]
//     \n\n" + summary as the seed context the model sees next turn.
//  5. Clear the rendered transcript; the events flow back through the
//     subscription pump and rebuild it from the post-reset slice.
//
// The pre-compact events stay in the log for audit - the summary is a
// new synthetic row, not a destructive edit. This keeps /compact
// reversible-by-design: a future "uncompact" verb could restore the
// pre-summary history because every row is still there, just gated by
// the reset marker.
package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/memory"
	"github.com/georgebuilds/carlos/internal/providers"
)

// compactSummaryTimeout caps the synchronous summarizer call. The
// LLMSummarizer makes one provider call; 60s is generous (Anthropic's
// streaming first-byte median is well under 5s, full response on a
// 4k-token transcript under 20s).
const compactSummaryTimeout = 60 * time.Second

// compactSummaryPrefix marks the synthetic user_message so a future
// reader of the log (or an "uncompact" verb) can distinguish a true
// user turn from a compact-emitted seed.
const compactSummaryPrefix = "[compacted summary]\n\n"

// WithSummarizer wires a memory.Summarizer so the `/compact` slash
// command can summarize and reset the conversation. When nil, /compact
// echoes "not configured" - graceful degradation. Production wires
// memory.LLMSummarizer against the chat's provider + model; tests
// inject a fake that returns a canned string.
func WithSummarizer(s memory.Summarizer) Option {
	return func(m *Model) { m.summarizer = s }
}

// runCompactCmd is the chat-side /compact pipeline. Returns a tea.Cmd
// that runs the summarizer + writes the reset + synthetic-user pair to
// the event log, then surfaces a statusMsg confirming the swap.
//
// Empty-history short-circuit: if the transcript projects down to zero
// user-or-assistant rows we skip the provider call and echo "nothing
// to compact" - Claude Code's behavior for the same edge.
//
// Failures inside the goroutine surface as a warn-colored statusMsg
// via errMsg. The pre-compact events stay in the log unchanged so a
// failed compact is reversible (no partial state to clean up).
func (m *Model) runCompactCmd() tea.Cmd {
	if m.summarizer == nil {
		return func() tea.Msg {
			return statusMsg{
				text: "compact requires an LLM-backed summarizer; not configured",
				kind: statusWarn,
			}
		}
	}
	history := transcriptToMessages(m.transcript)
	if len(history) == 0 {
		return func() tea.Msg {
			return statusMsg{text: "nothing to compact", kind: statusInfo}
		}
	}

	summarizer := m.summarizer
	log := m.log
	agentID := m.agentID
	turnCount := len(history)

	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), compactSummaryTimeout)
		defer cancel()
		summary, _, err := summarizer.Summarize(ctx, history)
		if err != nil {
			return statusMsg{
				text: "compact failed: " + err.Error(),
				kind: statusWarn,
			}
		}
		summary = trimTrailingWhitespace(summary)
		if summary == "" {
			return statusMsg{
				text: "compact failed: empty summary",
				kind: statusWarn,
			}
		}

		// 1. Reset marker. chatglue.buildHistory drops everything
		//    before this on the next turn; the rendered transcript
		//    drops on applyEvent via the existing EvtSessionReset
		//    case.
		appendCtx, appendCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer appendCancel()
		if _, err := log.Append(appendCtx, agent.Event{
			AgentID: agentID,
			TS:      time.Now().UTC(),
			Type:    agent.EvtSessionReset,
			Payload: []byte("{}"),
		}); err != nil {
			return statusMsg{
				text: "compact: append reset failed: " + err.Error(),
				kind: statusWarn,
			}
		}

		// 2. Synthetic user_message carrying the summary so the next
		//    turn's prompt sees the gist as the prior context.
		seed, err := json.Marshal(agent.MessagePayload{
			Text: compactSummaryPrefix + summary,
		})
		if err != nil {
			return statusMsg{
				text: "compact: marshal summary: " + err.Error(),
				kind: statusWarn,
			}
		}
		if _, err := log.Append(appendCtx, agent.Event{
			AgentID: agentID,
			TS:      time.Now().UTC(),
			Type:    agent.EvtUserMessage,
			Payload: seed,
		}); err != nil {
			return statusMsg{
				text: "compact: append summary: " + err.Error(),
				kind: statusWarn,
			}
		}

		return statusMsg{
			text: fmt.Sprintf("conversation compacted (%d messages → 1 summary)", turnCount),
			kind: statusInfo,
		}
	}
}

// transcriptToMessages projects the rendered transcript into the
// providers.Message slice the summarizer ingests. Only user +
// assistant rows contribute - tool calls / results / steering / state
// changes are conversation-adjacent metadata, not prose the model
// should be asked to summarize. (The summarizer's LLMSummarizer prompt
// is also tuned for plain conversational turns.)
//
// Empty-text rows skip so a defensively-empty assistant turn doesn't
// produce a zero-length Block the provider rejects.
func transcriptToMessages(entries []transcriptEntry) []providers.Message {
	out := make([]providers.Message, 0, len(entries))
	for _, e := range entries {
		var role string
		switch e.kind {
		case entryUserMessage:
			role = "user"
		case entryAssistantMessage:
			role = "assistant"
		default:
			continue
		}
		if e.text == "" {
			continue
		}
		out = append(out, providers.Message{
			Role:    role,
			Content: []providers.Block{{Kind: "text", Text: e.text}},
		})
	}
	return out
}

// trimTrailingWhitespace strips only trailing whitespace + newlines
// from s. The summarizer's output usually ends with a stray newline
// from the streaming finalizer; the leading whitespace is rare and
// could be intentional (e.g. a code-fence opening). Conservative trim.
func trimTrailingWhitespace(s string) string {
	for len(s) > 0 {
		last := s[len(s)-1]
		if last == ' ' || last == '\t' || last == '\n' || last == '\r' {
			s = s[:len(s)-1]
			continue
		}
		break
	}
	return s
}
