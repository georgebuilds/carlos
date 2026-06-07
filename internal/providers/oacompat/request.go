package oacompat

import (
	"encoding/json"
	"fmt"

	"github.com/georgebuilds/carlos/internal/providers"
)

// BuildRequest converts the canonical providers.Request into the Chat
// Completions wire shape. System prompt is injected as the first message
// (Chat Completions has no top-level system field - it's a role).
//
// errPrefix is included in error messages so callers can stamp them with
// their provider name (e.g. "openai" or "openrouter") without the shared
// package having to know which one called it.
func BuildRequest(req providers.Request, errPrefix string) (*MessagesRequest, error) {
	out := &MessagesRequest{
		Model:  req.Model,
		Stream: true,
	}
	if req.System != "" {
		out.Messages = append(out.Messages, APIMsg{
			Role: "system", Content: req.System,
		})
	}
	for _, m := range req.Messages {
		converted, err := toAPIMessages(m, errPrefix)
		if err != nil {
			return nil, fmt.Errorf("%s: message role=%s: %w", errPrefix, m.Role, err)
		}
		out.Messages = append(out.Messages, converted...)
	}
	for _, t := range req.Tools {
		params := json.RawMessage(t.Schema)
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out.Tools = append(out.Tools, APITool{
			Type: "function",
			Function: APIToolFnDecl{
				Name: t.Name, Description: t.Description, Parameters: params,
			},
		})
	}
	return out, nil
}

// toAPIMessages converts ONE canonical message into one-or-more wire
// messages. The fan-out is necessary because:
//
//   - Anthropic packs tool_use AND text into a single assistant message with
//     a content[] array.
//   - Chat Completions splits text content and tool_calls onto the same
//     assistant message but uses a flat string for content; that's fine.
//   - Chat Completions sends EACH tool_result as its own role:"tool" message
//     (with tool_call_id), not as blocks inside a user message. So one
//     canonical "user" message containing N tool_result blocks fans out to
//     N role:"tool" wire messages.
//
// Unknown Kind values are rejected - silently dropping them would produce
// confusing model behavior.
func toAPIMessages(m providers.Message, errPrefix string) ([]APIMsg, error) {
	// Partition blocks by category to assemble the right wire shapes.
	var textParts []string
	var toolCalls []APIToolCall
	var toolResults []APIMsg

	for _, b := range m.Content {
		switch b.Kind {
		case "text", "":
			if b.Text != "" {
				textParts = append(textParts, b.Text)
			}
		case "tool_use":
			args := string(b.ToolInput)
			if args == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, APIToolCall{
				ID:   b.ToolUseID,
				Type: "function",
				Function: APIFunction{
					Name:      b.ToolName,
					Arguments: args,
				},
			})
		case "tool_result":
			toolResults = append(toolResults, APIMsg{
				Role:       "tool",
				ToolCallID: b.ToolUseID,
				Content:    string(b.ToolResult),
			})
		default:
			return nil, fmt.Errorf("%s: unknown content kind %q", errPrefix, b.Kind)
		}
	}

	var out []APIMsg
	// Tool results: each becomes its own role:"tool" wire message.
	// Per OpenAI's protocol these must precede / not be mixed with the
	// triggering assistant turn; the caller orders messages correctly so
	// we just emit them in the same position the canonical message held.
	if len(toolResults) > 0 {
		out = append(out, toolResults...)
	}
	// Text + tool_calls share one wire message if both present, OR text
	// alone, OR tool_calls alone with empty content.
	if len(textParts) > 0 || len(toolCalls) > 0 {
		msg := APIMsg{Role: m.Role}
		if len(textParts) > 0 {
			msg.Content = joinText(textParts)
		}
		if len(toolCalls) > 0 {
			msg.ToolCalls = toolCalls
		}
		out = append(out, msg)
	}
	if len(out) == 0 {
		// A message with no usable blocks. Emit an empty one to preserve
		// turn ordering; the model will see an empty user/assistant turn
		// which is fine and uncommon.
		out = append(out, APIMsg{Role: m.Role})
	}
	return out, nil
}

func joinText(parts []string) string {
	if len(parts) == 1 {
		return parts[0]
	}
	// Multiple text blocks in one canonical message are unusual but legal.
	// Join with double-newline so the model sees them as paragraphs rather
	// than running them together.
	out := parts[0]
	for _, p := range parts[1:] {
		out += "\n\n" + p
	}
	return out
}
