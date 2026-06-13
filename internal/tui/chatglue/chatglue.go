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
	"net/http"
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

	// ctx is the per-handler context handleUserMessage stashes before
	// calling agent.Run, so the OnToolCall / OnToolResult hooks can
	// reach a live ctx for their EventLog.Append calls. The loop is
	// single-threaded (one user message at a time) so a single field
	// is safe; if we ever parallelize, this becomes per-handler
	// state passed through closure capture instead.
	ctx context.Context
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
	// Spawn lineage: any sub-agent the model delegates to during this
	// turn (the Agent tool → Supervisor.Spawn) must be parented to THIS
	// thread, not spawned as a parentless top-level row. The tool reads
	// the id back via agent.SpawnParentFromContext. Without this, spawned
	// agents land in the session roster as top-level chats and per-thread
	// children queries (the web crew column, the TUI inline panel) can't
	// find them.
	ctx = agent.WithSpawnParent(ctx, l.agentID)

	// Stash for the OnToolCall / OnToolResult hooks - they run inside
	// agent.Run's loop and need a live ctx for their EventLog writes.
	l.ctx = ctx
	defer func() { l.ctx = nil }()

	history, err := l.buildHistory(ctx)
	if err != nil {
		l.surfaceError(ctx, fmt.Errorf("load history: %w", err))
		return
	}

	writer := &textSourceWriter{source: l.source, agentID: l.agentID}
	// Capture-at-issue: if the configured approver supports per-turn
	// frame snapshots, freeze the cross-frame state at the start of
	// this turn so a mid-turn Ctrl+F (which mutates the approver via
	// SetFrameSubtrees on the same instance) can't relabel an
	// already-issued tool call as cross-frame. The next user message
	// goes through a fresh Loop (rebuilt by swapLoop) and takes its
	// own snapshot. See frames audit §3, internal/agent/policy.go.
	turnApprover := l.cfg.Approver
	if snap, ok := turnApprover.(interface{ SnapshotAtFrame() agent.Approver }); ok {
		turnApprover = snap.SnapshotAtFrame()
	}
	opts := agent.LoopOptions{
		Model:         l.cfg.Model,
		System:        l.cfg.System,
		TextSink:      writer,
		Approver:      turnApprover,
		Budget:        l.cfg.Budget,
		MaxIterations: l.cfg.MaxIterations,
		Tools:         buildToolSpecs(l.cfg.Tools),
		// Stream tool events live: the loop pops the hook the moment a
		// tool_use lands and again when its result comes back. The chat
		// surface renders a "running…" card immediately, then folds in
		// the result on finish — instead of seeing every tool of the
		// turn arrive in a single post-Run batch.
		OnToolCall:   l.persistToolCall,
		OnToolResult: l.persistToolResult,
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
// Aliases agent.ToolResultPreviewCap - the sub-agent write path in
// agent.runChild caps with the same value, so the two can't drift.
const ToolResultPreviewCap = agent.ToolResultPreviewCap

// persistToolCall is wired as agent.LoopOptions.OnToolCall so each
// tool_use lands in the event log the instant the loop observes it,
// BEFORE the tool runs. The chat surface renders a "🔧 <tool> ·
// running…" card on the next subscription pump, instead of waiting
// for the whole turn to wrap.
//
// Best-effort: a marshal/append failure is swallowed - the bigger
// loop carries on, and the next OnToolResult still has the chance to
// land a result card (a fall-through to "standalone card without a
// matching call" branch in the chat). Spawning ctx is the per-handler
// context the surrounding handleUserMessage was given; the hook
// closes over it via the receiver instead of taking a ctx parameter
// so the LoopOptions signature stays simple.
func (l *Loop) persistToolCall(use providers.Block) {
	// Marshal of {string, []byte} can't fail per encoding/json's
	// type contract, so we treat the result as definitely-non-nil
	// and drop the defensive error check. SQLite-side payload-not-
	// null is satisfied unconditionally.
	payload, _ := json.Marshal(agent.ToolCall{Name: use.ToolName, Input: use.ToolInput})
	_, _ = l.log.Append(l.ctx, agent.Event{
		AgentID: l.agentID,
		TS:      time.Now().UTC(),
		Type:    agent.EvtToolCall,
		Payload: payload,
	})
}

// persistToolResult is OnToolResult's wire-up: paired with
// persistToolCall above, it lands the result the moment the tool
// finishes. The chat folds the result into the call card on receipt,
// flipping the status suffix from "running…" to "<N> lines" / "error".
func (l *Loop) persistToolResult(use providers.Block, result providers.Block) {
	out := result.ToolResult
	if len(out) > ToolResultPreviewCap {
		out = out[:ToolResultPreviewCap]
	}
	// Same {string, []byte, bool} marshal-can't-fail invariant as in
	// persistToolCall; the defensive error check would be dead code.
	payload, _ := json.Marshal(agent.ToolResult{
		Name:    use.ToolName, // result blocks carry no name; pair from the use.
		Output:  out,
		IsError: isErrorResult(result.ToolResult),
	})
	_, _ = l.log.Append(l.ctx, agent.Event{
		AgentID: l.agentID,
		TS:      time.Now().UTC(),
		Type:    agent.EvtToolResult,
		Payload: payload,
	})
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
		if ev.Type == agent.EvtUserMessage {
			// Composer-chip expansion (slices I-1 + I-3): user text
			// persists with inline markers (‹p:ID› etc); the model must
			// see the expanded content instead - pastes as fenced
			// blocks, images as real image blocks (vision providers) or
			// readable placeholders (everything else). This is the ONLY
			// expansion point (the live-send path also flows through
			// buildHistory), so markers are expanded exactly once and a
			// raw marker can never leak into the context.
			out = append(out, providers.Message{
				Role:    role,
				Content: l.expandUserBlocks(p.Text, p.Attachments),
			})
			continue
		}
		out = append(out, providers.Message{
			Role:    role,
			Content: []providers.Block{{Kind: "text", Text: p.Text}},
		})
	}
	return out, nil
}

