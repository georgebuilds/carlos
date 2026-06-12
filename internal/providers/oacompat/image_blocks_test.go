package oacompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/providers"
)

// ---------------------------------------------------------------------------
// Image content-parts (slice I-3)
// ---------------------------------------------------------------------------

// TestBuildRequest_ImageSwitchesToContentParts: a user message carrying
// an image block serializes content as a parts ARRAY, with text and
// image_url parts interleaved in canonical block order and the bytes
// embedded as a data: URL.
func TestBuildRequest_ImageSwitchesToContentParts(t *testing.T) {
	req := providers.Request{
		Messages: []providers.Message{{
			Role: "user",
			Content: []providers.Block{
				{Kind: "text", Text: "before"},
				providers.ImageBlock("image/png", []byte{0x89, 'P', 'N', 'G'}),
				{Kind: "text", Text: "after"},
				providers.ImageBlock("image/jpeg", []byte{0xFF, 0xD8}),
			},
		}},
	}
	out, err := BuildRequest(req, "test")
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("want 1 wire msg, got %d", len(out.Messages))
	}
	var parts []APIContentPart
	if err := json.Unmarshal(out.Messages[0].Content, &parts); err != nil {
		t.Fatalf("content is not a parts array: %v (raw: %s)", err, out.Messages[0].Content)
	}
	if len(parts) != 4 {
		t.Fatalf("want 4 parts, got %d: %+v", len(parts), parts)
	}
	wantTypes := []string{"text", "image_url", "text", "image_url"}
	for i, w := range wantTypes {
		if parts[i].Type != w {
			t.Errorf("parts[%d].Type = %q, want %q (order must follow block order)", i, parts[i].Type, w)
		}
	}
	if parts[0].Text != "before" || parts[2].Text != "after" {
		t.Errorf("text parts wrong: %+v", parts)
	}
	if got, want := parts[1].ImageURL.URL, "data:image/png;base64,iVBORw=="; got != want {
		t.Errorf("png data URL = %q, want %q", got, want)
	}
	if got, want := parts[3].ImageURL.URL, "data:image/jpeg;base64,/9g="; got != want {
		t.Errorf("jpeg data URL = %q, want %q", got, want)
	}
}

// TestBuildRequest_ImageOnlyMessage: an image with no accompanying text
// still produces a valid single-part array message.
func TestBuildRequest_ImageOnlyMessage(t *testing.T) {
	req := providers.Request{
		Messages: []providers.Message{{
			Role:    "user",
			Content: []providers.Block{providers.ImageBlock("image/png", []byte{1})},
		}},
	}
	out, err := BuildRequest(req, "test")
	if err != nil {
		t.Fatal(err)
	}
	var parts []APIContentPart
	if err := json.Unmarshal(out.Messages[0].Content, &parts); err != nil {
		t.Fatalf("content: %v", err)
	}
	if len(parts) != 1 || parts[0].Type != "image_url" {
		t.Errorf("want one image_url part, got %+v", parts)
	}
}

