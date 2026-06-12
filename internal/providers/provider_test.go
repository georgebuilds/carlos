package providers

import (
	"bytes"
	"testing"
)

// TestImageBlock pins the constructor's field mapping - adapters
// branch on exactly these three fields.
func TestImageBlock(t *testing.T) {
	data := []byte{0x89, 'P', 'N', 'G'}
	b := ImageBlock("image/png", data)
	if b.Kind != "image" {
		t.Errorf("Kind = %q, want image", b.Kind)
	}
	if b.MediaType != "image/png" {
		t.Errorf("MediaType = %q, want image/png", b.MediaType)
	}
	if !bytes.Equal(b.ImageData, data) {
		t.Errorf("ImageData = %v, want %v", b.ImageData, data)
	}
	if b.Text != "" || b.ToolUseID != "" || b.ToolName != "" {
		t.Errorf("non-image fields must stay zero: %+v", b)
	}
}
