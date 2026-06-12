package chatglue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
)

// visionStub is a providers.Provider whose only job is to answer the
// Capabilities().Vision question; buildHistory never calls Stream.
type visionStub struct{ vision bool }

func (v *visionStub) Name() string { return "vision-stub" }
func (v *visionStub) Capabilities() providers.Capabilities {
	return providers.Capabilities{Vision: v.vision}
}
func (v *visionStub) Stream(ctx context.Context, req providers.Request) (<-chan providers.Event, error) {
	ch := make(chan providers.Event)
	close(ch)
	return ch, nil
}

// pngBytes is a minimal payload that http.DetectContentType sniffs as
// image/png (the 8-byte signature is the entire decision input).
var pngBytes = []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x01, 0x02}

// storeArtifact writes content into the test's content-addressed store
// (CARLOS_ARTIFACT_BASE, pinned per-test by openTestLog - so call
// openTestLog FIRST) and returns the sha256 hex key. Mirrors what the
// chat TUI's paste path does via agent.WriteArtifact.
func storeArtifact(t *testing.T, content []byte) string {
	t.Helper()
	base := agent.ArtifactBasePath("")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatalf("mkdir artifact base: %v", err)
	}
	sum := sha256.Sum256(content)
	hexSum := hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(base, hexSum), content, 0o600); err != nil {
		t.Fatalf("write artifact blob: %v", err)
	}
	return hexSum
}

// TestBuildHistory_ImageMarkersBecomeImageBlocks: with a vision
// provider and a stored blob, an ‹i:…› marker splits the user message
// into text + image + text blocks in marker order, the image block
// carrying the artifact bytes and a sniffed media type.
func TestBuildHistory_ImageMarkersBecomeImageBlocks(t *testing.T) {
	log := openTestLog(t)
	sha := storeArtifact(t, pngBytes)
	const id = "agent-img-1"
	seedAgent(t, log, id)
	appendUserMessageWithAtts(t, log, id, "what is ‹i:1› about?", []agent.Attachment{
		{ID: "1", Kind: agent.AttachmentImage, Nickname: "shot.png", SHA256: sha},
	})

	l := NewLoop(Config{Provider: &visionStub{vision: true}}, log, newMemSource(), id)
	history, err := l.buildHistory(context.Background())
	if err != nil {
		t.Fatalf("buildHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1", len(history))
	}
	blks := history[0].Content
	if len(blks) != 3 {
		t.Fatalf("blocks = %d (%+v), want 3 (text, image, text)", len(blks), blks)
	}
	if blks[0].Kind != "text" || blks[0].Text != "what is " {
		t.Errorf("blks[0] = %+v, want leading text", blks[0])
	}
	if blks[1].Kind != "image" {
		t.Fatalf("blks[1].Kind = %q, want image", blks[1].Kind)
	}
	if blks[1].MediaType != "image/png" {
		t.Errorf("media type = %q, want image/png", blks[1].MediaType)
	}
	if !bytes.Equal(blks[1].ImageData, pngBytes) {
		t.Errorf("image bytes did not round-trip from the artifact store")
	}
	if blks[2].Kind != "text" || blks[2].Text != " about?" {
		t.Errorf("blks[2] = %+v, want trailing text", blks[2])
	}
}

// TestBuildHistory_MultipleImagesPreserveMarkerOrder: two image chips
// interleave with text exactly as typed.
func TestBuildHistory_MultipleImagesPreserveMarkerOrder(t *testing.T) {
	log := openTestLog(t)
	shaA := storeArtifact(t, pngBytes)
	jpg := append([]byte{0xFF, 0xD8, 0xFF}, bytes.Repeat([]byte{0xEE}, 12)...)
	shaB := storeArtifact(t, jpg)
	const id = "agent-img-2"
	seedAgent(t, log, id)
	appendUserMessageWithAtts(t, log, id, "‹i:a› versus ‹i:b› which?", []agent.Attachment{
		{ID: "a", Kind: agent.AttachmentImage, Nickname: "a.png", SHA256: shaA},
		{ID: "b", Kind: agent.AttachmentImage, Nickname: "b.jpg", SHA256: shaB},
	})

	l := NewLoop(Config{Provider: &visionStub{vision: true}}, log, newMemSource(), id)
	history, err := l.buildHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	blks := history[0].Content
	wantKinds := []string{"image", "text", "image", "text"}
	if len(blks) != len(wantKinds) {
		t.Fatalf("blocks = %d (%+v), want %d", len(blks), blks, len(wantKinds))
	}
	for i, k := range wantKinds {
		if blks[i].Kind != k {
			t.Errorf("blks[%d].Kind = %q, want %q", i, blks[i].Kind, k)
		}
	}
	if blks[0].MediaType != "image/png" || blks[2].MediaType != "image/jpeg" {
		t.Errorf("media types: %q, %q", blks[0].MediaType, blks[2].MediaType)
	}
	if blks[1].Text != " versus " || blks[3].Text != " which?" {
		t.Errorf("text blocks displaced: %+v", blks)
	}
}

// TestBuildHistory_MissingArtifactDegradesToPlaceholder: a SHA that
// points at nothing (pruned blob, never stored) degrades the chip to
// the "(unavailable)" text placeholder instead of failing the turn -
// and still leaks no raw marker.
func TestBuildHistory_MissingArtifactDegradesToPlaceholder(t *testing.T) {
	log := openTestLog(t) // fresh empty artifact store
	const id = "agent-img-3"
	seedAgent(t, log, id)
	appendUserMessageWithAtts(t, log, id, "see ‹i:1› here", []agent.Attachment{
		{ID: "1", Kind: agent.AttachmentImage, Nickname: "gone.png",
			SHA256: "0000000000000000000000000000000000000000000000000000000000000000"},
	})

	l := NewLoop(Config{Provider: &visionStub{vision: true}}, log, newMemSource(), id)
	history, err := l.buildHistory(context.Background())
	if err != nil {
		t.Fatalf("buildHistory must not fail on a missing blob: %v", err)
	}
	assertDegradedToText(t, history[0].Content, "[image: gone.png (unavailable)]")
}

