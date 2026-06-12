package chat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/clipboard"
	"github.com/georgebuilds/carlos/internal/theme"
)

// pngBytes encodes a w×h solid-color PNG, the shape the real
// clipboard library always delivers.
func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

// ctrlV is the key the bubbles textarea binds to text paste; the I-3
// intercept must win when an image is on the clipboard.
func ctrlV() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyCtrlV} }

// seedAgentRow inserts the agents-table row WriteArtifact's FK needs
// (production always has one via ensureDefaultAgent; the chat-side
// seedAgent helper only appends events).
func seedAgentRow(t *testing.T, log *agent.SQLiteEventLog, agentID string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	err := log.InsertAgent(context.Background(), agent.AgentRow{
		ID: agentID, RootID: agentID, State: agent.StateRunning,
		Title: "image paste", Model: "fake",
		CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
	})
	if err != nil {
		t.Fatalf("insert agent row: %v", err)
	}
}

// newImageModel builds a driven chat model with an injected clipboard
// and the artifact base redirected into the test's temp dir.
func newImageModel(t *testing.T, agentID string, clip clipboard.Reader, opts ...Option) *Model {
	t.Helper()
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))
	log := openTempLog(t)
	seedAgent(t, log, agentID, "image paste", "fake")
	seedAgentRow(t, log, agentID)
	opts = append([]Option{WithClipboard(clip)}, opts...)
	m := New(log, agentID, NewMemTextSource(), opts...)
	return drive(t, m, 120, 30)
}

func sendCtrlV(t *testing.T, m *Model) (*Model, tea.Cmd) {
	t.Helper()
	updated, cmd := m.Update(ctrlV())
	return updated.(*Model), cmd
}

// TestUpdate_CtrlVImageInsertsChip is the happy path: an image on the
// clipboard becomes one ▣ chip - marker-only textarea, artifact
// persisted content-addressed, attachment carrying Path + SHA256, and
// a dimensions-aware nickname.
func TestUpdate_CtrlVImageInsertsChip(t *testing.T) {
	data := pngBytes(t, 2, 3)
	fake := &clipboard.Fake{Image: data}
	m := newImageModel(t, "01HV00000000000000000I3001", fake)

	m, _ = sendCtrlV(t, m)

	chips := m.composer.Chips()
	if len(chips) != 1 || chips[0].Kind != agent.AttachmentImage {
		t.Fatalf("chips = %+v, want one image chip", chips)
	}
	wantNick := "image (2×3 png, " + strconv.Itoa(len(data)) + " B)"
	if chips[0].Nickname != wantNick {
		t.Errorf("nickname = %q, want %q", chips[0].Nickname, wantNick)
	}
	if got := m.ta.Value(); got != agent.Marker(agent.AttachmentImage, chips[0].ID) {
		t.Errorf("textarea must hold only the marker: %q", got)
	}
	_, atts := m.composer.Serialize()
	if len(atts) != 1 {
		t.Fatalf("attachments = %d, want 1", len(atts))
	}
	wantSHA := sha256.Sum256(data)
	if atts[0].SHA256 != hex.EncodeToString(wantSHA[:]) {
		t.Errorf("attachment SHA = %q, want content hash", atts[0].SHA256)
	}
	blob, err := os.ReadFile(atts[0].Path)
	if err != nil || !bytes.Equal(blob, data) {
		t.Errorf("artifact blob at %q wrong (err=%v, %d bytes)", atts[0].Path, err, len(blob))
	}
	if fake.Calls != 1 {
		t.Errorf("clipboard reads = %d, want exactly 1", fake.Calls)
	}
	if m.status != "" {
		t.Errorf("happy path must not set a status: %q", m.status)
	}
}

