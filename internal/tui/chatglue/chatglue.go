// Package chatglue wires the chat TUI to a real provider + tool loop.
//
// The chat package owns the read + write of EvtUserMessage events;
// chatglue owns the reaction: subscribe to those events, run
// agent.Run with the configured provider + tools, stream live
// assistant deltas into the chat's TextSource, and persist the sealed
// turn as an EvtAssistantMessage event so it survives reloads.
//
// Why a separate package: chat depends on agent (event log + types);
// agent must NOT depend on chat (it has no business knowing about a
// UI). chatglue lives one level up so it can import both without
// closing the cycle, and so the chat dev-aid (which uses a stub
// MemTextSource) and the production default-mode TUI (which uses
// chatglue) share zero glue code - the seam is the TextSource
// interface itself.
package chatglue

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/tools"
)

// TextSource is the publish seam the chat view polls each render
// frame. Mirrors internal/tui/chat.TextSource by structural typing -
// chatglue intentionally doesn't import chat to keep this package
// reusable from other surfaces (e.g. a future web TUI).
type TextSource interface {
	Append(agentID, delta string)
	Reset(agentID string)
}

// Config bundles the provider + model + tools + budget knobs chatglue
// passes into agent.Run for each turn. Built once by the caller
// (cmd/carlos.runDefault) and reused; fields are not mutated.
type Config struct {
	// Provider is the streaming client (anthropic / openai / etc).
	// Required.
	Provider providers.Provider
	// Model is the model id sent on each request. Required.
	Model string
	// Tools is the registry the model sees during a turn. The model
	// can call any tool registered here; chatglue does not gate.
	// Optional - a nil registry means "text only".
	Tools *tools.Registry
	// System is the system prompt. Optional; defaults to "" which
	// lets the provider use its built-in default.
	System string
	// Approver is invoked per tool call. Optional; AutoApprover
	// (yes-to-everything) when nil. Production wires the same
	// stdinApprover the `please` path uses or a TUI-side prompt.
	Approver agent.Approver
	// Budget caps tokens / cost for each turn. Optional.
	Budget agent.Budget
	// MaxIterations caps tool-use ping-pong per turn (default 25
	// from agent.Run when zero).
	MaxIterations int
}

// Loop is the per-session glue. One Loop runs per chat - it owns the
// subscription to that agent's event log + the goroutine that drives
// agent.Run on each user message. Lifecycle: NewLoop → Start → Stop
// (idempotent). Start returns immediately; the work happens in the
// background goroutine.
type Loop struct {
	cfg     Config
	log     *agent.SQLiteEventLog
	source  TextSource
	agentID string

	stopOnce sync.Once
	cancel   context.CancelFunc
}

// NewLoop wires a Loop. agentID is the chat's parent agent id (the
// id chat.New was given). The log is the same event log the chat
// reads from + writes EvtUserMessage into; chatglue subscribes for
// fan-out + appends EvtAssistantMessage on turn completion.
func NewLoop(cfg Config, log *agent.SQLiteEventLog, source TextSource, agentID string) *Loop {
	return &Loop{cfg: cfg, log: log, source: source, agentID: agentID}
}

// Start subscribes to the agent's event channel and spins the run
// goroutine. Idempotent: a second Start is a no-op.
//
// Cancelling parentCtx propagates into every in-flight agent.Run via
// the per-turn child context; the loop drains its pending event and
// exits cleanly.
func (l *Loop) Start(parentCtx context.Context) error {
	if l == nil {
		return fmt.Errorf("chatglue: nil loop")
	}
	ctx, cancel := context.WithCancel(parentCtx)
	l.cancel = cancel
	ch, unsub, err := l.log.Subscribe(l.agentID)
	if err != nil {
		cancel()
		return fmt.Errorf("chatglue: subscribe: %w", err)
	}
	go func() {
		defer unsub()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-ch:
				if !ok {
					return
				}
				if ev.Type != agent.EvtUserMessage {
					continue
				}
				l.handleUserMessage(ctx, ev)
			}
		}
	}()
	return nil
}

// Stop terminates the run goroutine. Idempotent - safe to call from
// a defer + an explicit Shutdown path.
func (l *Loop) Stop() {
	l.stopOnce.Do(func() {
		if l.cancel != nil {
			l.cancel()
		}
	})
}

