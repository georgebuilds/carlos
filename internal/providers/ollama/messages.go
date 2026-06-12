package ollama

import (
	"encoding/json"
	"fmt"

	"github.com/georgebuilds/carlos/internal/providers"
)

// API request/response shapes for Ollama's /api/chat endpoint. Only the
// fields carlos actually exercises are typed here; everything else falls
// through json.RawMessage or is dropped at parse time.
//
// Wire-format docs:
//   https://github.com/ollama/ollama/blob/main/docs/api.md#generate-a-chat-completion
//
// Tool format mirrors OpenAI verbatim - Ollama adopted that shape. The
// only place the wire diverges from OpenAI: the tool_call's
// function.arguments field is a JSON OBJECT on Ollama (not a string the
// way OpenAI ships it). Adapter logic in client.go marshals that object
// to bytes for the canonical providers.ToolUse.Input field.

// chatRequest is the request body for POST /api/chat.
type chatRequest struct {
	Model    string         `json:"model"`
	Messages []apiMsg       `json:"messages"`
	Tools    []apiTool      `json:"tools,omitempty"`
	Stream   bool           `json:"stream"`
	Options  map[string]any `json:"options,omitempty"`
}

// apiMsg is one message on the wire. Ollama (like OpenAI) uses a flat
// {role, content, tool_calls?, tool_call_id?} shape rather than
// Anthropic's typed-block array. We collapse a canonical Message's
// content blocks into:
//   - role=user/assistant with text content → string content
//   - role=assistant with tool_use blocks → tool_calls array
//   - role=tool (per canonical tool_result block) → role=tool with
//     content=<result body> and tool_call_id pointing at the originating
//     tool_use. Anthropic batches tool_results into one user message
//     containing multiple tool_result blocks; Ollama expects them as
//     separate role=tool messages, so we fan them out at adapter time.
type apiMsg struct {
	Role       string        `json:"role"`
	Content    string        `json:"content"`
	ToolCalls  []apiToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Name       string        `json:"name,omitempty"`
}

// apiToolCall is one tool invocation. Arguments is a JSON object on the
// wire (object literal, not a string) - we keep it as RawMessage so the
// shape round-trips faithfully when the caller is replaying a recorded
// assistant turn.
type apiToolCall struct {
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type,omitempty"` // "function" when present
	Function apiToolCallFun `json:"function"`
}

type apiToolCallFun struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// apiTool is one tool advertised to the model.
type apiTool struct {
	Type     string        `json:"type"` // always "function"
	Function apiToolSchema `json:"function"`
}

type apiToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// buildRequest converts the canonical providers.Request to Ollama's
// /api/chat wire shape. System prompts go in as a leading role=system
// message (Ollama supports the same convention as OpenAI). num_predict
// defaults to 4096 - Ollama's own default is -1 (unbounded until the
// model emits its stop token), but we cap to keep partial-failure
// behavior similar to the other providers.
func buildRequest(req providers.Request) (*chatRequest, error) {
	out := &chatRequest{
		Model:   req.Model,
		Stream:  true,
		Options: map[string]any{"num_predict": 4096},
	}
	if req.System != "" {
		out.Messages = append(out.Messages, apiMsg{
			Role:    "system",
			Content: req.System,
		})
	}
	for _, m := range req.Messages {
		msgs, err := toAPIMessages(m)
		if err != nil {
			return nil, fmt.Errorf("ollama: message role=%s: %w", m.Role, err)
		}
		out.Messages = append(out.Messages, msgs...)
	}
	for _, t := range req.Tools {
		schema := t.Schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out.Tools = append(out.Tools, apiTool{
			Type: "function",
			Function: apiToolSchema{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  schema,
			},
		})
	}
	return out, nil
}