// TestUpdate_CtrlVNoImageFallsThrough pins the text-paste contract:
// with no image on the clipboard (headless sessions included) ctrl+v
// reaches the textarea's own paste binding untouched - no chip, no
// status, and the textarea returns its paste command.
func TestUpdate_CtrlVNoImageFallsThrough(t *testing.T) {
	fake := &clipboard.Fake{} // zero value: no image, counts calls
	m := newImageModel(t, "01HV00000000000000000I3002", fake)

	m, cmd := sendCtrlV(t, m)

	if m.composer.HasChips() {
		t.Error("no-image ctrl+v must not create a chip")
	}
	if m.status != "" {
		t.Errorf("no-image ctrl+v must not set a status: %q", m.status)
	}
	if fake.Calls != 1 {
		t.Errorf("clipboard reads = %d, want exactly 1 probe", fake.Calls)
	}
	// The textarea saw the key: its Paste binding returns a command.
	if cmd == nil {
		t.Error("ctrl+v must fall through to the textarea's paste command")
	}
}

// TestUpdate_CtrlVNilClipboardFallsThrough: an explicit nil reader
// disables the probe entirely; ctrl+v stays a stock text paste. The
// bare-Model path (nil composer AND nil clip) must be safe too.
func TestUpdate_CtrlVNilClipboardFallsThrough(t *testing.T) {
	m := newImageModel(t, "01HV00000000000000000I3003", nil)
	m, cmd := sendCtrlV(t, m)
	if m.composer.HasChips() || m.status != "" {
		t.Errorf("nil clipboard must fall through clean: chips=%v status=%q",
			m.composer.Chips(), m.status)
	}
	if cmd == nil {
		t.Error("nil clipboard ctrl+v must still reach the textarea")
	}

	bare := &Model{}
	if bare.handleImagePaste() {
		t.Error("bare Model must not consume ctrl+v")
	}
}

// TestUpdate_CtrlVArtifactWriteFailure: when the artifact store cannot
// be written the key is consumed with a status-line error, NO chip is
// inserted, and the textarea is untouched - the clipboard still holds
// the image so the user can retry.
func TestUpdate_CtrlVArtifactWriteFailure(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &clipboard.Fake{Image: pngBytes(t, 4, 4)}
	log := openTempLog(t)
	const agentID = "01HV00000000000000000I3004"
	seedAgent(t, log, agentID, "image fail", "fake")
	m := New(log, agentID, NewMemTextSource(), WithClipboard(fake))
	m = drive(t, m, 120, 30)
	t.Setenv("CARLOS_ARTIFACT_BASE", blocker) // mkdir over a file fails

	m, cmd := sendCtrlV(t, m)

	if cmd != nil {
		t.Error("failed image paste must consume the key (no textarea cmd)")
	}
	if m.composer.HasChips() {
		t.Error("failed image paste must not insert a chip")
	}
	if m.ta.Value() != "" {
		t.Errorf("textarea must stay untouched: %q", m.ta.Value())
	}
	if !strings.HasPrefix(m.status, "image paste failed:") || m.statusKind != statusError {
		t.Errorf("status = %q (kind %d), want an image-paste error", m.status, m.statusKind)
	}
	if !strings.Contains(m.status, "clipboard kept") {
		t.Errorf("status must tell the user the clipboard survives: %q", m.status)
	}
}

// noArtifactLog is an agent.EventLog with no artifact support, standing in
// for any future log backend that can't store artifacts.
type noArtifactLog struct{}

func (noArtifactLog) Append(context.Context, agent.Event) (int64, error) { return 0, nil }
func (noArtifactLog) Read(context.Context, string, int64) ([]agent.Event, error) {
	return nil, nil
}
func (noArtifactLog) Subscribe(string) (<-chan agent.Event, func(), error) {
	ch := make(chan agent.Event)
	return ch, func() {}, nil
}
func (noArtifactLog) Close() error { return nil }

// TestUpdate_CtrlVLogWithoutArtifactsFails: a log that can't store
// artifacts routes to the same no-chip failure shape as a write error.
func TestUpdate_CtrlVLogWithoutArtifactsFails(t *testing.T) {
	fake := &clipboard.Fake{Image: pngBytes(t, 1, 1)}
	m := New(noArtifactLog{}, "01HV00000000000000000I3005", NewMemTextSource(), WithClipboard(fake))
	m = drive(t, m, 120, 30)

	m, cmd := sendCtrlV(t, m)
	if cmd != nil || m.composer.HasChips() {
		t.Error("artifact-less log must consume the key without a chip")
	}
	if !strings.HasPrefix(m.status, "image paste failed:") || m.statusKind != statusError {
		t.Errorf("status = %q (kind %d), want an image-paste error", m.status, m.statusKind)
	}
}