// handleUserMessage runs one full assistant turn in response to a
// user message. Pipeline:
//
//  1. Build the message history from the event log (user + assistant
//     events only - tool calls/results don't survive across turns in
//     v0; the model only sees the conversational arc).
//  2. Append the just-arrived user message to the history.
//  3. Stream the response into TextSource via a chunked writer that
//     batches by newline + every ~32 bytes to keep the UI smooth
//     without spamming the lock.
//  4. On completion, append an EvtAssistantMessage event with the
//     full sealed text + TextSource.Reset.
//
// Errors at any step surface as a system-note assistant message
// (prefixed "carlos:") so the user sees the failure in-line rather
// than via a stderr leak.
func (l *Loop) handleUserMessage(ctx context.Context, _ agent.Event) {
	history, err := l.buildHistory(ctx)
	if err != nil {
		l.surfaceError(ctx, fmt.Errorf("load history: %w", err))
		return
	}

	writer := &textSourceWriter{source: l.source, agentID: l.agentID}
	opts := agent.LoopOptions{
		Model:         l.cfg.Model,
		System:        l.cfg.System,
		TextSink:      writer,
		Approver:      l.cfg.Approver,
		Budget:        l.cfg.Budget,
		MaxIterations: l.cfg.MaxIterations,
		Tools:         buildToolSpecs(l.cfg.Tools),
	}

	msgs, err := agent.Run(ctx, l.cfg.Provider, l.cfg.Tools, opts, history)
	if err != nil {
		l.surfaceError(ctx, err)
		return
	}

	// agent.Run returns the FULL message slice (history + new turns),
	// so we slice off everything we passed in. Otherwise every
	// persisted assistant_message event ends up containing ALL prior
	// assistant text concatenated, and every tool event from prior
	// turns gets persisted again. Field repro: after a few turns,
	// each rendered "🧢: …" entry grows by including every previous
	// response, which reads as "Carlos's new reply appended to the
	// old one without a cap" because the entry's lone cap is at the
	// top of the (now multi-turn) text blob.
	newMsgs := msgs
	if len(history) <= len(msgs) {
		newMsgs = msgs[len(history):]
	}

	// Persist tool_call + tool_result events BEFORE the assistant
	// turn so the rendered transcript order matches the model's
	// actual reasoning sequence: model asks → tool runs → model
	// summarizes. Persisting after the assistant text would order
	// them as: summary → tool, which reads backwards.
	l.persistToolEvents(ctx, newMsgs)
	full := finalAssistantText(newMsgs)
	if full == "" && hadToolUse(newMsgs) {
		// Some models (notably Gemini's tool-use flow) end a turn
		// without a wrap-up message after the tool round-trip. The
		// chat would otherwise show "tool ran" with no
		// acknowledgement - looks like carlos hung. Surface a
		// muted "no follow-up text" line so the user knows the turn
		// is complete + the model just had nothing to add.
		full = "(no follow-up text after tools)"
	}
	if err := l.persistAssistantTurn(ctx, full); err != nil {
		l.surfaceError(ctx, fmt.Errorf("persist assistant turn: %w", err))
	}
	l.source.Reset(l.agentID)
}

// hadToolUse reports whether msgs contains at least one tool_use
// block. Used to detect the "model ran tools then stopped without
// commenting" path so the chat can surface a placeholder instead of
// silently dropping the turn.
func hadToolUse(msgs []providers.Message) bool {
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Kind == "tool_use" {
				return true
			}
		}
	}
	return false
}

// ToolResultPreviewCap is the max payload size persisted for a single
// tool_result event. Larger outputs land in the model's context fine
// (agent.Run sees the full bytes); the persisted event just carries a
// preview the chat transcript can render without bloating the log.
const ToolResultPreviewCap = 2048

// persistToolEvents walks msgs and appends one EvtToolCall +
// EvtToolResult event per tool_use/tool_result block pair. Best-
// effort: a marshal/append failure on one tool doesn't abort the
// rest. Order preserved across iterations so the transcript reads as
// the model's actual sequence.
func (l *Loop) persistToolEvents(ctx context.Context, msgs []providers.Message) {
	for _, msg := range msgs {
		for _, b := range msg.Content {
			switch b.Kind {
			case "tool_use":
				payload, err := json.Marshal(agent.ToolCall{Name: b.ToolName, Input: b.ToolInput})
				if err != nil {
					continue
				}
				_, _ = l.log.Append(ctx, agent.Event{
					AgentID: l.agentID, TS: time.Now().UTC(),
					Type: agent.EvtToolCall, Payload: payload,
				})
			case "tool_result":
				out := b.ToolResult
				if len(out) > ToolResultPreviewCap {
					out = out[:ToolResultPreviewCap]
				}
				// Map back to a tool name: tool_result blocks don't
				// carry one, but the preceding tool_use does. Walk
				// msgs again to find the matching ToolUseID - short
				// O(N) is fine, N is tools-per-turn (usually <10).
				name := lookupToolName(msgs, b.ToolUseID)
				isErr := isErrorResult(b.ToolResult)
				payload, err := json.Marshal(agent.ToolResult{
					Name: name, Output: out, IsError: isErr,
				})
				if err != nil {
					continue
				}
				_, _ = l.log.Append(ctx, agent.Event{
					AgentID: l.agentID, TS: time.Now().UTC(),
					Type: agent.EvtToolResult, Payload: payload,
				})
			}
		}
	}
}

// lookupToolName scans msgs for the tool_use block whose ToolUseID
// matches id and returns its ToolName. Returns "" if not found
// (defensive - shouldn't happen on well-formed Anthropic protocol).
func lookupToolName(msgs []providers.Message, id string) string {
	for _, msg := range msgs {
		for _, b := range msg.Content {
			if b.Kind == "tool_use" && b.ToolUseID == id {
				return b.ToolName
			}
		}
	}
	return ""
}

