package anthropic

import (
	"encoding/json"
	"fmt"

	"github.com/georgebuilds/carlos/internal/providers"
)

// API request/response shapes for the Anthropic Messages API. Only the
// fields carlos actually uses are typed here; everything else is dropped
// at parse time. Wire-format docs:
// https://docs.anthropic.com/en/api/messages
//
// Versioning: we set anthropic-version: 2023-06-01 (the long-stable Messages
// API release). Streaming uses server-sent events; see sse.go.

const apiVersion = "2023-06-01"

// messagesRequest is the request body for POST /v1/messages.
type messagesRequest struct {
	Model     string      `json:"model"`
	MaxTokens int         `json:"max_tokens"`
	System    string      `json:"system,omitempty"`
	Messages  []apiMsg    `json:"messages"`
	Tools     []apiTool   `json:"tools,omitempty"`
	Stream    bool        `json:"stream"`
}

// apiMsg is the on-the-wire shape for a single message in the messages
// array. Anthropic supports string content (shortcut) or an array of
// typed content blocks; we always send the typed-array form so tool_use
// and tool_result round-trips work uniformly.
type apiMsg struct {
	Role    string     `json:"role"`
	Content []apiBlock `json:"content"`
}

// apiBlock is one content block. Type discriminates on:
//
//	text        — {type: text, text: "..."}
//	tool_use    — {type: tool_use, id, name, input}
//	tool_result — {type: tool_result, tool_use_id, content}
type apiBlock struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	ID         string          `json:"id,omitempty"`          // tool_use
	Name       string          `json:"name,omitempty"`        // tool_use
	Input      json.RawMessage `json:"input,omitempty"`       // tool_use
	ToolUseID  string          `json:"tool_use_id,omitempty"` // tool_result
	Content    string          `json:"content,omitempty"`     // tool_result body
}

type apiTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// buildRequest converts the canonical providers.Request to the Anthropic
// wire shape. MaxTokens defaults to 4096 if the caller didn't set one;
// the API requires a positive value.
func buildRequest(req providers.Request) (*messagesRequest, error) {
	out := &messagesRequest{
		Model:     req.Model,
		MaxTokens: 4096,
		System:    req.System,
		Stream:    true,
	}
	for _, m := range req.Messages {
		blocks, err := toAPIBlocks(m.Content)
		if err != nil {
			return nil, fmt.Errorf("anthropic: message role=%s: %w", m.Role, err)
		}
		out.Messages = append(out.Messages, apiMsg{Role: m.Role, Content: blocks})
	}
	for _, t := range req.Tools {
		schema := t.Schema
		if len(schema) == 0 {
			schema = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out.Tools = append(out.Tools, apiTool{
			Name: t.Name, Description: t.Description, InputSchema: schema,
		})
	}
	return out, nil
}

// toAPIBlocks converts canonical providers.Block slices to wire-format
// apiBlock. Unknown Kind values are rejected — silently dropping them
// would produce confusing model behavior.
func toAPIBlocks(in []providers.Block) ([]apiBlock, error) {
	out := make([]apiBlock, 0, len(in))
	for _, b := range in {
		switch b.Kind {
		case "text", "":
			out = append(out, apiBlock{Type: "text", Text: b.Text})
		case "tool_use":
			input := json.RawMessage(b.ToolInput)
			if len(input) == 0 {
				input = json.RawMessage(`{}`)
			}
			out = append(out, apiBlock{
				Type: "tool_use", ID: b.ToolUseID, Name: b.ToolName, Input: input,
			})
		case "tool_result":
			out = append(out, apiBlock{
				Type: "tool_result", ToolUseID: b.ToolUseID, Content: string(b.ToolResult),
			})
		default:
			return nil, fmt.Errorf("anthropic: unknown content kind %q", b.Kind)
		}
	}
	return out, nil
}

// streamEvent is the union of SSE event payloads we care about, decoded
// from the `data:` line JSON. Only typed fields are pulled out; the rest
// is left as raw JSON for forward-compat (Anthropic may add fields).
type streamEvent struct {
	Type         string          `json:"type"`
	Index        int             `json:"index,omitempty"`
	ContentBlock *streamCB       `json:"content_block,omitempty"`
	Delta        *streamDelta    `json:"delta,omitempty"`
	Message      json.RawMessage `json:"message,omitempty"`
	Usage        json.RawMessage `json:"usage,omitempty"`
	Error        *streamError    `json:"error,omitempty"`
}

type streamCB struct {
	Type  string          `json:"type"`
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Text  string          `json:"text,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
}

type streamDelta struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	PartialJSON string `json:"partial_json,omitempty"`
	StopReason  string `json:"stop_reason,omitempty"`
}

type streamError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}