// TestBuildRequest_ImageValidation: missing data / media type errors
// name the problem and the provider prefix.
func TestBuildRequest_ImageValidation(t *testing.T) {
	cases := []struct {
		name string
		blk  providers.Block
		want string
	}{
		{"no data", providers.Block{Kind: "image", MediaType: "image/png"}, "no data"},
		{"no media type", providers.Block{Kind: "image", ImageData: []byte{1}}, "no media type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildRequest(providers.Request{
				Messages: []providers.Message{{Role: "user", Content: []providers.Block{tc.blk}}},
			}, "myprov")
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "myprov") {
				t.Errorf("error %q should mention %q and the provider prefix", err, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Text-only wire-format byte compatibility (load-bearing)
// ---------------------------------------------------------------------------

// legacy* mirror the EXACT pre-image wire structs (Content was a plain
// `string` with omitempty). Marshaling them reproduces the old bytes
// mechanically; the regression below asserts the new RawMessage-based
// encoding is byte-identical for every text-only shape. If this test
// fails, the on-the-wire request format changed for existing users -
// do not "fix" the expectation without understanding why.
type legacyRequest struct {
	Model    string      `json:"model"`
	Messages []legacyMsg `json:"messages"`
	Tools    []APITool   `json:"tools,omitempty"`
	Stream   bool        `json:"stream"`
}

type legacyMsg struct {
	Role       string        `json:"role"`
	Content    string        `json:"content,omitempty"`
	ToolCalls  []APIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string        `json:"tool_call_id,omitempty"`
	Name       string        `json:"name,omitempty"`
}

// TestBuildRequest_TextOnlyWireFormatUnchanged is the byte-compat pin:
// a representative text-only conversation (system + user + assistant
// with tool_calls + tool results + special characters that exercise
// encoding/json's escaping) must marshal byte-for-byte identically to
// the pre-image `Content string` wire format.
func TestBuildRequest_TextOnlyWireFormatUnchanged(t *testing.T) {
	req := providers.Request{
		Model:  "gpt-test",
		System: "You are <carlos> & you say \"hi\"\nwith newlines",
		Messages: []providers.Message{
			{Role: "user", Content: []providers.Block{{Kind: "text", Text: "run `ls` & exit <now> — ünïcödé 🧢"}}},
			{Role: "assistant", Content: []providers.Block{
				{Kind: "text", Text: "ok"},
				{Kind: "tool_use", ToolUseID: "tu-1", ToolName: "bash", ToolInput: []byte(`{"cmd":"ls"}`)},
			}},
			{Role: "user", Content: []providers.Block{
				{Kind: "tool_result", ToolUseID: "tu-1", ToolResult: []byte("file1\nfile2\n")},
			}},
			// Assistant turn with tool_calls only (content key must stay
			// omitted, not become "" or null).
			{Role: "assistant", Content: []providers.Block{
				{Kind: "tool_use", ToolUseID: "tu-2", ToolName: "noop"},
			}},
			// Empty message fallback (role-only envelope).
			{Role: "user", Content: nil},
			// Multiple text blocks joined with double newline.
			{Role: "user", Content: []providers.Block{
				{Kind: "text", Text: "para1"},
				{Kind: "text", Text: "para2"},
			}},
		},
		Tools: []providers.ToolSpec{{
			Name: "bash", Description: "run a command",
			Schema: []byte(`{"type":"object","properties":{"cmd":{"type":"string"}}}`),
		}},
	}

	out, err := BuildRequest(req, "test")
	if err != nil {
		t.Fatalf("BuildRequest: %v", err)
	}
	got, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal new request: %v", err)
	}

	legacy := legacyRequest{
		Model:  "gpt-test",
		Stream: true,
		Messages: []legacyMsg{
			{Role: "system", Content: "You are <carlos> & you say \"hi\"\nwith newlines"},
			{Role: "user", Content: "run `ls` & exit <now> — ünïcödé 🧢"},
			{Role: "assistant", Content: "ok", ToolCalls: []APIToolCall{{
				ID: "tu-1", Type: "function",
				Function: APIFunction{Name: "bash", Arguments: `{"cmd":"ls"}`},
			}}},
			{Role: "tool", ToolCallID: "tu-1", Content: "file1\nfile2\n"},
			{Role: "assistant", ToolCalls: []APIToolCall{{
				ID: "tu-2", Type: "function",
				Function: APIFunction{Name: "noop", Arguments: "{}"},
			}}},
			{Role: "user"},
			{Role: "user", Content: "para1\n\npara2"},
		},
		Tools: out.Tools, // tool encoding unchanged; reuse to isolate Content
	}
	want, err := json.Marshal(&legacy)
	if err != nil {
		t.Fatalf("marshal legacy request: %v", err)
	}

	if string(got) != string(want) {
		t.Errorf("text-only wire bytes CHANGED:\n got: %s\nwant: %s", got, want)
	}
}

// TestBuildRequest_TextOnlyWireLiteral pins one human-readable literal
// so a drift in BOTH the new and legacy mirrors (e.g. a tag edit
// applied to each) still breaks loudly.
func TestBuildRequest_TextOnlyWireLiteral(t *testing.T) {
	out, err := BuildRequest(providers.Request{
		Model:  "m",
		System: "sys",
		Messages: []providers.Message{
			{Role: "user", Content: []providers.Block{{Kind: "text", Text: "hi <there>"}}},
		},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	// encoding/json HTML-escapes < and > inside strings; the old
	// `Content string` field did the same, so the escapes are part of
	// the pinned format.
	want := `{"model":"m","messages":[{"role":"system","content":"sys"},{"role":"user","content":"hi \u003cthere\u003e"}],"stream":true}`
	if string(got) != want {
		t.Errorf("wire literal:\n got: %s\nwant: %s", got, want)
	}
}