// expandUserBlocks turns one persisted user message (text with chip
// markers + the attachments those markers reference) into the typed
// block slice the provider sees. Slice I-3's bridge:
//
//   - provider without vision (or nil provider): the whole message
//     stays ONE text block via agent.ExpandMarkers - image chips
//     degrade to "[image: label]" placeholders, byte-identical to the
//     pre-I-3 behavior;
//   - vision provider: the message splits at image markers into text
//     blocks + image blocks IN MARKER ORDER, the pixels loaded from
//     the content-addressed artifact store by the attachment's SHA256
//     (the chat TUI stored them there at paste time);
//   - any image that can't be loaded (no SHA, blob missing, bytes
//     that don't sniff as a supported image type) degrades to the
//     same text placeholder marked "(unavailable)" - a broken chip
//     must never fail the turn.
//
// The I-1 invariant carries over: no raw ‹x:id› marker ever reaches
// the model through any of these paths.
func (l *Loop) expandUserBlocks(text string, atts []agent.Attachment) []providers.Block {
	vision := l.cfg.Provider != nil && l.cfg.Provider.Capabilities().Vision
	if !vision {
		return []providers.Block{{Kind: "text", Text: agent.ExpandMarkers(text, atts)}}
	}
	segs := agent.ExpandMarkerSegments(text, atts)
	basePath := agent.ArtifactBasePath("")
	blocks := make([]providers.Block, 0, len(segs))
	for _, seg := range segs {
		if seg.Image == nil {
			blocks = append(blocks, providers.Block{Kind: "text", Text: seg.Text})
			continue
		}
		data, mediaType, ok := loadImageArtifact(basePath, *seg.Image)
		if !ok {
			blocks = append(blocks, providers.Block{
				Kind: "text",
				Text: unavailableImageText(*seg.Image),
			})
			continue
		}
		blocks = append(blocks, providers.ImageBlock(mediaType, data))
	}
	return blocks
}

// supportedImageMediaTypes is the intersection of what Anthropic's
// Messages API and the OpenAI-compatible providers accept. The chat
// composer normalizes clipboard images to PNG, so in practice this is
// belt-and-braces against hand-crafted attachments.
var supportedImageMediaTypes = map[string]bool{
	"image/png":  true,
	"image/jpeg": true,
	"image/gif":  true,
	"image/webp": true,
}

// loadImageArtifact reads an image attachment's bytes from the
// content-addressed artifact store and sniffs their media type.
// ok=false for every failure mode (no SHA recorded, blob missing,
// bytes that aren't a supported image format) - the caller degrades
// to a text placeholder.
func loadImageArtifact(basePath string, att agent.Attachment) (data []byte, mediaType string, ok bool) {
	if att.SHA256 == "" {
		return nil, "", false
	}
	b, err := agent.ReadArtifact(basePath, att.SHA256)
	if err != nil || len(b) == 0 {
		return nil, "", false
	}
	mt := http.DetectContentType(b)
	if !supportedImageMediaTypes[mt] {
		return nil, "", false
	}
	return b, mt, true
}

// unavailableImageText is the degraded stand-in for an image chip
// whose bytes can't be sent despite the provider supporting vision.
// Distinct from the plain capability placeholder so the model (and
// anyone reading a transcript dump) can tell "provider can't see
// images" from "this particular image is gone".
func unavailableImageText(att agent.Attachment) string {
	p := agent.ImagePlaceholder(att)
	return strings.TrimSuffix(p, "]") + " (unavailable)]"
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

// ErrorEventPrefix marks an EvtAssistantMessage as a chatglue-
// surfaced loop / provider error rather than a real model turn.
// The chat package's applyEvent detects this marker and routes the
// message to a bordered "error card" instead of the usual avatar +
// markdown render. The bytes are deliberately ugly (square brackets
// + a hyphen) so a real model is extremely unlikely to produce them
// verbatim at the head of a reply.
const ErrorEventPrefix = "[carlos-error] "

// surfaceError persists a single sealed EvtAssistantMessage tagged
// with [ErrorEventPrefix] so the chat surface renders it as an
// error card instead of an assistant turn. Skipping the live
// TextSource push means the user sees the error a tick later (once
// the subscription pump delivers the event), but the trade is the
// styled card vs. the prior raw "carlos: …" leak into the streaming
// buffer.
func (l *Loop) surfaceError(ctx context.Context, err error) {
	msg := ErrorEventPrefix + err.Error()
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
