package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/georgebuilds/carlos/internal/providers"
)

// Summarizer turns a conversation transcript (or any provider Message
// slice) into a one-paragraph factual summary suitable for FTS5
// indexing. It returns the summary text + a coarse token count so
// callers can budget downstream context.
//
// The summarizer is invoked from the agent loop's conversation-close
// hook (Phase 7 follow-up wires this). Until then, callers can
// exercise the path with NaiveSummarizer (no LLM call).
type Summarizer interface {
	Summarize(ctx context.Context, msgs []providers.Message) (text string, tokens int, err error)
}

// NaiveSummarizer is a zero-cost stub that produces a placeholder
// summary from the message list shape — no provider call, no token
// spend. Useful for tests + early CLI exercising before the real
// summarizer hook is wired.
//
// Output shape: "<N messages, last user said: ...>" — keeps the FTS5
// index populated with searchable content (the last user message
// often carries the topic).
type NaiveSummarizer struct{}

// Summarize implements Summarizer. Truncation: we cap the "last user
// said" tail at 256 runes so a single huge paste doesn't bloat the
// row. Token count is approximated as `len(text) / 4`, the standard
// English-text rule of thumb.
func (NaiveSummarizer) Summarize(_ context.Context, msgs []providers.Message) (string, int, error) {
	if len(msgs) == 0 {
		return "", 0, errors.New("memory: NaiveSummarizer: empty messages")
	}
	lastUser := ""
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			lastUser = firstTextBlock(msgs[i])
			break
		}
	}
	if lastUser == "" {
		// No user message — fall back to the last block of any role.
		lastUser = firstTextBlock(msgs[len(msgs)-1])
	}
	if r := []rune(lastUser); len(r) > 256 {
		lastUser = string(r[:256]) + "…"
	}
	text := fmt.Sprintf("<%d messages, last user said: %s>", len(msgs), strings.TrimSpace(lastUser))
	return text, approxTokens(text), nil
}

// LLMSummarizer calls a providers.Provider with a fixed prompt
// ("summarize this conversation in one factual paragraph") and
// returns the assistant's textual response. Cost is approximately
// $0.005 per call at v0 model defaults (Sonnet-class with a short
// transcript).
//
// Implementation note: this is the v0 shape and intentionally
// minimal. It does NOT chunk long transcripts, does NOT retry on
// transient provider errors, and does NOT respect prompt caching
// (the transcript is the cache-defeating part of every call). Phase 7
// follow-up may grow those once we have telemetry on real
// conversation lengths.
type LLMSummarizer struct {
	Provider providers.Provider
	Model    string // optional; provider default if empty
}

// summarizerPrompt is the fixed system prompt. Kept terse — the model
// follows instructions better when the prompt is short.
const summarizerPrompt = "You summarize a conversation between a user and an AI assistant. Reply with exactly one factual paragraph (3-5 sentences) covering: what the user wanted, what was decided, and any open thread. Do NOT add preamble, headings, or markdown."

// Summarize implements Summarizer. Token count is the assistant
// response length / 4 (same approximation as NaiveSummarizer; the
// real provider may report exact usage via EventStopReason but the v0
// surface doesn't expose that yet).
func (s LLMSummarizer) Summarize(ctx context.Context, msgs []providers.Message) (string, int, error) {
	if s.Provider == nil {
		return "", 0, errors.New("memory: LLMSummarizer: nil provider")
	}
	if len(msgs) == 0 {
		return "", 0, errors.New("memory: LLMSummarizer: empty messages")
	}
	// We give the provider the system prompt and the raw conversation
	// as the user content. Building a synthetic single user message
	// keeps the request shape provider-agnostic — every adapter
	// understands a system + user pair.
	flat := flattenConversation(msgs)
	req := providers.Request{
		Model:  s.Model,
		System: summarizerPrompt,
		Messages: []providers.Message{
			{
				Role: "user",
				Content: []providers.Block{
					{Kind: "text", Text: flat},
				},
			},
		},
	}
	stream, err := s.Provider.Stream(ctx, req)
	if err != nil {
		return "", 0, fmt.Errorf("memory: LLMSummarizer: stream open: %w", err)
	}
	var sb strings.Builder
	for ev := range stream {
		switch ev.Kind {
		case providers.EventTextDelta:
			sb.WriteString(ev.Text)
		case providers.EventError:
			if ev.Err != nil {
				return "", 0, fmt.Errorf("memory: LLMSummarizer: stream error: %w", ev.Err)
			}
		}
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return "", 0, errors.New("memory: LLMSummarizer: empty response")
	}
	return text, approxTokens(text), nil
}

// flattenConversation renders msgs into a single string the
// summarizer LLM can ingest. Format: one block per message, role
// prefix, blank line between. Tool blocks are inlined as
// `[tool: name]` markers so the summary can mention them without
// reproducing tool input/output verbatim.
func flattenConversation(msgs []providers.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		if sb.Len() > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(strings.ToUpper(m.Role))
		sb.WriteString(":\n")
		for _, b := range m.Content {
			switch b.Kind {
			case "text":
				sb.WriteString(b.Text)
			case "tool_use":
				fmt.Fprintf(&sb, "[tool_use: %s]", b.ToolName)
			case "tool_result":
				fmt.Fprintf(&sb, "[tool_result: %s]", b.ToolUseID)
			default:
				if b.Text != "" {
					sb.WriteString(b.Text)
				}
			}
		}
	}
	return sb.String()
}

// firstTextBlock returns the first text block's payload from a
// providers.Message, or empty string if none exist.
func firstTextBlock(m providers.Message) string {
	for _, b := range m.Content {
		if b.Kind == "text" && b.Text != "" {
			return b.Text
		}
	}
	return ""
}

// approxTokens is the rule-of-thumb token estimator used when the
// provider doesn't report exact usage. Good to ~30% on English prose,
// which is fine for the budget telemetry we use this for.
func approxTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}