// TestUpdate_CtrlVWhileSlashSuggestOpen: an image paste inside an open
// slash band inserts the chip at the cursor and re-refreshes the band.
func TestUpdate_CtrlVWhileSlashSuggestOpen(t *testing.T) {
	fake := &clipboard.Fake{Image: pngBytes(t, 1, 1)}
	m := newImageModel(t, "01HV00000000000000000I3006", fake)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/mo")})
	m = updated.(*Model)
	m, _ = sendCtrlV(t, m)
	chips := m.composer.Chips()
	if len(chips) != 1 {
		t.Fatal("image paste in slash mode must still chip")
	}
	want := "/mo" + agent.Marker(agent.AttachmentImage, chips[0].ID)
	if got := m.ta.Value(); got != want {
		t.Errorf("value = %q, want %q", got, want)
	}
	_ = m.View() // band + chip render together without panicking
}

// underlineSGRRe matches an SGR sequence carrying a standalone "4"
// (underline) parameter. Tests force the 16-color ANSI profile so
// color params are 3x/9x codes and a bare 4 is unambiguous.
var underlineSGRRe = regexp.MustCompile(`\x1b\[(?:[0-9]+;)*4(?:;[0-9]+)*m`)

// withANSIProfile pins lipgloss to the 16-color profile for style
// assertions (go test's non-TTY stdout otherwise downgrades every
// style to plain text) and restores the prior profile after.
func withANSIProfile(t *testing.T) {
	t.Helper()
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.ANSI)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })
}

// TestRenderChip_VisionGateUnderline pins the gate treatment at the
// chip level: gated image chips render underlined in colorWarn, plain
// content identical either way; paste chips never gate.
func TestRenderChip_VisionGateUnderline(t *testing.T) {
	withANSIProfile(t)
	warned := renderChip(agent.AttachmentImage, "shot", true)
	normal := renderChip(agent.AttachmentImage, "shot", false)
	if !underlineSGRRe.MatchString(warned) {
		t.Errorf("gated image chip must carry an underline SGR: %q", warned)
	}
	if underlineSGRRe.MatchString(normal) {
		t.Errorf("ungated image chip must not underline: %q", normal)
	}
	if stripANSI(warned) != stripANSI(normal) {
		t.Errorf("gate must not change chip text: %q vs %q",
			stripANSI(warned), stripANSI(normal))
	}
	if underlineSGRRe.MatchString(renderChip(agent.AttachmentPaste, "p", true)) {
		t.Error("paste chips must never take the vision-warn treatment")
	}
}

// TestRenderComposerInput_VisionGateResolvesLive drives the full
// render path: the probe is consulted at render time, so flipping the
// provider's capability (a /model swap) flips the chip treatment with
// no re-insert.
func TestRenderComposerInput_VisionGateResolvesLive(t *testing.T) {
	withANSIProfile(t)
	vision := true
	fake := &clipboard.Fake{Image: pngBytes(t, 1, 1)}
	m := newImageModel(t, "01HV00000000000000000I3007", fake,
		WithVisionProbe(func() bool { return vision }))
	m, _ = sendCtrlV(t, m)

	if out := m.renderComposerInput(100); underlineSGRRe.MatchString(out) {
		t.Errorf("vision=true must not underline the chip:\n%q", out)
	}
	vision = false // simulate /model swap to a non-vision provider
	out := m.renderComposerInput(100)
	if !underlineSGRRe.MatchString(out) {
		t.Errorf("vision=false must underline the image chip:\n%q", out)
	}
	if !strings.Contains(stripANSI(out), theme.ChipSigilImage) {
		t.Errorf("chip sigil missing from gated render:\n%q", out)
	}
}