// toAPIMessages flattens one canonical Message into one-or-more wire
// messages. The fan-out exists because:
//
//   - An assistant message with N tool_use blocks → one wire message
//     with N entries in tool_calls (Ollama wants them aggregated).
//   - A user message with N tool_result blocks → N separate role=tool
//     wire messages (Ollama wants them split).
//   - Plain text user/assistant → one wire message; multiple text blocks
//     get concatenated since Ollama has no typed-block shape.
//
// Unknown block kinds are rejected - silently dropping them produces
// confusing model behavior (Anthropic-style design choice, kept for
// consistency across providers).
func toAPIMessages(m providers.Message) ([]apiMsg, error) {
	// Separate the three block categories.
	var textParts []string
	var toolCalls []apiToolCall
	var toolResults []providers.Block
	for _, b := range m.Content {
		switch b.Kind {
		case "text", "":
			if b.Text != "" {
				textParts = append(textParts, b.Text)
			}
		case "image":
			// Ollama advertises Vision=false (capabilities are per-model
			// there and carlos doesn't probe), so callers shouldn't send
			// image blocks - but history replayed after a provider switch
			// legitimately can contain them. Degrade to a visible text
			// placeholder instead of erroring: failing the whole turn
			// over an old screenshot would brick the session. The
			// surrounding newlines keep concat() from smushing the
			// placeholder into adjacent text fragments.
			textParts = append(textParts, "\n[image attachment omitted: this provider does not support vision]\n")
		case "tool_use":
			args := json.RawMessage(b.ToolInput)
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			toolCalls = append(toolCalls, apiToolCall{
				ID:   b.ToolUseID,
				Type: "function",
				Function: apiToolCallFun{
					Name:      b.ToolName,
					Arguments: args,
				},
			})
		case "tool_result":
			toolResults = append(toolResults, b)
		default:
			return nil, fmt.Errorf("ollama: unknown content kind %q", b.Kind)
		}
	}

	var out []apiMsg

	// Tool results fan out into their own role=tool wire messages,
	// regardless of the canonical message's nominal role (carlos sends
	// them on a role=user envelope per Anthropic's protocol).
	for _, r := range toolResults {
		out = append(out, apiMsg{
			Role:       "tool",
			Content:    string(r.ToolResult),
			ToolCallID: r.ToolUseID,
		})
	}

	// Text + tool_calls collapse into a single wire message for the
	// originating role. Skip emission entirely if there's nothing to
	// say AND we already fanned out tool_results - sending an empty
	// assistant message would just confuse the server.
	hasText := len(textParts) > 0
	hasCalls := len(toolCalls) > 0
	if hasText || hasCalls {
		content := ""
		if hasText {
			content = concat(textParts)
		}
		out = append(out, apiMsg{
			Role:      m.Role,
			Content:   content,
			ToolCalls: toolCalls,
		})
	} else if len(out) == 0 {
		// Pathological: empty message. Preserve the role envelope with
		// empty content so the server sees the turn.
		out = append(out, apiMsg{Role: m.Role, Content: ""})
	}

	return out, nil
}

// concat joins text parts with no separator. Anthropic's text blocks are
// adjacent fragments of the same assistant utterance, so a separator
// would inject artificial whitespace.
func concat(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	buf := make([]byte, 0, n)
	for _, p := range parts {
		buf = append(buf, p...)
	}
	return string(buf)
}

// streamChunk is one parsed line of the JSONL response stream. Ollama
// emits one such object per newline-terminated line.
//
// Final chunk: done=true, done_reason populated, plus aggregate stats
// (total_duration, eval_count, etc.) that we don't currently surface.
type streamChunk struct {
	Model      string         `json:"model"`
	CreatedAt  string         `json:"created_at"`
	Message    streamChunkMsg `json:"message"`
	Done       bool           `json:"done"`
	DoneReason string         `json:"done_reason,omitempty"`
	// Server-side error on non-200 paths: Ollama uses a top-level
	// {"error": "..."} object. We type it as a string here so the
	// scanner can surface mid-stream errors without a separate shape.
	Error string `json:"error,omitempty"`
}

type streamChunkMsg struct {
	Role      string             `json:"role"`
	Content   string             `json:"content"`
	ToolCalls []streamChunkTCall `json:"tool_calls,omitempty"`
}

type streamChunkTCall struct {
	Function streamChunkTFun `json:"function"`
}

type streamChunkTFun struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}
