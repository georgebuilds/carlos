package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/tools"
)

// Approver is the gate the loop calls before executing each tool call.
// Returning false denies the call; the loop reports "(rejected by user)"
// back to the model so it can adapt rather than crashing.
//
// The approver is also where session-level allowlisting lives - a TUI
// implementation can prompt with y/N/Always and keep "Always" in state.
type Approver interface {
	ApproveToolCall(name string, input []byte) bool
}

// AutoApprover always approves. Used by tests and the --yes flag.
type AutoApprover struct{}

func (AutoApprover) ApproveToolCall(string, []byte) bool { return true }

// LoopOptions configures one tool-use loop run.
type LoopOptions struct {
	Model         string
	System        string
	Tools         []providers.ToolSpec
	Approver      Approver
	TextSink      io.Writer // optional: streams assistant text deltas as they arrive
	MaxIterations int       // 0 → 25 default

	// Steering is an optional channel the supervisor sends user-side
	// nudges on. Between iterations (after collecting the assistant
	// turn, before the next provider call), the loop drains the channel
	// and prepends any pending messages as user-role messages. This is
	// the "delivered at the next tool-call boundary" contract from
	// SPEC § Manage mode § Verbs (Steer).
	//
	// Closing the channel is the canonical "no more steering will
	// arrive" signal - the loop stops draining on close. Sending on a
	// nil channel from the supervisor is a no-op (the loop's drain is
	// a non-blocking select against nil, which never fires).
	Steering <-chan string

	// Budget caps the loop's spend in tokens, cents, and/or wall-clock
	// time. Zero value (or BudgetTracker == nil) disables enforcement
	// entirely - the loop behaves as it did pre-5a. Phase 5 slice 5a.
	Budget Budget

	// BudgetTracker is the running counter the budget gate consults
	// BEFORE each provider stream. nil disables enforcement. The
	// supervisor allocates one Tracker per scope (per-run, per-subtree)
	// and chains them so subtree spend rolls up into the parent.
	//
	// The loop calls BudgetTracker.CheckBudget(opts.Budget) at the top
	// of each iteration; on exceed, it returns wrapped
	// ErrBudgetExceeded so the caller classifies as a graceful end
	// rather than an infra error. After each completed stream the loop
	// pushes an estimate via BudgetTracker.Add (today: from message
	// body length - see budget.EstimateCallCost / Tokens; future:
	// adapters that wire real usage will report through this same Add).
	BudgetTracker *Tracker

	// OnToolCall is called inline the moment a tool_use block is
	// observed in the assistant turn, BEFORE the tool runs. Wired
	// by chatglue to persist EvtToolCall into the event log so the
	// chat surface renders the "running…" card live instead of
	// waiting for the whole turn to wrap up. nil = skip the hook.
	OnToolCall func(use providers.Block)

	// OnToolResult is called inline the moment a tool finishes, with
	// the result block (success or error) ready for persistence. The
	// pair OnToolCall/OnToolResult lets the chat fold the result back
	// into the card it just rendered, mirroring how a streaming
	// transcript reads. nil = skip the hook.
	OnToolResult func(use providers.Block, result providers.Block)
}

// ErrMaxIterations is returned when the loop runs past MaxIterations
// without reaching a non-tool_use stop reason. A safety net so a runaway
// model can't burn unbounded provider calls in one Run.
var ErrMaxIterations = errors.New("loop: max iterations exceeded")

