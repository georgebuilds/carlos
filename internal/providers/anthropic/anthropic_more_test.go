package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/providers"
)

func TestName_ReturnsAnthropic(t *testing.T) {
	c := New("k")
	if got := c.Name(); got != "anthropic" {
		t.Errorf("Name=%q want anthropic", got)
	}
}

func TestCapabilities_AdvertisesAllFirstClassFlags(t *testing.T) {
	c := New("k")
	caps := c.Capabilities()
	if !caps.ParallelToolUse {
		t.Error("ParallelToolUse should be true")
	}
	if !caps.PromptCaching {
		t.Error("PromptCaching should be true")
	}
	if !caps.StructuredOut {
		t.Error("StructuredOut should be true")
	}
	if !caps.Vision {
		t.Error("Vision should be true")
	}
}

func TestToAPIBlocks_EmptyKindTreatedAsText(t *testing.T) {
	out, err := toAPIBlocks([]providers.Block{{Kind: "", Text: "hi"}})
	if err != nil {
		t.Fatalf("toAPIBlocks: %v", err)
	}
	if len(out) != 1 || out[0].Type != "text" || out[0].Text != "hi" {
		t.Errorf("got %+v, want one text block 'hi'", out)
	}
}

func TestToAPIBlocks_ToolUseEmptyInputBackfilled(t *testing.T) {
	out, err := toAPIBlocks([]providers.Block{{
		Kind: "tool_use", ToolUseID: "tu1", ToolName: "bash",
	}})
	if err != nil {
		t.Fatalf("toAPIBlocks: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("got %d blocks", len(out))
	}
	if string(out[0].Input) != "{}" {
		t.Errorf("Input=%q, want backfilled {}", string(out[0].Input))
	}
}

func TestToAPIBlocks_ToolUseWithInputPassesThrough(t *testing.T) {
	in := json.RawMessage(`{"cmd":"ls"}`)
	out, err := toAPIBlocks([]providers.Block{{
		Kind: "tool_use", ToolUseID: "tu1", ToolName: "bash",
		ToolInput: []byte(in),
	}})
	if err != nil {
		t.Fatalf("toAPIBlocks: %v", err)
	}
	if string(out[0].Input) != string(in) {
		t.Errorf("Input=%q want %q", string(out[0].Input), string(in))
	}
}

func TestToAPIBlocks_ToolResultRendersContent(t *testing.T) {
	out, err := toAPIBlocks([]providers.Block{{
		Kind: "tool_result", ToolUseID: "tu1", ToolResult: []byte("ok\n"),
	}})
	if err != nil {
		t.Fatalf("toAPIBlocks: %v", err)
	}
	if out[0].ToolUseID != "tu1" || out[0].Content != "ok\n" {
		t.Errorf("got %+v", out[0])
	}
}

func TestToAPIBlocks_UnknownKindReturnsError(t *testing.T) {
	_, err := toAPIBlocks([]providers.Block{{Kind: "image_url"}})
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
	if !strings.Contains(err.Error(), "unknown content kind") {
		t.Errorf("err=%q should mention unknown content kind", err)
	}
}

func TestBuildRequest_DefaultsMaxTokens(t *testing.T) {
	r, err := buildRequest(providers.Request{Model: "claude-x"})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if r.MaxTokens != 4096 {
		t.Errorf("MaxTokens=%d want 4096", r.MaxTokens)
	}
	if !r.Stream {
		t.Error("Stream should default to true")
	}
}

func TestBuildRequest_PinsSystemAndMessages(t *testing.T) {
	r, err := buildRequest(providers.Request{
		Model:  "claude-x",
		System: "you are carlos",
		Messages: []providers.Message{
			{Role: "user", Content: []providers.Block{{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if r.System != "you are carlos" {
		t.Errorf("System=%q want pinned through", r.System)
	}
	if len(r.Messages) != 1 || r.Messages[0].Role != "user" {
		t.Errorf("Messages=%+v", r.Messages)
	}
}

func TestBuildRequest_BadBlockKindWrapped(t *testing.T) {
	_, err := buildRequest(providers.Request{
		Messages: []providers.Message{
			{Role: "user", Content: []providers.Block{{Kind: "bogus"}}},
		},
	})
	if err == nil {
		t.Fatal("expected error from buildRequest")
	}
	if !strings.Contains(err.Error(), "role=user") {
		t.Errorf("err=%q should mention role context", err)
	}
}

func TestBuildRequest_ToolsBackfillEmptySchema(t *testing.T) {
	r, err := buildRequest(providers.Request{
		Tools: []providers.ToolSpec{{Name: "bash", Description: "run a command"}},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if len(r.Tools) != 1 {
		t.Fatalf("Tools=%+v", r.Tools)
	}
	if got := string(r.Tools[0].InputSchema); !strings.Contains(got, `"type":"object"`) {
		t.Errorf("InputSchema=%q want object backfill", got)
	}
}

func TestBuildRequest_ToolsPassThroughSchema(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"cmd":{"type":"string"}}}`)
	r, err := buildRequest(providers.Request{
		Tools: []providers.ToolSpec{{Name: "bash", Description: "x", Schema: schema}},
	})
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if string(r.Tools[0].InputSchema) != string(schema) {
		t.Errorf("schema not preserved")
	}
}
