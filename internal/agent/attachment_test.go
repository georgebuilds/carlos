package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMarker_RoundTrip pins the marker wire format per kind and walks
// it back through FindMarkers - the serialize/parse pair must agree
// or persisted chips silently degrade to literal text on replay.
func TestMarker_RoundTrip(t *testing.T) {
	cases := []struct {
		kind AttachmentKind
		want string
	}{
		{AttachmentPaste, "‹p:a1›"},
		{AttachmentImage, "‹i:a1›"},
		{AttachmentMention, "‹m:a1›"},
	}
	for _, tc := range cases {
		got := Marker(tc.kind, "a1")
		if got != tc.want {
			t.Errorf("Marker(%s, a1) = %q, want %q", tc.kind, got, tc.want)
		}
		spans := FindMarkers("x " + got + " y")
		if len(spans) != 1 {
			t.Fatalf("FindMarkers should find the %s marker; got %v", tc.kind, spans)
		}
		if spans[0].Kind != tc.kind || spans[0].ID != "a1" {
			t.Errorf("round-trip drift: got kind=%s id=%s, want kind=%s id=a1",
				spans[0].Kind, spans[0].ID, tc.kind)
		}
	}
}

// TestMarker_UnknownKindOrEmptyIDReturnsEmpty pins the defensive
// contract: no tag mapping (or no id) means no marker, never a
// half-formed one.
func TestMarker_UnknownKindOrEmptyIDReturnsEmpty(t *testing.T) {
	if got := Marker(AttachmentKind("video"), "a1"); got != "" {
		t.Errorf("Marker(unknown kind) = %q, want empty", got)
	}
	if got := Marker(AttachmentPaste, ""); got != "" {
		t.Errorf("Marker(empty id) = %q, want empty", got)
	}
}

// TestFindMarkers_OffsetsAndOrder asserts byte offsets are exact (the
// composer slices the string with them) and that spans come back in
// text order.
func TestFindMarkers_OffsetsAndOrder(t *testing.T) {
	s := "see ‹p:1› and ‹m:2x›!"
	spans := FindMarkers(s)
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want 2: %v", len(spans), spans)
	}
	if s[spans[0].Start:spans[0].End] != "‹p:1›" {
		t.Errorf("span 0 slices to %q, want ‹p:1›", s[spans[0].Start:spans[0].End])
	}
	if s[spans[1].Start:spans[1].End] != "‹m:2x›" {
		t.Errorf("span 1 slices to %q, want ‹m:2x›", s[spans[1].Start:spans[1].End])
	}
	if spans[0].Start > spans[1].Start {
		t.Errorf("spans out of text order: %v", spans)
	}
}

// TestFindMarkers_RejectsMalformed: partially-edited markers must NOT
// match - they degrade to literal text instead of half-expanding.
func TestFindMarkers_RejectsMalformed(t *testing.T) {
	for _, s := range []string{
		"",
		"plain text",
		"‹p:1",      // missing close
		"p:1›",      // missing open
		"‹x:1›",     // unknown tag
		"‹p:›",      // empty id
		"‹p:ABC›",   // uppercase id (charset is lowercase base36)
		"‹p : 1 ›",  // interior spaces
		"‹‹p:1",     // mangled
		"« angle »", // unrelated punctuation
	} {
		if got := FindMarkers(s); got != nil {
			t.Errorf("FindMarkers(%q) = %v, want nil", s, got)
		}
		if ContainsMarker(s) {
			t.Errorf("ContainsMarker(%q) = true, want false", s)
		}
	}
	if !ContainsMarker("a ‹i:9z› b") {
		t.Error("ContainsMarker should detect a well-formed marker")
	}
}

// TestExpandMarkers_PasteFencedAndLabeled is the core model-bound
// expansion: full content, fenced, labeled with the nickname, zero
// marker residue.
func TestExpandMarkers_PasteFencedAndLabeled(t *testing.T) {
	atts := []Attachment{{
		ID: "1", Kind: AttachmentPaste, Nickname: "paste#1",
		Content: "line one\nline two",
	}}
	got := ExpandMarkers("check this ‹p:1› please", atts)
	for _, want := range []string{
		"[pasted: paste#1]",
		"```\nline one\nline two\n```",
		"check this ",
		" please",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expansion missing %q in:\n%s", want, got)
		}
	}
	if ContainsMarker(got) {
		t.Errorf("raw marker leaked into model-bound text:\n%s", got)
	}
}