// TestRenderImagePeekCard_Content pins the card shape: dashed top rule
// with the dim italic "image" corner tag, stats row (size, dimensions,
// format), NO content preview, dashed bottom rule - plus the warning
// line exactly when the provider lacks vision.
func TestRenderImagePeekCard_Content(t *testing.T) {
	meta := imageMeta{bytes: 24 * 1024, width: 640, height: 480, format: "png"}

	out := stripANSI(renderImagePeekCard(meta, false, 80))
	rows := strings.Split(out, "\n")
	if len(rows) != 3 {
		t.Fatalf("ungated card rows = %d, want 3:\n%s", len(rows), out)
	}
	if !strings.Contains(rows[0], "┄") || !strings.HasSuffix(rows[0], "image") {
		t.Errorf("top rule + corner tag wrong: %q", rows[0])
	}
	if !strings.Contains(rows[1], "24 KB · 640×480 · png") {
		t.Errorf("stats row = %q, want size · dims · format", rows[1])
	}
	if strings.Trim(strings.TrimSpace(rows[2]), "┄") != "" {
		t.Errorf("bottom rule must be all dashes: %q", rows[2])
	}

	gated := stripANSI(renderImagePeekCard(meta, true, 80))
	grows := strings.Split(gated, "\n")
	if len(grows) != 4 {
		t.Fatalf("gated card rows = %d, want 4:\n%s", len(grows), gated)
	}
	if !strings.Contains(grows[2], "↳ this frame's model can't read images") {
		t.Errorf("gated card missing the warning line: %q", grows[2])
	}

	// Undecodable bytes degrade to a size-only stats row.
	plain := stripANSI(renderImagePeekCard(imageMeta{bytes: 812}, false, 80))
	if !strings.Contains(plain, "812 B") || strings.Contains(plain, "·") {
		t.Errorf("size-only stats wrong:\n%s", plain)
	}
}

// TestRenderImagePeekCard_NoLeftStripeAndNarrow extends the I-2 design
// regression to image peeks: no row may open with a stripe glyph, at
// any width, in either gate state, and narrow widths clamp safely.
func TestRenderImagePeekCard_NoLeftStripeAndNarrow(t *testing.T) {
	meta := imageMeta{bytes: 1536, width: 10, height: 10, format: "png"}
	for _, gate := range []bool{false, true} {
		for _, w := range []int{80, 40, 10, 0} {
			for i, row := range strings.Split(stripANSI(renderImagePeekCard(meta, gate, w)), "\n") {
				trimmed := strings.TrimLeft(row, " ")
				for _, banned := range []string{"│", "┃", "▌", "█", "▎"} {
					if strings.HasPrefix(trimmed, banned) {
						t.Errorf("gate=%v w=%d row %d opens with banned stripe glyph %q: %q",
							gate, w, i, banned, row)
					}
				}
				if !strings.HasPrefix(row, "  ") {
					t.Errorf("gate=%v w=%d row %d missing 2-cell indent: %q", gate, w, i, row)
				}
			}
		}
	}
}

// TestRenderInput_ImagePeekBand: the image card occupies the hint-band
// slot while the cursor touches the chip, with the warning line when
// gated, and the binary payload never leaks into the frame.
func TestRenderInput_ImagePeekBand(t *testing.T) {
	vision := false
	fake := &clipboard.Fake{Image: pngBytes(t, 5, 7)}
	m := newImageModel(t, "01HV00000000000000000I3008", fake,
		WithVisionProbe(func() bool { return vision }))
	m, _ = sendCtrlV(t, m)

	out := stripANSI(m.renderInput(100))
	if !strings.Contains(out, "5×7 · png") {
		t.Errorf("image peek stats missing:\n%s", out)
	}
	if !strings.Contains(out, "↳ this frame's model can't read images") {
		t.Errorf("gated peek missing warning line:\n%s", out)
	}
	vision = true
	out = stripANSI(m.renderInput(100))
	if strings.Contains(out, "can't read images") {
		t.Errorf("ungated peek must drop the warning line:\n%s", out)
	}
	// Cursor away (two cells past the marker's end): card gone. The
	// chip itself still renders (its nickname carries the dimensions),
	// so the card's dashed rules are the absence signal.
	m.ta.CursorEnd()
	m.ta.InsertString(" x")
	m.composer.Sync()
	if strings.Contains(stripANSI(m.renderInput(100)), "┄") {
		t.Error("image peek must vanish when the cursor leaves the chip")
	}
}