// isErrorResult heuristic: the loop wraps rejected + errored tools
// with the "(rejected by user)" / "tool error: …" prefixes documented
// in agent.executeOneTool. Match those to surface error styling in
// the chat transcript.
func isErrorResult(b []byte) bool {
	s := string(b)
	return strings.HasPrefix(s, "(rejected by user)") || strings.HasPrefix(s, "tool error:")
}

// buildHistory projects the event log into a []providers.Message
// suitable for agent.Run's initial argument. Only EvtUserMessage and
// EvtAssistantMessage events contribute - tool calls/results are not
// re-played across turns (v0 limitation; the model still has them
// inside a single turn via agent.Run's own loop). The just-arrived
// user message is included because it was already appended to the
// log before chatglue received the eventMsg.
//
// EvtSessionReset resets the accumulator: anything before the latest
// reset is dropped from the history the model sees. Pre-reset events
// stay in the log for audit; they just don't feed the next turn.
// This is the durable side of `/clear` - without it, "clear" would
// only wipe the visual transcript while the model kept replying as
// if mid-conversation.
func (l *Loop) buildHistory(ctx context.Context) ([]providers.Message, error) {
	evs, err := l.log.Read(ctx, l.agentID, 0)
	if err != nil {
		return nil, err
	}
	out := make([]providers.Message, 0, len(evs))
	for _, ev := range evs {
		if ev.Type == agent.EvtSessionReset {
			out = out[:0]
			continue
		}
		var role string
		switch ev.Type {
		case agent.EvtUserMessage:
			role = "user"
		case agent.EvtAssistantMessage:
			role = "assistant"
		default:
			continue
		}
		var p agent.MessagePayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			continue
		}
		if p.Text == "" {
			continue
		}
		out = append(out, providers.Message{
			Role:    role,
			Content: []providers.Block{{Kind: "text", Text: p.Text}},
		})
	}
	return out, nil
}

// persistAssistantTurn appends the sealed assistant text as an
// EvtAssistantMessage event. Empty text → no event (the next user
// message will get a fresh response; an empty assistant row would
// just clutter the transcript).
func (l *Loop) persistAssistantTurn(ctx context.Context, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	payload, err := json.Marshal(agent.MessagePayload{Text: text})
	if err != nil {
		return err
	}
	_, err = l.log.Append(ctx, agent.Event{
		AgentID: l.agentID,
		TS:      time.Now().UTC(),
		Type:    agent.EvtAssistantMessage,
		Payload: payload,
	})
	return err
}

// surfaceError pushes the failure as both a live TextSource delta
// (visible immediately) AND a sealed EvtAssistantMessage (visible
// across reloads). Prefix marks the line as carlos-generated so the
// user can tell it apart from a real model response.
func (l *Loop) surfaceError(ctx context.Context, err error) {
	msg := "carlos: " + err.Error()
	l.source.Append(l.agentID, msg)
	_ = l.persistAssistantTurn(ctx, msg)
	l.source.Reset(l.agentID)
}

// finalAssistantText concatenates the text blocks of EVERY assistant
// message in msgs (across all agent.Run iterations within one turn).
// Tool-use blocks are skipped; consecutive text blocks across
// iterations are joined with "\n\n" so a multi-step response
// ("let me check…" → bash → "here's what I found…") reads as a
// single arc in the persisted transcript. Taking only the last
// message would drop any preamble the model said before invoking a
// tool, which is exactly the text the user just watched stream in.
func finalAssistantText(msgs []providers.Message) string {
	var parts []string
	for _, msg := range msgs {
		if msg.Role != "assistant" {
			continue
		}
		var b strings.Builder
		for _, blk := range msg.Content {
			if blk.Kind == "text" {
				b.WriteString(blk.Text)
			}
		}
		if s := strings.TrimSpace(b.String()); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}

// buildToolSpecs lifts a tools.Registry into the []providers.ToolSpec
// shape agent.Run expects. Nil registry → nil slice (text-only run).
func buildToolSpecs(reg *tools.Registry) []providers.ToolSpec {
	if reg == nil {
		return nil
	}
	all := reg.All()
	specs := make([]providers.ToolSpec, 0, len(all))
	for _, t := range all {
		specs = append(specs, providers.ToolSpec{
			Name: t.Name(), Description: t.Description(), Schema: t.Schema(),
		})
	}
	return specs
}

// textSourceWriter adapts a TextSource (Append-by-string) into an
// io.Writer so agent.LoopOptions.TextSink can stream the in-flight
// assistant deltas straight into the chat view's live buffer.
type textSourceWriter struct {
	source  TextSource
	agentID string
}

func (w *textSourceWriter) Write(p []byte) (int, error) {
	w.source.Append(w.agentID, string(p))
	return len(p), nil
}

// Compile-time check.
var _ io.Writer = (*textSourceWriter)(nil)