// TestExpandMarkers_FenceStretchesPastBackticks guards the fenced
// block against content that itself contains ``` runs.
func TestExpandMarkers_FenceStretchesPastBackticks(t *testing.T) {
	atts := []Attachment{{
		ID: "1", Kind: AttachmentPaste, Nickname: "md",
		Content: "before\n```go\ncode\n```\nafter",
	}}
	got := ExpandMarkers("‹p:1›", atts)
	if !strings.Contains(got, "````\n") {
		t.Errorf("fence should stretch to ```` when content holds ```:\n%s", got)
	}
	if !strings.Contains(got, "```go\ncode\n```") {
		t.Errorf("content must survive verbatim:\n%s", got)
	}
}

// TestExpandMarkers_ImageAndMentionPlaceholders: the not-yet-owned
// kinds expand to readable placeholders - never the raw marker.
func TestExpandMarkers_ImageAndMentionPlaceholders(t *testing.T) {
	atts := []Attachment{
		{ID: "1", Kind: AttachmentImage, Nickname: "screenshot"},
		{ID: "2", Kind: AttachmentMention, Path: "internal/agent/loop.go"},
	}
	got := ExpandMarkers("see ‹i:1› and ‹m:2›", atts)
	if !strings.Contains(got, "[image: screenshot]") {
		t.Errorf("image placeholder missing in %q", got)
	}
	if !strings.Contains(got, "@internal/agent/loop.go") {
		t.Errorf("mention placeholder (path fallback) missing in %q", got)
	}
	if ContainsMarker(got) {
		t.Errorf("raw marker leaked: %q", got)
	}
}

// TestExpandMarkers_MentionIsCompactReference (slice I-4): a mention
// expands to "@path" plus the read-with-tools note - file CONTENTS are
// never inlined - and a path-less mention falls back to the nickname
// label chain.
func TestExpandMarkers_MentionIsCompactReference(t *testing.T) {
	got := ExpandMarkers("fix ‹m:1›", []Attachment{
		{ID: "1", Kind: AttachmentMention, Nickname: "loop.go", Path: "internal/agent/loop.go"},
	})
	want := "fix @internal/agent/loop.go (mentioned file, not inlined; read it with file tools if needed)"
	if got != want {
		t.Errorf("mention expansion = %q, want %q", got, want)
	}
	got = ExpandMarkers("fix ‹m:1›", []Attachment{
		{ID: "1", Kind: AttachmentMention, Nickname: "loop.go"},
	})
	if !strings.Contains(got, "@loop.go (mentioned file") {
		t.Errorf("path-less mention should fall back to the nickname: %q", got)
	}
	if ContainsMarker(got) {
		t.Errorf("raw marker leaked: %q", got)
	}
}

// TestExpandMarkers_MissingAttachmentIsDeterministic: a marker whose
// attachment vanished (e.g. recalled history) becomes a stable
// placeholder, not a leak and not a panic.
func TestExpandMarkers_MissingAttachmentIsDeterministic(t *testing.T) {
	got := ExpandMarkers("dangling ‹p:9z›", nil)
	if got != "dangling [attachment 9z unavailable]" {
		t.Errorf("got %q", got)
	}
}

// TestExpandMarkers_NoMarkersFastPath: chip-less text passes through
// untouched (same string, no allocation-level guarantees asserted).
func TestExpandMarkers_NoMarkersFastPath(t *testing.T) {
	in := "perfectly ordinary text with `code` and 100% punctuation"
	if got := ExpandMarkers(in, nil); got != in {
		t.Errorf("chip-less text must pass through verbatim; got %q", got)
	}
}