// Run drives the standard Anthropic-style tool-use loop:
//
//  1. Send {messages, tools} to the provider; stream the assistant turn.
//  2. Assemble the assistant's content blocks (text + tool_use).
//  3. If stop_reason != tool_use, return.
//  4. Otherwise, for each tool_use block: approve → execute → build a
//     tool_result block.
//  5. Append the tool_results as a user message and loop.
//
// The loop is provider-agnostic - it talks to providers.Provider and
// expects the canonical Anthropic event shape. Adapters in
// internal/providers/{openai,ollama,...} normalize their wire formats to
// this shape so the same loop runs unchanged.
//
// Returns the full message history (including the final assistant turn)
// so callers can persist it. ctx cancels the in-flight stream and any
// running tool.
func Run(ctx context.Context, p providers.Provider, reg *tools.Registry, opts LoopOptions, initial []providers.Message) ([]providers.Message, error) {
	if opts.Approver == nil {
		opts.Approver = AutoApprover{}
	}
	maxIter := opts.MaxIterations
	if maxIter == 0 {
		maxIter = 25
	}

	messages := make([]providers.Message, len(initial))
	copy(messages, initial)

	for iter := 0; iter < maxIter; iter++ {
		// Drain any pending steering nudges before constructing the
		// next provider request. This is the "delivered at the next
		// tool-call boundary" contract - never injected mid-stream.
		messages = drainSteering(opts.Steering, messages)

		// Phase 5 slice 5a: budget gate. Refusing here gives the model
		// a clean termination instead of a mid-stream yank. We classify
		// as a graceful end (no error wrapping with the iter prefix)
		// so callers can errors.Is(err, ErrBudgetExceeded) and treat
		// the run as "completed under-budget" rather than infra-broken.
		if opts.BudgetTracker != nil {
			if err := opts.BudgetTracker.CheckBudget(opts.Budget); err != nil {
				return messages, err
			}
		}

		// Estimate request body size for the post-call usage push (only
		// matters when the adapter doesn't report real usage).
		reqBytes := messageBodyBytes(messages)
		sysBytes := len(opts.System)

		stream, err := p.Stream(ctx, providers.Request{
			Model:    opts.Model,
			System:   opts.System,
			Messages: messages,
			Tools:    opts.Tools,
		})
		if err != nil {
			return messages, fmt.Errorf("loop: stream iter %d: %w", iter, err)
		}

		assistant, stopReason, err := collectAssistant(stream, opts.TextSink)
		if err != nil {
			return messages, fmt.Errorf("loop: iter %d: %w", iter, err)
		}
		messages = append(messages, assistant)

		// Push an estimate to the Tracker so the NEXT iteration's gate
		// has a chance to fire. Adapters that wire real usage will land
		// real numbers here instead (Phase 5d / per-adapter follow-up).
		if opts.BudgetTracker != nil {
			respBytes := blockBodyBytes(assistant.Content)
			opts.BudgetTracker.Add(
				EstimateCallTokens(sysBytes, reqBytes),
				EstimateCallTokens(0, respBytes),
				EstimateCallCost(sysBytes, reqBytes+respBytes),
			)
		}

		if stopReason != "tool_use" {
			return messages, nil
		}

		// Execute each tool_use block in this turn. Anthropic supports
		// parallel tool_use; the protocol expects ALL tool_result blocks
		// to come back in the same subsequent user message. We invoke
		// the optional OnToolCall / OnToolResult hooks inline so any
		// listener (chatglue today) sees a tool's start + finish as it
		// happens, not as a post-turn batch. Hook errors are the
		// listener's problem; the loop doesn't observe them.
		results := make([]providers.Block, 0)
		for _, b := range assistant.Content {
			if b.Kind != "tool_use" {
				continue
			}
			if opts.OnToolCall != nil {
				opts.OnToolCall(b)
			}
			res := executeOneTool(ctx, reg, opts.Approver, b)
			if opts.OnToolResult != nil {
				opts.OnToolResult(b, res)
			}
			results = append(results, res)
		}
		if len(results) == 0 {
			// stop_reason was tool_use but no tool_use blocks were
			// emitted - provider bug or schema drift. Surface
			// rather than spinning.
			return messages, fmt.Errorf("loop: stop=tool_use but no tool_use blocks in assistant turn")
		}
		messages = append(messages, providers.Message{Role: "user", Content: results})
	}
	return messages, ErrMaxIterations
}

// messageBodyBytes sums the on-wire byte size of every Text and
// ToolResult body across the given messages. It's a rough proxy for
// "how big is this provider request" used by the budget gate's
// estimate fallback when the adapter doesn't report real usage.
func messageBodyBytes(msgs []providers.Message) int {
	n := 0
	for _, m := range msgs {
		n += blockBodyBytes(m.Content)
	}
	return n
}

// blockBodyBytes is messageBodyBytes for a single message's content.
func blockBodyBytes(blocks []providers.Block) int {
	n := 0
	for _, b := range blocks {
		n += len(b.Text)
		n += len(b.ToolInput)
		n += len(b.ToolResult)
	}
	return n
}

// drainSteering pulls every available message from ch and appends it
// to messages as a user-role text block. Non-blocking: returns as soon
// as the channel has nothing pending. Safe on a nil channel.
//
// Each drained message is wrapped with a "[steer] " prefix so the
// model can distinguish supervisor nudges from organic user input -
// helpful when steering arrives mid-conversation about a different
// concern than the original prompt.
func drainSteering(ch <-chan string, messages []providers.Message) []providers.Message {
	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return messages
			}
			if msg == "" {
				continue
			}
			messages = append(messages, providers.Message{
				Role: "user",
				Content: []providers.Block{
					{Kind: "text", Text: "[steer] " + msg},
				},
			})
		default:
			return messages
		}
	}
}

// executeOneTool runs a single tool_use block and returns the
// corresponding tool_result. Denials and execution errors come back as
// tool_result text so the model can adapt - they are not surfaced as
// loop errors.
func executeOneTool(ctx context.Context, reg *tools.Registry, approver Approver, use providers.Block) providers.Block {
	if !approver.ApproveToolCall(use.ToolName, use.ToolInput) {
		return providers.Block{
			Kind:       "tool_result",
			ToolUseID:  use.ToolUseID,
			ToolResult: []byte("(rejected by user)"),
		}
	}
	out, err := reg.Execute(ctx, use.ToolName, use.ToolInput)
	if err != nil {
		return providers.Block{
			Kind:       "tool_result",
			ToolUseID:  use.ToolUseID,
			ToolResult: []byte(fmt.Sprintf("tool error: %v", err)),
		}
	}
	return providers.Block{
		Kind:       "tool_result",
		ToolUseID:  use.ToolUseID,
		ToolResult: out,
	}
}

