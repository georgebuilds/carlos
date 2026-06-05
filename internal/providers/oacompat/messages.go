package oacompat

import "encoding/json"

// API request/response shapes for the OpenAI Chat Completions API
// (POST .../chat/completions with stream:true). Only the fields carlos
// actually uses are typed here; everything else is dropped at parse time.
//
// Wire-format docs: https://platform.openai.com/docs/api-reference/chat
//
// Why Chat Completions and not Responses / Assistants:
//   - Chat Completions is the broadly-supported, well-trodden path served
//     cleanly by every current OpenAI model (gpt-4o, gpt-4o-mini, gpt-4-turbo,
//     o1-mini, o1, o3-mini, ...) AND by every OpenAI-compatible proxy
//     (OpenRouter, vLLM, Together, llama.cpp, ...).
//   - The Responses API is newer and scoped narrower; the Assistants API is
//     being deprecated. We commit to Chat Completions and leave Responses
//     as a future opt-in if a feature ever requires it.

// MessagesRequest is the request body for POST .../chat/completions.
type MessagesRequest struct {
	Model    string    `json:"model"`
	Messages []APIMsg  `json:"messages"`
	Tools    []APITool `json:"tools,omitempty"`
	Stream   bool      `json:"stream"`
}

// APIMsg is one Chat Completions message. The shape differs subtly by role:
//
//	system    — {role: system, content: "..."}
//	user      — {role: user, content: "..." OR [{type:text|image_url, ...}]}
//	assistant — {role: assistant, content: "..."?, tool_calls: [...]?}
//	tool      — {role: tool, tool_call_id: "...", content: "..."}
//
// We always send content as a plain string today; multipart content (images)
// is a separate slice. Assistant turns with tool_calls send content=""
// alongside the tool_calls array; that's the Chat Completions convention.
type APIMsg struct {
	Role       string        `json:"role"`
	Content    string        `json:"content,omitempty"`
	ToolCalls  []APIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Name       string        `json:"name,omitempty"`
}

// APIToolCall mirrors the Chat Completions assistant tool_call shape on
// REQUEST (history replay) and on RESPONSE (assembled deltas). The streamed
// form (APIStreamToolCall) is similar but with all fields optional and an
// extra Index to disambiguate parallel calls.
type APIToolCall struct {
	ID       string      `json:"id"`
	Type     string      `json:"type"` // always "function" today
	Function APIFunction `json:"function"`
}

// APIFunction is the function-call payload inside an APIToolCall. Arguments
// is a JSON-encoded STRING (not an object), per OpenAI's schema.
type APIFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// APITool is one entry in the tools array. The function.parameters field
// receives the canonical providers.ToolSpec.Schema verbatim — the schema
// in our interface IS the JSON Schema.
type APITool struct {
	Type     string         `json:"type"` // always "function"
	Function APIToolFnDecl  `json:"function"`
}

// APIToolFnDecl is the function-declaration payload inside an APITool.
type APIToolFnDecl struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

// StreamChunk is the JSON payload of one `data: {...}` SSE frame from
// Chat Completions streaming. Only the fields we read are typed.
//
// OpenAI sends one chunk per token-ish batch. Each chunk's choices[i].delta
// holds the *additions* since the previous chunk; the consumer accumulates
// content and tool_call argument fragments by index.
type StreamChunk struct {
	ID      string         `json:"id,omitempty"`
	Object  string         `json:"object,omitempty"`
	Model   string         `json:"model,omitempty"`
	Choices []StreamChoice `json:"choices,omitempty"`
	// Error is populated when the server surfaces a streaming error frame
	// rather than tearing down the connection. Some compatible servers
	// (vLLM, certain gateways, OpenRouter when an upstream provider fails
	// mid-stream) use this; OpenAI itself usually just 200s then sends an
	// `error` event. We honor both.
	Error *StreamError `json:"error,omitempty"`
}

// StreamChoice is one per-choice envelope inside a StreamChunk.
type StreamChoice struct {
	Index        int          `json:"index"`
	Delta        *StreamDelta `json:"delta,omitempty"`
	FinishReason string       `json:"finish_reason,omitempty"`
}

// StreamDelta is the per-chunk delta payload. Text deltas come in Content;
// tool-call deltas come in ToolCalls (one entry per parallel tool_call,
// keyed by .Index).
type StreamDelta struct {
	Role      string              `json:"role,omitempty"`
	Content   string              `json:"content,omitempty"`
	ToolCalls []APIStreamToolCall `json:"tool_calls,omitempty"`
}

// APIStreamToolCall is the per-chunk delta shape. Differs from APIToolCall:
//   - Index is required to identify WHICH parallel tool_call this delta
//     belongs to (the array is not order-preserving across chunks).
//   - All other fields are optional; the first chunk for an index typically
//     carries id+type+function.name; subsequent chunks carry only
//     function.arguments fragments.
type APIStreamToolCall struct {
	Index    int                `json:"index"`
	ID       string             `json:"id,omitempty"`
	Type     string             `json:"type,omitempty"`
	Function *APIStreamFunction `json:"function,omitempty"`
}

// APIStreamFunction is the per-chunk function-delta payload.
type APIStreamFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

// StreamError mirrors OpenAI's error envelope. OpenRouter sometimes returns
// errors mid-stream rather than as an HTTP non-2xx (e.g., the upstream
// provider rejected the request after OpenRouter accepted it); we forward
// these as EventError.
type StreamError struct {
	Type    string `json:"type,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
}