// TestExpandMarkers_ContentNotReExpanded: marker-shaped text INSIDE a
// paste's content must come out literal - expansion is single-pass.
func TestExpandMarkers_ContentNotReExpanded(t *testing.T) {
	atts := []Attachment{
		{ID: "1", Kind: AttachmentPaste, Nickname: "outer", Content: "inner ‹p:2› stays literal"},
		{ID: "2", Kind: AttachmentPaste, Nickname: "trap", Content: "BOOM"},
	}
	got := ExpandMarkers("‹p:1›", atts)
	if !strings.Contains(got, "inner ‹p:2› stays literal") {
		t.Errorf("content was re-expanded:\n%s", got)
	}
	if strings.Contains(got, "BOOM") {
		t.Errorf("nested marker expanded - single-pass invariant broken:\n%s", got)
	}
}

// TestExpandPaste_EmptyAndNewlineTerminatedContent covers the fence
// termination branches: empty content and already-\n-terminated
// content must not gain a stray blank line.
func TestExpandPaste_EmptyAndNewlineTerminatedContent(t *testing.T) {
	empty := expandPaste(Attachment{ID: "1", Kind: AttachmentPaste, Nickname: "e"})
	if !strings.Contains(empty, "```\n```") {
		t.Errorf("empty paste should produce an empty fenced block:\n%s", empty)
	}
	term := expandPaste(Attachment{ID: "2", Kind: AttachmentPaste, Nickname: "t", Content: "x\n"})
	if strings.Contains(term, "x\n\n```") {
		t.Errorf("newline-terminated content gained a blank line:\n%s", term)
	}
}

// TestAttachmentLabel_FallbackChain: nickname → path → ID.
func TestAttachmentLabel_FallbackChain(t *testing.T) {
	if got := attachmentLabel(Attachment{ID: "1", Nickname: "n", Path: "p"}); got != "n" {
		t.Errorf("nickname should win; got %q", got)
	}
	if got := attachmentLabel(Attachment{ID: "1", Path: "p"}); got != "p" {
		t.Errorf("path is the second fallback; got %q", got)
	}
	if got := attachmentLabel(Attachment{ID: "1"}); got != "1" {
		t.Errorf("ID is the last fallback; got %q", got)
	}
}

// TestMessagePayload_AttachmentsJSONRoundTrip proves the additive
// schema: chips survive marshal/unmarshal, chip-less payloads stay
// byte-identical to the pre-I-1 shape, and old rows (no attachments
// key) unmarshal cleanly.
func TestMessagePayload_AttachmentsJSONRoundTrip(t *testing.T) {
	in := MessagePayload{
		Text: "hi ‹p:1›",
		Attachments: []Attachment{{
			ID: "1", Kind: AttachmentPaste, Nickname: "paste#1",
			Content: "big paste", Path: "", SHA256: "",
		}},
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out MessagePayload
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Text != in.Text || len(out.Attachments) != 1 || out.Attachments[0] != in.Attachments[0] {
		t.Errorf("round-trip drift: %+v -> %+v", in, out)
	}

	// Chip-less payloads omit the key entirely (back-compat bytes).
	plain, err := json.Marshal(MessagePayload{Text: "plain"})
	if err != nil {
		t.Fatalf("marshal plain: %v", err)
	}
	if strings.Contains(string(plain), "attachments") {
		t.Errorf("empty Attachments must be omitted; got %s", plain)
	}

	// Pre-I-1 rows (text only) still unmarshal.
	var old MessagePayload
	if err := json.Unmarshal([]byte(`{"text":"legacy"}`), &old); err != nil {
		t.Fatalf("legacy unmarshal: %v", err)
	}
	if old.Text != "legacy" || old.Attachments != nil {
		t.Errorf("legacy row drift: %+v", old)
	}
}

// TestMarkerKind_UnknownTagDefensiveFallback covers the markerKind
// branch the regexp can't normally reach.
func TestMarkerKind_UnknownTagDefensiveFallback(t *testing.T) {
	if got := markerKind("z"); got != AttachmentKind("") {
		t.Errorf("markerKind(z) = %q, want empty", got)
	}
}
