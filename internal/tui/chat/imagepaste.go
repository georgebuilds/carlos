// Image paste (roadmap slice I-3, TUI half).
//
// Ctrl+V probes the system clipboard for an image BEFORE the bubbles
// textarea's own paste binding fires (the textarea binds ctrl+v to a
// TEXT clipboard read via atotto). When an image is present its PNG
// bytes are stored content-addressed via agent.WriteArtifact and the
// composer gains one ▣ chip whose attachment carries Path + SHA256 -
// exactly what the chatglue bridge (the plumbing half of I-3) needs
// to ship real image blocks to vision providers on the next turn.
// When the clipboard holds no image - or no clipboard is available
// at all (headless session, test Fake without bytes) - the key falls
// through to the textarea untouched, so text paste keeps working
// everywhere.
//
// Failure shape (decided in the I-3 integration notes): when the
// artifact write fails the key is consumed, NO chip is inserted, and
// a status-line error names the failure. The clipboard is left
// untouched so the user can simply retry; inserting a chip whose
// pixels were never persisted would LOOK like success while shipping
// the model a placeholder.
//
// Capability gate: image chips render a colorWarn underline and the
// peek card grows a warning line when the active provider reports
// Capabilities().Vision == false. Warn-only - submit is never
// blocked, because the bridge degrades gracefully server-side (the
// image becomes a readable text placeholder). The probe is resolved
// live on every render so /model swaps and frame switches flip the
// treatment without re-inserting chips.

package chat

import (
	"bytes"
	"context"
	"image"
	_ "image/png" // registered for DecodeConfig: clipboard images are always PNG
	"strconv"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// imageArtifactKind is the artifacts-table kind recorded for pasted
// images. The kind column is open TEXT; "image" is the conventional
// value the I-3 plumbing slice introduced.
const imageArtifactKind = "image"

// imagePasteTimeout bounds the artifact write (one file write + one
// SQLite insert). Generous - the write is local - but a hard ceiling
// so a wedged disk can't freeze the key loop forever.
const imagePasteTimeout = 5 * time.Second

// artifactSink is the slice of the event log handleImagePaste needs;
// it structurally matches agent.WriteArtifact's log parameter.
// *agent.SQLiteEventLog (the only production log) satisfies it; a
// bare in-memory test log doesn't, which routes image pastes to the
// same no-chip failure shape as a write error.
type artifactSink interface {
	InsertArtifact(ctx context.Context, a agent.Artifact) error
}

// imageMeta carries the compose-time stats for one image chip's peek
// card: byte size, pixel dimensions, and decoded format. Held on the
// composer (not the persisted Attachment) because the peek card only
// exists while composing - replayed messages render the chip from
// the nickname alone.
type imageMeta struct {
	bytes  int
	width  int
	height int
	format string // "" when the bytes didn't decode
}

// handleImagePaste is the ctrl+v intercept body. Returns true when
// the key was fully consumed (image chip inserted, or an error was
// surfaced); false means "no image here" and the caller must let the
// keystroke flow to the textarea so text paste works unchanged.
func (m *Model) handleImagePaste() bool {
	if m.clip == nil || m.composer == nil {
		return false
	}
	data, ok := m.clip.ReadImage()
	if !ok {
		return false
	}
	sink, ok := m.log.(artifactSink)
	if !ok {
		m.status = "image paste failed: this session's event log can't store artifacts"
		m.statusKind = statusError
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), imagePasteTimeout)
	defer cancel()
	ref, err := agent.WriteArtifact(ctx, sink, m.agentID, imageArtifactKind, data)
	if err != nil {
		m.status = "image paste failed: " + err.Error() + " - clipboard kept, retry with ctrl+v"
		m.statusKind = statusError
		return true
	}
	meta := decodeImageMeta(data)
	id := m.composer.InsertChip(agent.Attachment{
		Kind:     agent.AttachmentImage,
		Nickname: imageNickname(meta),
		Path:     ref.Path,
		SHA256:   ref.SHA256,
	})
	m.composer.setImageMeta(id, meta)
	return true
}

// imageVisionWarn reports whether image chips should render the warn
// treatment: a wired vision probe that answers false. Resolved live
// on every render (the production probe reads the CURRENT dispatch
// under its own lock) so a /model swap or frame switch flips the
// treatment immediately. A nil probe (tests, the dev-aid surface)
// assumes vision - warning without knowing would be noise.
func (m *Model) imageVisionWarn() bool {
	return m.visionProbe != nil && !m.visionProbe()
}

// decodeImageMeta derives the peek/nickname stats from the raw image
// bytes. DecodeConfig reads only the header - cheap even for large
// images. Undecodable bytes degrade to a size-only meta.
func decodeImageMeta(data []byte) imageMeta {
	meta := imageMeta{bytes: len(data)}
	if cfg, format, err := image.DecodeConfig(bytes.NewReader(data)); err == nil {
		meta.width, meta.height, meta.format = cfg.Width, cfg.Height, format
	}
	return meta
}

// imageNickname labels an image chip in the clipnick style:
// "image (640×480 png, 24 KB)", degrading to "image (24 KB)" when
// the bytes didn't decode (a Fake serving non-PNG bytes; the real
// clipboard library always normalizes to PNG).
func imageNickname(meta imageMeta) string {
	if meta.format == "" {
		return "image (" + byteSizeLabel(meta.bytes) + ")"
	}
	return "image (" + itoa(meta.width) + "×" + itoa(meta.height) + " " +
		meta.format + ", " + byteSizeLabel(meta.bytes) + ")"
}

// imageStatsLine is the peek card's stats row: byte size, then pixel
// dimensions and format when the bytes decoded. Mid-dot separators,
// matching the paste card's stats row.
func imageStatsLine(meta imageMeta) string {
	s := byteSizeLabel(meta.bytes)
	if meta.format != "" {
		s += " · " + itoa(meta.width) + "×" + itoa(meta.height) + " · " + meta.format
	}
	return s
}

// byteSizeLabel renders a byte count for chip labels: "812 B",
// "24 KB", "1.2 MB". 1024 base, one decimal with ".0" trimmed -
// compactCount's convention with explicit binary units, because
// "24k" reads as a char count in this UI and bytes are not chars.
func byteSizeLabel(n int) string {
	switch {
	case n < 1024:
		return strconv.Itoa(n) + " B"
	case n < 1024*1024:
		return trimTrailingZero(float64(n)/1024) + " KB"
	default:
		return trimTrailingZero(float64(n)/(1024*1024)) + " MB"
	}
}