// collectAssistant walks a provider event stream and reconstructs the
// assistant turn as a single providers.Message. Text deltas accumulate
// into text blocks; tool_use_start opens a tool_use block; tool_use_end
// finalizes its input field. Multiple text/tool_use blocks can interleave
// - the assembled blocks preserve order.
//
// textSink (if non-nil) receives every text delta as it arrives so the
// CLI / TUI can render progressively without waiting for the full turn.
func collectAssistant(stream <-chan providers.Event, textSink io.Writer) (providers.Message, string, error) {
	var blocks []providers.Block
	var textBuf strings.Builder
	var stopReason string

	flushText := func() {
		if textBuf.Len() == 0 {
			return
		}
		blocks = append(blocks, providers.Block{
			Kind: "text",
			Text: textBuf.String(),
		})
		textBuf.Reset()
	}

	for ev := range stream {
		switch ev.Kind {
		case providers.EventTextDelta:
			// Scrub terminal control sequences before they hit the
			// live text sink. A misbehaving (or compromised) provider
			// streaming raw ANSI / OSC escapes — or an error envelope
			// leaking control bytes through the SSE channel — would
			// otherwise repaint the chat surface OR get echoed by the
			// terminal as input back into bubbletea's stdin (which is
			// where "weird characters appear in the input box" reports
			// come from). The persisted message still gets the raw
			// text (in case the provider intentionally returned a
			// control glyph as content), but the textSink sees only
			// printable bytes.
			textBuf.WriteString(ev.Text)
			if textSink != nil {
				_, _ = textSink.Write([]byte(scrubControlChars(ev.Text)))
			}
		case providers.EventToolUseStart:
			flushText()
			if ev.ToolUse == nil {
				continue
			}
			blocks = append(blocks, providers.Block{
				Kind:      "tool_use",
				ToolUseID: ev.ToolUse.ID,
				ToolName:  ev.ToolUse.Name,
			})
		case providers.EventToolUseDelta:
			// Adapter handles input accumulation per content-block index;
			// we only need the final input on ToolUseEnd. Nothing to do
			// here unless we want to surface progressive input to the
			// TUI in a future slice.
		case providers.EventToolUseEnd:
			if ev.ToolUse == nil {
				continue
			}
			// Finalize the most recent tool_use block matching this ID.
			for i := len(blocks) - 1; i >= 0; i-- {
				if blocks[i].Kind == "tool_use" && blocks[i].ToolUseID == ev.ToolUse.ID {
					blocks[i].ToolInput = ev.ToolUse.Input
					break
				}
			}
		case providers.EventStopReason:
			stopReason = ev.Stop
		case providers.EventError:
			return providers.Message{}, "", ev.Err
		}
	}
	flushText()
	return providers.Message{Role: "assistant", Content: blocks}, stopReason, nil
}

// scrubControlChars strips C0/C1 terminal control bytes from s before
// it reaches a live text sink. Most consequentially this drops ESC
// (0x1b) — the lead byte of every CSI / OSC sequence — so a streamed
// SSE chunk containing raw ANSI cannot reach the terminal and provoke
// an interactive response (OSC 11 background-color query, OSC 4 color
// palette query, etc.) that would then be echoed back into bubbletea's
// stdin and land in the chat composer as garbage. Preserves newline,
// carriage return, and tab — those are legitimate printable whitespace.
//
// The non-streamed assistant message keeps the raw text so the model's
// own context window stays faithful; only the human-visible live
// surface is scrubbed.
func scrubControlChars(s string) string {
	// Hot path: no escapes / controls → return verbatim. Most provider
	// chunks land here.
	clean := true
	for i := 0; i < len(s); i++ {
		if isControlByte(s[i]) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isControlByte(c) {
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// isControlByte reports whether c is a C0 / C1 control byte the live
// text sink must drop. Allows newline (0x0a), carriage return (0x0d),
// and tab (0x09); drops everything else in [0x00, 0x1f] plus DEL
// (0x7f) and the C1 range [0x80, 0x9f]. Multi-byte UTF-8 continuation
// bytes are all >= 0xc0 so this byte-level scrub never severs a code
// point.
func isControlByte(c byte) bool {
	switch c {
	case '\t', '\n', '\r':
		return false
	case 0x7f: // DEL
		return true
	}
	if c < 0x20 {
		return true
	}
	if c >= 0x80 && c <= 0x9f {
		return true
	}
	return false
}
