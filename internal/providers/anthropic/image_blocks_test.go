package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/providers"
)

// TestToAPIBlocks_ImageBecomesBase64Source: an image block maps to the
// native {type:image, source:{type:base64, media_type, data}} shape,
// with the bytes std-base64 encoded and block order preserved.
func TestToAPIBlocks_ImageBecomesBase64Source(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}
	blocks, err := toAPIBlocks([]providers.Block{
		{Kind: "text", Text: "what is this?"},
		providers.ImageBlock("image/png", png),
		{Kind: "text", Text: "thanks"},
	})
	if err != nil {
		t.Fatalf("toAPIBlocks: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("want 3 blocks, got %d", len(blocks))
	}
	if blocks[0].Type != "text" || blocks[2].Type != "text" {
		t.Errorf("text blocks displaced: %+v", blocks)
	}
	img := blocks[1]
	if img.Type != "image" {
		t.Fatalf("blocks[1].Type = %q, want image", img.Type)
	}
	if img.Source == nil {
		t.Fatal("image block has nil source")
	}
	if img.Source.Type != "base64" || img.Source.MediaType != "image/png" {
		t.Errorf("source envelope wrong: %+v", img.Source)
	}
	if img.Source.Data != "iVBORw0KGgo=" {
		t.Errorf("base64 data = %q, want iVBORw0KGgo=", img.Source.Data)
	}
}

// TestToAPIBlocks_ImageWireJSON pins the marshaled shape of an image
// block - the Messages API rejects shape drift server-side, so catch
// it here first.
func TestToAPIBlocks_ImageWireJSON(t *testing.T) {
	blocks, err := toAPIBlocks([]providers.Block{
		providers.ImageBlock("image/jpeg", []byte{0xFF, 0xD8}),
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(blocks[0])
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"/9g="}}`
	if string(b) != want {
		t.Errorf("image block wire JSON:\n got %s\nwant %s", b, want)
	}
}

// TestToAPIBlocks_ImageValidation: empty data or missing media type is
// a caller bug surfaced as an error (the API would 400 anyway; fail
// fast with a message that names the problem).
func TestToAPIBlocks_ImageValidation(t *testing.T) {
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
			_, err := toAPIBlocks([]providers.Block{tc.blk})
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should mention %q", err, tc.want)
			}
		})
	}
}

// TestBuildRequest_ImageInUserMessage: the full request path threads
// image blocks through (regression for the old reject-unknown-kind
// behavior swallowing them).
func TestBuildRequest_ImageInUserMessage(t *testing.T) {
	req := providers.Request{
		Model: "claude-test",
		Messages: []providers.Message{{
			Role: "user",
			Content: []providers.Block{
				{Kind: "text", Text: "see image"},
				providers.ImageBlock("image/png", []byte{1, 2, 3}),
			},
		}},
	}
	out, err := buildRequest(req)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if len(out.Messages) != 1 || len(out.Messages[0].Content) != 2 {
		t.Fatalf("unexpected shape: %+v", out.Messages)
	}
	if out.Messages[0].Content[1].Type != "image" {
		t.Errorf("second block should be image: %+v", out.Messages[0].Content[1])
	}
}