// TestBuildHistory_NoSHADegradesToPlaceholder: an image attachment
// persisted without a SHA (storage failed at paste time) degrades the
// same way.
func TestBuildHistory_NoSHADegradesToPlaceholder(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-img-4"
	seedAgent(t, log, id)
	appendUserMessageWithAtts(t, log, id, "see ‹i:1› here", []agent.Attachment{
		{ID: "1", Kind: agent.AttachmentImage, Nickname: "lost.png"},
	})

	l := NewLoop(Config{Provider: &visionStub{vision: true}}, log, newMemSource(), id)
	history, err := l.buildHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertDegradedToText(t, history[0].Content, "[image: lost.png (unavailable)]")
}

// TestBuildHistory_NonImageBytesDegrade: a blob that doesn't sniff as
// a supported image type (here: plain text) must not be sent as an
// image block.
func TestBuildHistory_NonImageBytesDegrade(t *testing.T) {
	log := openTestLog(t)
	sha := storeArtifact(t, []byte("definitely not pixels"))
	const id = "agent-img-5"
	seedAgent(t, log, id)
	appendUserMessageWithAtts(t, log, id, "see ‹i:1› here", []agent.Attachment{
		{ID: "1", Kind: agent.AttachmentImage, Nickname: "weird.bin", SHA256: sha},
	})

	l := NewLoop(Config{Provider: &visionStub{vision: true}}, log, newMemSource(), id)
	history, err := l.buildHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertDegradedToText(t, history[0].Content, "[image: weird.bin (unavailable)]")
}

// TestBuildHistory_NonVisionProviderStaysSingleTextBlock: pre-I-3
// behavior is pinned for providers without vision - one text block,
// plain "[image: …]" placeholder (no "(unavailable)" suffix: the
// image may be fine, the provider just can't see it).
func TestBuildHistory_NonVisionProviderStaysSingleTextBlock(t *testing.T) {
	log := openTestLog(t)
	sha := storeArtifact(t, pngBytes) // stored AND readable - still text
	const id = "agent-img-6"
	seedAgent(t, log, id)
	appendUserMessageWithAtts(t, log, id, "see ‹i:1› here", []agent.Attachment{
		{ID: "1", Kind: agent.AttachmentImage, Nickname: "shot.png", SHA256: sha},
	})

	l := NewLoop(Config{Provider: &visionStub{vision: false}}, log, newMemSource(), id)
	history, err := l.buildHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	blks := history[0].Content
	if len(blks) != 1 || blks[0].Kind != "text" {
		t.Fatalf("non-vision provider must get one text block; got %+v", blks)
	}
	if blks[0].Text != "see [image: shot.png] here" {
		t.Errorf("placeholder text = %q", blks[0].Text)
	}
}

// TestBuildHistory_MixedChipsWithImage: pastes and mentions expand in
// the text blocks AROUND a real image block, and the no-raw-marker
// invariant holds across every block of the multi-block message.
func TestBuildHistory_MixedChipsWithImage(t *testing.T) {
	log := openTestLog(t)
	sha := storeArtifact(t, pngBytes)
	const id = "agent-img-7"
	seedAgent(t, log, id)
	appendUserMessageWithAtts(t, log, id, "logs ‹p:1› shot ‹i:2› file ‹m:3› dangling ‹i:9z›",
		[]agent.Attachment{
			{ID: "1", Kind: agent.AttachmentPaste, Nickname: "logs", Content: "ERR boom"},
			{ID: "2", Kind: agent.AttachmentImage, Nickname: "shot.png", SHA256: sha},
			{ID: "3", Kind: agent.AttachmentMention, Nickname: "loop.go", Path: "internal/agent/loop.go"},
		})

	l := NewLoop(Config{Provider: &visionStub{vision: true}}, log, newMemSource(), id)
	history, err := l.buildHistory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	blks := history[0].Content
	var imageCount int
	var joined string
	for i, b := range blks {
		if agent.ContainsMarker(b.Text) {
			t.Errorf("RAW MARKER LEAKED in block %d: %q", i, b.Text)
		}
		if b.Kind == "image" {
			imageCount++
			continue
		}
		joined += b.Text
	}
	if imageCount != 1 {
		t.Errorf("image blocks = %d, want 1 (dangling ‹i:9z› must NOT become one)", imageCount)
	}
	for _, want := range []string{"[pasted: logs]", "ERR boom", "@internal/agent/loop.go (mentioned file", "[attachment 9z unavailable]"} {
		if !contains(joined, want) {
			t.Errorf("expanded text missing %q:\n%s", want, joined)
		}
	}
}

// assertDegradedToText asserts a degraded image chip produced text
// blocks only, containing wantPlaceholder, with no marker leakage.
func assertDegradedToText(t *testing.T, blks []providers.Block, wantPlaceholder string) {
	t.Helper()
	var joined string
	for i, b := range blks {
		if b.Kind == "image" {
			t.Fatalf("block %d is an image; expected degraded text only: %+v", i, blks)
		}
		if agent.ContainsMarker(b.Text) {
			t.Errorf("RAW MARKER LEAKED in block %d: %q", i, b.Text)
		}
		joined += b.Text
	}
	if !contains(joined, wantPlaceholder) {
		t.Errorf("placeholder %q missing from %q", wantPlaceholder, joined)
	}
}

func contains(s, sub string) bool { return bytes.Contains([]byte(s), []byte(sub)) }
