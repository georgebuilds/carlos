package agent

import (
	"strings"
	"testing"
)

// TestExpandMarkerSegments_NoMarkers: a chip-less message comes back
// as exactly one verbatim text segment.
func TestExpandMarkerSegments_NoMarkers(t *testing.T) {
	segs := ExpandMarkerSegments("plain text, no chips", nil)
	if len(segs) != 1 || segs[0].Image != nil || segs[0].Text != "plain text, no chips" {
		t.Fatalf("got %+v, want single verbatim text segment", segs)
	}
}

// TestExpandMarkerSegments_ImageSplitsInOrder: text around image
// markers splits into text/image/text segments in marker order, and
// the image segments carry the referenced attachments.
func TestExpandMarkerSegments_ImageSplitsInOrder(t *testing.T) {
	atts := []Attachment{
		{ID: "a1", Kind: AttachmentImage, Nickname: "one.png", SHA256: "s1"},
		{ID: "b2", Kind: AttachmentImage, Nickname: "two.png", SHA256: "s2"},
	}
	segs := ExpandMarkerSegments("first ‹i:a1› middle ‹i:b2› last", atts)
	if len(segs) != 5 {
		t.Fatalf("got %d segments (%+v), want 5", len(segs), segs)
	}
	wantText := map[int]string{0: "first ", 2: " middle ", 4: " last"}
	for i, want := range wantText {
		if segs[i].Image != nil || segs[i].Text != want {
			t.Errorf("segs[%d] = %+v, want text %q", i, segs[i], want)
		}
	}
	if segs[1].Image == nil || segs[1].Image.ID != "a1" || segs[1].Image.SHA256 != "s1" {
		t.Errorf("segs[1] = %+v, want image a1", segs[1])
	}
	if segs[3].Image == nil || segs[3].Image.ID != "b2" {
		t.Errorf("segs[3] = %+v, want image b2", segs[3])
	}
}

// TestExpandMarkerSegments_ImageOnly: a message that is just one image
// marker yields exactly one image segment - no empty text bookends.
func TestExpandMarkerSegments_ImageOnly(t *testing.T) {
	segs := ExpandMarkerSegments("‹i:x›", []Attachment{
		{ID: "x", Kind: AttachmentImage, SHA256: "deadbeef"},
	})
	if len(segs) != 1 || segs[0].Image == nil || segs[0].Image.ID != "x" {
		t.Fatalf("got %+v, want exactly one image segment", segs)
	}
}

// TestExpandMarkerSegments_TextHalvesMatchExpandMarkers: pastes,
// mentions, and dangling markers expand to the SAME text bytes
// ExpandMarkers produces - the shared renderer is the contract.
func TestExpandMarkerSegments_TextHalvesMatchExpandMarkers(t *testing.T) {
	atts := []Attachment{
		{ID: "1", Kind: AttachmentPaste, Nickname: "logs", Content: "ERR line"},
		{ID: "2", Kind: AttachmentMention, Nickname: "loop.go", Path: "internal/agent/loop.go"},
	}
	in := "see ‹p:1› and ‹m:2› plus dangling ‹p:9z› end"
	segs := ExpandMarkerSegments(in, atts)
	if len(segs) != 1 || segs[0].Image != nil {
		t.Fatalf("non-image markers must stay one text segment; got %+v", segs)
	}
	if want := ExpandMarkers(in, atts); segs[0].Text != want {
		t.Errorf("segment text diverged from ExpandMarkers:\n got %q\nwant %q", segs[0].Text, want)
	}
}

// TestExpandMarkerSegments_DanglingImageMarkerStaysText: an image
// marker with NO matching attachment cannot become an image segment -
// it degrades to the deterministic unavailable placeholder, in text.
func TestExpandMarkerSegments_DanglingImageMarkerStaysText(t *testing.T) {
	segs := ExpandMarkerSegments("look ‹i:gone› here", nil)
	if len(segs) != 1 || segs[0].Image != nil {
		t.Fatalf("dangling image marker produced an image segment: %+v", segs)
	}
	if !strings.Contains(segs[0].Text, "[attachment gone unavailable]") {
		t.Errorf("missing unavailable placeholder: %q", segs[0].Text)
	}
}

// TestExpandMarkerSegments_NoRawMarkerSurvives: across every kind plus
// dangling markers, no text segment may retain a raw marker.
func TestExpandMarkerSegments_NoRawMarkerSurvives(t *testing.T) {
	atts := []Attachment{
		{ID: "1", Kind: AttachmentPaste, Content: "paste body"},
		{ID: "2", Kind: AttachmentImage, Nickname: "shot.png", SHA256: "abc"},
		{ID: "3", Kind: AttachmentMention, Path: "x.go"},
	}
	segs := ExpandMarkerSegments("‹p:1› a ‹i:2› b ‹m:3› c ‹i:nope›", atts)
	for i, s := range segs {
		if ContainsMarker(s.Text) {
			t.Errorf("RAW MARKER LEAKED in segment %d: %q", i, s.Text)
		}
	}
}

// TestImagePlaceholder: the exported placeholder follows the label
// fallback chain (nickname > path > id).
func TestImagePlaceholder(t *testing.T) {
	cases := []struct {
		a    Attachment
		want string
	}{
		{Attachment{ID: "i", Nickname: "nick.png", Path: "/p.png"}, "[image: nick.png]"},
		{Attachment{ID: "i", Path: "/p.png"}, "[image: /p.png]"},
		{Attachment{ID: "i"}, "[image: i]"},
	}
	for _, tc := range cases {
		if got := ImagePlaceholder(tc.a); got != tc.want {
			t.Errorf("ImagePlaceholder(%+v) = %q, want %q", tc.a, got, tc.want)
		}
	}
}