// TestRenderImagePeekCard_NoColor: with a NO_COLOR palette the card
// still carries the tag, stats, warning line, and rules as plain text.
func TestRenderImagePeekCard_NoColor(t *testing.T) {
	t.Cleanup(func() { ApplyPalette(theme.Load(theme.Options{})) })
	ApplyPalette(theme.Load(theme.Options{
		Env: func(k string) string {
			if k == "NO_COLOR" {
				return "1"
			}
			return ""
		},
	}))
	meta := imageMeta{bytes: 2048, width: 8, height: 8, format: "png"}
	plain := stripANSI(renderImagePeekCard(meta, true, 80))
	for _, want := range []string{"image", "2 KB · 8×8 · png", "↳ this frame's model can't read images", "┄"} {
		if !strings.Contains(plain, want) {
			t.Errorf("NO_COLOR image card missing %q:\n%s", want, plain)
		}
	}
}

// TestModel_ImagePasteSubmitAndReplay is the end-to-end contract:
// ctrl+v -> chip -> submit persists marker text + image attachment
// (Path + SHA256 intact for the chatglue bridge); a fresh model
// replaying the log renders the ▣ chip, never the raw ‹i:› marker.
func TestModel_ImagePasteSubmitAndReplay(t *testing.T) {
	data := pngBytes(t, 3, 3)
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))
	log := openTempLog(t)
	const agentID = "01HV00000000000000000I3009"
	seedAgent(t, log, agentID, "image replay", "fake")
	seedAgentRow(t, log, agentID)
	m := New(log, agentID, NewMemTextSource(), WithClipboard(&clipboard.Fake{Image: data}))
	m = drive(t, m, 120, 30)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("see: ")})
	m = updated.(*Model)
	m, _ = sendCtrlV(t, m)
	nick := m.composer.Chips()[0].Nickname
	cmd := m.submit()
	if cmd == nil {
		t.Fatal("submit returned nil cmd")
	}
	if msg := cmd(); msg != nil {
		if em, ok := msg.(errMsg); ok {
			t.Fatalf("submit cmd errored: %v", em.err)
		}
	}

	fresh := New(log, agentID, NewMemTextSource())
	fresh = drive(t, fresh, 120, 30)
	view := stripANSI(fresh.View())
	if strings.Contains(view, "‹i:") {
		t.Errorf("raw image marker leaked into replayed transcript:\n%s", view)
	}
	if !strings.Contains(view, theme.ChipSigilImage+" "+nick) {
		t.Errorf("replayed transcript missing the image chip %q:\n%s", nick, view)
	}

	// The persisted payload carries the bridge-ready attachment.
	evs, err := log.Read(context.Background(), agentID, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	wantSHA := sha256.Sum256(data)
	var found bool
	for _, ev := range evs {
		if ev.Type != agent.EvtUserMessage {
			continue
		}
		var p agent.MessagePayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatalf("payload: %v", err)
		}
		if len(p.Attachments) != 1 {
			t.Fatalf("attachments = %d, want 1", len(p.Attachments))
		}
		a := p.Attachments[0]
		if a.Kind != agent.AttachmentImage || a.SHA256 != hex.EncodeToString(wantSHA[:]) || a.Path == "" {
			t.Errorf("persisted attachment = %+v, want image kind + SHA + path", a)
		}
		found = true
	}
	if !found {
		t.Fatal("no EvtUserMessage persisted")
	}
}

// TestComposer_ImageMetaLifecycle: stats follow the chip - pruned when
// the chip is deleted, cleared on Reset, zero for unknown IDs, and all
// nil-receiver safe.
func TestComposer_ImageMetaLifecycle(t *testing.T) {
	c, ta := newTestComposer()
	id := c.InsertChip(agent.Attachment{Kind: agent.AttachmentImage, Nickname: "img"})
	c.setImageMeta(id, imageMeta{bytes: 9, width: 1, height: 2, format: "png"})
	if got := c.imageMetaFor(id); got.bytes != 9 || got.format != "png" {
		t.Errorf("meta round-trip = %+v", got)
	}
	if got := c.imageMetaFor("nope"); got != (imageMeta{}) {
		t.Errorf("unknown id must yield zero meta: %+v", got)
	}

	// Kill the marker out-of-band; Sync prunes attachment AND meta.
	ta.SetValue("plain")
	c.Sync()
	if got := c.imageMetaFor(id); got != (imageMeta{}) {
		t.Errorf("meta must prune with the chip: %+v", got)
	}

	id2 := c.InsertChip(agent.Attachment{Kind: agent.AttachmentImage, Nickname: "img2"})
	c.setImageMeta(id2, imageMeta{bytes: 5})
	c.Reset()
	if got := c.imageMetaFor(id2); got != (imageMeta{}) {
		t.Errorf("Reset must clear meta: %+v", got)
	}

	// Partial prune: one of two image chips deleted, the other keeps
	// its meta (exercises the seen-filtered prune branch).
	idA := c.InsertChip(agent.Attachment{Kind: agent.AttachmentImage, Nickname: "a"})
	c.setImageMeta(idA, imageMeta{bytes: 1})
	idB := c.InsertChip(agent.Attachment{Kind: agent.AttachmentImage, Nickname: "b"})
	c.setImageMeta(idB, imageMeta{bytes: 2})
	ta.SetValue(agent.Marker(agent.AttachmentImage, idA)) // B's marker gone
	c.Sync()
	if got := c.imageMetaFor(idA); got.bytes != 1 {
		t.Errorf("surviving chip lost its meta: %+v", got)
	}
	if got := c.imageMetaFor(idB); got != (imageMeta{}) {
		t.Errorf("deleted chip kept its meta: %+v", got)
	}

	var nilC *Composer
	nilC.setImageMeta("x", imageMeta{})
	if got := nilC.imageMetaFor("x"); got != (imageMeta{}) {
		t.Errorf("nil composer meta = %+v", got)
	}
}

// TestDecodeImageMeta covers the stats sniffer: PNG headers decode to
// dimensions + format, garbage degrades to size-only.
func TestDecodeImageMeta(t *testing.T) {
	data := pngBytes(t, 6, 9)
	meta := decodeImageMeta(data)
	if meta.width != 6 || meta.height != 9 || meta.format != "png" || meta.bytes != len(data) {
		t.Errorf("png meta = %+v", meta)
	}
	garbage := decodeImageMeta([]byte("not an image at all"))
	if garbage.format != "" || garbage.width != 0 || garbage.bytes != 19 {
		t.Errorf("garbage meta = %+v", garbage)
	}
}

// TestImageNickname pins both nickname forms.
func TestImageNickname(t *testing.T) {
	withDims := imageNickname(imageMeta{bytes: 24 * 1024, width: 640, height: 480, format: "png"})
	if withDims != "image (640×480 png, 24 KB)" {
		t.Errorf("nickname = %q", withDims)
	}
	plain := imageNickname(imageMeta{bytes: 812})
	if plain != "image (812 B)" {
		t.Errorf("undecodable nickname = %q", plain)
	}
}

// TestByteSizeLabel pins the unit boundaries and the trimmed decimal.
func TestByteSizeLabel(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1 KB"},
		{24 * 1024, "24 KB"},
		{1228, "1.2 KB"},
		{1024 * 1024, "1 MB"},
		{5 * 1024 * 1024 / 2, "2.5 MB"},
	}
	for _, tc := range cases {
		if got := byteSizeLabel(tc.n); got != tc.want {
			t.Errorf("byteSizeLabel(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// TestModel_ImageVisionWarn pins the probe semantics: nil assumes
// vision (no warn), a wired probe is consulted on every call.
func TestModel_ImageVisionWarn(t *testing.T) {
	m := &Model{}
	if m.imageVisionWarn() {
		t.Error("nil probe must not warn")
	}
	calls := 0
	m.visionProbe = func() bool { calls++; return calls > 1 }
	if !m.imageVisionWarn() {
		t.Error("probe=false must warn")
	}
	if m.imageVisionWarn() {
		t.Error("probe=true must not warn (resolved live, not cached)")
	}
}

// Compile-time guard: the production SQLite log must satisfy the
// artifact sink the ctrl+v intercept type-asserts for.
var _ artifactSink = (*agent.SQLiteEventLog)(nil)
