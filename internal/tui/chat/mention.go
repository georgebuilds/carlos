// @file mention autocomplete (roadmap slice I-4).
//
// Typing "@" at the start of a whitespace-delimited token opens a fuzzy
// file completion band in the same hint-band slot the slash composer
// uses (suggest.go). Accepting a candidate (tab) replaces the typed
// "@query" with one ◇ mention chip via Composer.InsertChip; the chip's
// attachment carries the file path, which expands model-side to a
// compact "@path" reference (agent.ExpandMarkers) - file CONTENTS are
// never inlined, carlos has read tools for that.
//
// Candidate sources, by tier:
//
//  1. cwd files - walked with tools.WalkRespectingGitignore from the
//     process working directory, lazily on the first "@" and cached
//     for mentionIndexTTL. The roadmap's "open files" tier was dropped:
//     carlos is not an editor and tracks no open buffers (preflight
//     phase-I-composer, I-4 section).
//  2. @vault/ opt-in - when the query starts with "vault/" (or
//     "@vault/") AND a vault is configured (WithVaultPath, from
//     cfg.Vault.Path), candidates come from the vault tree instead.
//     Without a configured vault the prefix falls through to tier 1,
//     so a repo with a literal vault/ directory still completes.
//
// Bounds (all surfaced, never silent): the walk stops at
// mentionIndexCap files and the band's description row then carries a
// "first N files indexed" note; directories deeper than
// mentionDepthCap are pruned; binary files are skipped by extension.
// Skill files (SKILL.md, skills/ directories) are excluded per the
// roadmap's coding-agent identity bias - mentioning a skill is what
// slash commands are for.
//
// OSC 8 hyperlinks are slice 9l: every rendered path flows through
// mentionLinkText below, the single seam where 9l wraps the text in a
// terminal hyperlink.

package chat

import (
	"errors"
	"os"
	stdpath "path"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/fuzzy"
	"github.com/georgebuilds/carlos/internal/tools"
)

const (
	// mentionIndexCap bounds the file walk: past this many candidate
	// files the walk stops and the band advertises the truncation
	// ("first N files indexed"). 5000 covers carlos-sized repos with
	// huge headroom while keeping the worst-case Rank pass trivial.
	mentionIndexCap = 5000

	// mentionIndexTTL is how long a built index is trusted before the
	// next "@" keystroke rebuilds it. Time-based rather than mtime-
	// based: a correct mtime probe needs to re-stat every directory,
	// which is most of the walk's cost anyway. 30s keeps the index
	// fresh across a typical edit-mention cycle at zero steady-state
	// cost (the rebuild only happens while a mention is being typed).
	mentionIndexTTL = 30 * time.Second

	// mentionDepthCap prunes pathological directory nesting. 16 levels
	// is beyond any sane repo layout; the cap exists so a recursive
	// symlink farm or a vendored monorepo can't stall the key loop.
	mentionDepthCap = 16

	// mentionMatchCap bounds the navigable match list. The chips row
	// shows a handful at a time anyway; past 50 the user narrows the
	// query rather than arrowing.
	mentionMatchCap = 50

	// mentionVaultPrefix is the tier-2 opt-in: a query starting with
	// this searches the configured vault instead of the cwd.
	mentionVaultPrefix = "vault/"
)

// mentionBinaryExt lists extensions skipped during the index walk -
// files the model would never read as text. Cheap allowlist of the
// usual suspects; anything unlisted stays mentionable (over-including
// is harmless, the mention is just a path reference).
var mentionBinaryExt = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
	".ico": true, ".bmp": true, ".tiff": true, ".pdf": true,
	".zip": true, ".gz": true, ".tgz": true, ".tar": true, ".bz2": true,
	".xz": true, ".7z": true, ".rar": true,
	".exe": true, ".dll": true, ".so": true, ".dylib": true, ".a": true,
	".o": true, ".bin": true, ".dat": true, ".wasm": true,
	".sqlite": true, ".db": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".eot": true,
	".mp3": true, ".mp4": true, ".mov": true, ".avi": true, ".mkv": true,
	".wav": true, ".flac": true, ".ogg": true,
	".pyc": true, ".class": true, ".jar": true,
}

// mentionSuggest is the autocomplete state for the composer's
// "@-mention mode", a structural sibling of slashSuggest. Active when
// the cursor sits inside an @token being typed; powers the 3-row hint
// band above the input separator (candidate chips / ↳ path / keybinds).
type mentionSuggest struct {
	// open is true iff the cursor sits in an @token AND the user
	// hasn't dismissed the band with Esc.
	open bool

	// dismissed is set by Esc and cleared when the cursor leaves the
	// @token (predicate goes false re-arms the band).
	dismissed bool

	// matches is the ranked candidate list, capped at mentionMatchCap.
	matches []mentionMatch

	// cursor indexes matches; wraps with ↑↓, tab accepts it.
	cursor int

	// query is the typed pattern between the "@" and the cursor.
	query string

	// total is the full fuzzy-match count before the mentionMatchCap
	// clip; the description row shows "showing N of M" when they
	// differ.
	total int

	// note carries the index-truncation notice ("first N files
	// indexed") so a capped walk is never silent.
	note string
}

// mentionMatch is one accepted-able candidate: display is the relative
// path shown in the band ("internal/tui/chat/chat.go", or
// "vault/notes/x.md" for tier 2); path is what the attachment stores -
// identical for cwd files, the absolute on-disk path for vault files
// so read tools resolve it without knowing the vault root.
type mentionMatch struct {
	display string
	path    string
}

// mentionIndex is one lazily-built candidate file list. files holds
// slash-form paths relative to root, in walk (lexicographic) order so
// equal fuzzy scores tie-break deterministically.
type mentionIndex struct {
	root      string
	files     []string
	truncated bool
	builtAt   time.Time
}

// errMentionIndexFull is the internal walk-stop sentinel raised when
// the index hits mentionIndexCap. Never escapes buildMentionIndex.
var errMentionIndexFull = errors.New("mention index full")

// mentionLinkText is the single seam through which a mention's path
// reaches every rendered surface (the band's ↳ description row and the
// peek card's path row). Slice 9l: the path is wrapped in an OSC 8
// file:// hyperlink HERE and nowhere else.
func mentionLinkText(path string) string {
	return mentionLinkDisplay(path, path)
}

// mentionLinkDisplay is mentionLinkText with the display text split
// out. Width-clamped rows truncate the VISIBLE text before the wrap
// while the URL keeps the full path - running byte-based truncateRight
// on an already-linked string would slice mid-escape and leave the
// link unclosed, bleeding it into whatever renders next.
func mentionLinkDisplay(path, display string) string {
	return osc8(fileURL(path), display)
}

// mentionQueryAt is the @-mention predicate: given the cursor's
// logical line and its rune column, it returns the pattern between the
// freshest "@" and the cursor when - and only when - the cursor sits
// inside an @token being typed. Rules:
//
//   - the "@" must open a token: start of line or preceded by
//     whitespace ("foo@bar" stays literal text - emails never trigger);
//   - no whitespace between the "@" and the cursor (a lone "@" followed
//     by a space is the escape hatch: it stays literal);
//   - the query may itself contain "@"-free path characters only; a
//     second "@" inside the token reads as email-like and disarms.
func mentionQueryAt(line string, col int) (query string, ok bool) {
	runes := []rune(line)
	if col < 0 || col > len(runes) {
		return "", false
	}
	i := col - 1
	for ; i >= 0; i-- {
		r := runes[i]
		if unicode.IsSpace(r) {
			return "", false
		}
		if r == '@' {
			break
		}
	}
	if i < 0 {
		// No "@" anywhere in the token before the cursor.
		return "", false
	}
	if i > 0 && !unicode.IsSpace(runes[i-1]) {
		// Mid-word "@" (foo@bar) - never trigger.
		return "", false
	}
	return string(runes[i+1 : col]), true
}

// cursorLine returns the cursor's logical line plus its rune column
// within it, mirroring Composer.lineSpans' derivation.
func (m *Model) cursorLine() (string, int) {
	row := m.ta.Line()
	li := m.ta.LineInfo()
	col := li.StartColumn + li.ColumnOffset
	lines := strings.Split(m.ta.Value(), "\n")
	if row < 0 || row >= len(lines) {
		return "", 0
	}
	return lines[row], col
}

// refreshSuggests updates BOTH composer autocomplete layers from the
// current textarea state. The single post-edit hook chat.go calls so
// the two bands can never drift from the text or from each other.
func (m *Model) refreshSuggests() {
	m.slashSuggest.refresh(m.ta.Value(), m.argCompleterFn())
	m.refreshMentionSuggest()
}

// refreshMentionSuggest re-derives the mention state from the textarea.
// Idempotent; called after every keystroke. Slash mode owns the band
// outright (a value starting with "/" never mentions), matching the
// render-side priority in renderInput.
func (m *Model) refreshMentionSuggest() {
	s := &m.mentionSuggest
	if m.composer == nil || m.readOnly || looksLikeSlash(m.ta.Value()) {
		s.reset()
		return
	}
	line, col := m.cursorLine()
	q, ok := mentionQueryAt(line, col)
	if !ok {
		// Leaving the @token re-arms a dismissed band.
		s.reset()
		return
	}
	if s.dismissed {
		s.open = false
		s.matches = nil
		s.cursor = 0
		s.query = q
		s.total = 0
		s.note = ""
		return
	}
	prev, hadPrev := s.selected()
	matches, total, note := m.mentionCandidates(q)
	s.open = true
	s.query = q
	s.matches = matches
	s.total = total
	s.note = note
	s.cursor = 0
	if hadPrev {
		for i, c := range matches {
			if c.display == prev.display {
				s.cursor = i
				break
			}
		}
	}
}

func (s *mentionSuggest) reset() {
	*s = mentionSuggest{}
}

// dismiss hides the band without erasing the typed @token. Cleared
// when the cursor leaves the token (refresh sees the predicate false).
func (s *mentionSuggest) dismiss() {
	s.open = false
	s.dismissed = true
}

// selected returns the match under the cursor, or (zero, false).
func (s *mentionSuggest) selected() (mentionMatch, bool) {
	if !s.open || len(s.matches) == 0 {
		return mentionMatch{}, false
	}
	if s.cursor < 0 || s.cursor >= len(s.matches) {
		return mentionMatch{}, false
	}
	return s.matches[s.cursor], true
}

func (s *mentionSuggest) cursorUp() {
	if !s.open || len(s.matches) == 0 {
		return
	}
	s.cursor--
	if s.cursor < 0 {
		s.cursor = len(s.matches) - 1
	}
}

func (s *mentionSuggest) cursorDown() {
	if !s.open || len(s.matches) == 0 {
		return
	}
	s.cursor++
	if s.cursor >= len(s.matches) {
		s.cursor = 0
	}
}

// mentionCandidates resolves the tier (cwd vs vault) and fuzzy-ranks
// the index against the typed pattern. Returns the capped match list,
// the pre-cap total, and the index-truncation note ("" when complete).
func (m *Model) mentionCandidates(query string) (matches []mentionMatch, total int, note string) {
	pattern := query
	vault := false
	if m.vaultPath != "" {
		if rest, found := strings.CutPrefix(query, "@"+mentionVaultPrefix); found {
			pattern, vault = rest, true
		} else if rest, found := strings.CutPrefix(query, mentionVaultPrefix); found {
			pattern, vault = rest, true
		}
	}
	var idx *mentionIndex
	if vault {
		idx = m.mentionVaultIndex()
	} else {
		idx = m.mentionCwdIndex()
	}
	if idx == nil {
		return nil, 0, ""
	}
	ranked := fuzzy.Rank(pattern, idx.files)
	total = len(ranked)
	for i, r := range ranked {
		if i == mentionMatchCap {
			break
		}
		mm := mentionMatch{display: r.Candidate, path: r.Candidate}
		if vault {
			mm.display = mentionVaultPrefix + r.Candidate
			mm.path = filepath.Join(m.vaultPath, filepath.FromSlash(r.Candidate))
		}
		matches = append(matches, mm)
	}
	if idx.truncated {
		note = "first " + itoa(len(idx.files)) + " files indexed"
	}
	return matches, total, note
}

// mentionCwdIndex returns the tier-1 candidate index, building (or
// TTL-refreshing) it from the working directory on demand. mentionRoot
// overrides the root for tests; production leaves it "" = os.Getwd.
func (m *Model) mentionCwdIndex() *mentionIndex {
	if m.mentionIdx != nil && time.Since(m.mentionIdx.builtAt) < mentionIndexTTL {
		return m.mentionIdx
	}
	root := m.mentionRoot
	if root == "" {
		wd, err := os.Getwd()
		if err != nil {
			return m.mentionIdx // stale beats nothing; nil stays nil
		}
		root = wd
	}
	m.mentionIdx = buildMentionIndex(root, true)
	return m.mentionIdx
}

// mentionVaultIndex is the tier-2 sibling, rooted at the configured
// vault. No gitignore pass: vaults are rarely git repos and LoadIgnorer
// would walk the (possibly network-mounted) tree a second time for
// nothing; .git is still pruned defensively by the walker itself.
func (m *Model) mentionVaultIndex() *mentionIndex {
	if m.vaultPath == "" {
		return nil
	}
	if m.mentionVaultIdx != nil && time.Since(m.mentionVaultIdx.builtAt) < mentionIndexTTL {
		return m.mentionVaultIdx
	}
	m.mentionVaultIdx = buildMentionIndex(m.vaultPath, false)
	return m.mentionVaultIdx
}

// buildMentionIndex walks root collecting candidate files. See the
// package comment for the bound set (cap / depth / binary-ext / skill
// exclusions).
func buildMentionIndex(root string, useGitignore bool) *mentionIndex {
	return buildMentionIndexCapped(root, useGitignore, mentionIndexCap)
}

// buildMentionIndexCapped is the cap-parameterized core, split out so
// tests can exercise truncation without creating 5000 files.
func buildMentionIndexCapped(root string, useGitignore bool, limit int) *mentionIndex {
	idx := &mentionIndex{root: root, builtAt: time.Now()}
	var ig tools.Ignorer
	if useGitignore {
		if loaded, err := tools.LoadIgnorer(root); err == nil {
			ig = loaded
		}
	}
	walkErr := tools.WalkRespectingGitignore(root, ig, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			// Unreadable entry: skip it rather than abort the index.
			return nil
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil || rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if info.IsDir() {
			if mentionSkipDir(rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if mentionSkipFile(rel) {
			return nil
		}
		if len(idx.files) >= limit {
			idx.truncated = true
			return errMentionIndexFull
		}
		idx.files = append(idx.files, rel)
		return nil
	})
	// errMentionIndexFull is the expected stop signal; any other walk
	// error still leaves a usable partial index (never silently empty:
	// the truncated flag is only set by the cap, and an error-shortened
	// walk degrades to fewer candidates, not wrong ones).
	_ = walkErr
	return idx
}

// mentionSkipDir prunes a directory from the index walk: skills dirs
// (coding-agent identity bias - skills are slash-command territory)
// and anything nested past mentionDepthCap.
func mentionSkipDir(rel string) bool {
	if stdpath.Base(rel) == "skills" {
		return true
	}
	return strings.Count(rel, "/")+1 >= mentionDepthCap
}

// mentionSkipFile drops non-mentionable files: SKILL.md (skill files
// are excluded wholesale) and binary payloads by extension.
func mentionSkipFile(rel string) bool {
	if stdpath.Base(rel) == "SKILL.md" {
		return true
	}
	return mentionBinaryExt[strings.ToLower(stdpath.Ext(rel))]
}

// handleMentionSuggestKey processes a keystroke while mention mode is
// active, mirroring handleSlashSuggestKey exactly: tab accepts the
// highlighted candidate (inserting the chip), ↑↓ navigate when more
// than one match exists (otherwise they stay ordinary textarea/history
// motion), esc dismisses without erasing the typed token, enter is NOT
// intercepted (it still sends, same as slash mode).
func (m *Model) handleMentionSuggestKey(key string) (tea.Cmd, bool) {
	if !m.mentionSuggest.open || m.readOnly {
		return nil, false
	}
	switch key {
	case "tab":
		m.acceptMention()
		return nil, true
	case "up":
		if len(m.mentionSuggest.matches) > 1 {
			m.mentionSuggest.cursorUp()
			return nil, true
		}
	case "down":
		if len(m.mentionSuggest.matches) > 1 {
			m.mentionSuggest.cursorDown()
			return nil, true
		}
	case "esc":
		m.mentionSuggest.dismiss()
		return nil, true
	}
	return nil, false
}

// acceptMention replaces the typed "@query" with one mention chip. The
// token is removed by replaying backspaces through the textarea (the
// composer's own atomic-edit trick, immune to desync), then InsertChip
// places the ‹m:ID› marker at the cursor. Text after the cursor that
// happened to continue the token is left alone - the query is defined
// as @..cursor, exactly what the user typed toward the completion.
func (m *Model) acceptMention() {
	sel, ok := m.mentionSuggest.selected()
	if !ok || m.composer == nil {
		return
	}
	n := utf8.RuneCountInString(m.mentionSuggest.query) + 1 // +1 for the '@'
	m.composer.replayKey(tea.KeyBackspace, n)
	m.composer.InsertChip(agent.Attachment{
		Kind:     agent.AttachmentMention,
		Path:     sel.path,
		Nickname: stdpath.Base(sel.display),
	})
	m.refreshSuggests()
}

// ----- hint band -------------------------------------------------------

// renderMentionHint paints the 3-row mention band in the hint-band
// slot, reusing the slash band's skin: candidate chips row, "↳ <path>"
// description for the highlighted candidate, keybind row. Returns ""
// when mention mode is off.
func renderMentionHint(s mentionSuggest, w int) string {
	if !s.open {
		return ""
	}
	const indent = "  "
	contentW := w - len(indent)
	if contentW < 20 {
		contentW = 20
	}
	rows := make([]string, 0, 3)
	if row := renderMentionChips(s, contentW); row != "" {
		rows = append(rows, indent+row)
	}
	if row := renderMentionDescription(s, contentW); row != "" {
		rows = append(rows, indent+row)
	}
	rows = append(rows, indent+renderMentionKeyHints())
	return strings.Join(rows, "\n")
}

// renderMentionChips lays out candidate BASENAMES as the sliding-window
// chip palette (full paths would blow the row; the description row
// carries the highlighted candidate's full path). Layout mirrors
// renderSlashChips: selected accent+bold, dim others, +N overflow
// markers, cursor cushion of 2. A single match collapses into the
// description row; zero matches warn.
func renderMentionChips(s mentionSuggest, w int) string {
	if len(s.matches) == 0 {
		warnStyle := lipgloss.NewStyle().Foreground(colorWarn)
		return warnStyle.Render(truncateRight("no files match @"+s.query, w))
	}
	if len(s.matches) == 1 {
		return ""
	}
	selStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(colorMuted)
	moreStyle := lipgloss.NewStyle().Foreground(colorSubtle)

	const cushion = 2
	start := 0
	if s.cursor > cushion {
		start = s.cursor - cushion
	}
	const sep = "  "
	used := 0
	parts := make([]string, 0, len(s.matches)+2)
	if start > 0 {
		left := moreStyle.Render("+" + itoa(start) + " · ")
		parts = append(parts, left)
		used += lipgloss.Width(left)
	}
	for i := start; i < len(s.matches); i++ {
		chip := stdpath.Base(s.matches[i].display)
		render := dimStyle.Render(chip)
		if i == s.cursor {
			render = selStyle.Render(chip)
		}
		width := lipgloss.Width(chip)
		if i > start {
			width += len(sep)
		}
		if used+width > w-6 {
			remaining := len(s.matches) - i
			parts = append(parts, moreStyle.Render(sep+"+"+itoa(remaining)))
			return strings.Join(parts, "")
		}
		if i > start {
			parts = append(parts, dimStyle.Render(sep))
		}
		parts = append(parts, render)
		used += width
	}
	return strings.Join(parts, "")
}

// renderMentionDescription is the "↳ <relative path>" row for the
// highlighted candidate, with the truncation notes dimly trailing:
// "showing N of M" when the match list was clipped, plus the index cap
// note when the walk was. The path flows through mentionLinkDisplay
// (the OSC 8 seam), truncated as plain text first so the link escapes
// are never cut.
func renderMentionDescription(s mentionSuggest, w int) string {
	sel, ok := s.selected()
	if !ok {
		return ""
	}
	glyphStyle := lipgloss.NewStyle().Foreground(colorAccent)
	pathStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	dimStyle := lipgloss.NewStyle().Foreground(colorSubtle)

	var notes []string
	if s.total > len(s.matches) {
		notes = append(notes, "showing "+itoa(len(s.matches))+" of "+itoa(s.total))
	}
	if s.note != "" {
		notes = append(notes, s.note)
	}
	tail := ""
	if len(notes) > 0 {
		tail = "  " + strings.Join(notes, " · ")
	}

	pathW := w - 2 - lipgloss.Width(tail) // 2 for the "↳ " glyph
	if pathW < 8 {
		pathW = 8
	}
	out := glyphStyle.Render("↳ ") +
		pathStyle.Render(mentionLinkDisplay(sel.display, truncateRight(sel.display, pathW)))
	if tail != "" {
		out += dimStyle.Render(tail)
	}
	return out
}

func renderMentionKeyHints() string {
	keyStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	dim := lipgloss.NewStyle().Foreground(colorSubtle)
	sep := dim.Render("  ·  ")
	return strings.Join([]string{
		keyStyle.Render("↑↓") + dim.Render(" select"),
		keyStyle.Render("tab") + dim.Render(" attach"),
		keyStyle.Render("enter") + dim.Render(" send"),
		keyStyle.Render("esc") + dim.Render(" cancel"),
	}, sep)
}

// ----- peek card -------------------------------------------------------

// renderMentionPeekCard paints the peek card for one mention chip:
//
//	┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄ mention
//	internal/tui/chat/chat.go
//	94.7 KB · modified 2026-06-12 10:31
//	┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄┄
//
// The file is stat-ed at peek time (relative paths resolve against the
// process cwd, vault mentions carry absolute paths); when it no longer
// exists the stats row is replaced by a plain-text warning so the
// signal survives NO_COLOR. Same sandlot rules as the other cards:
// dashed rules, dim italic corner tag, never a left color stripe. The
// path row goes through mentionLinkDisplay (OSC 8 seam, slice 9l).
func renderMentionPeekCard(att agent.Attachment, w int) string {
	const indent = "  "
	contentW := w - len(indent)
	if contentW < 20 {
		contentW = 20
	}

	dim := lipgloss.NewStyle().Foreground(colorMuted)
	tagStyle := lipgloss.NewStyle().Foreground(colorMuted).Italic(true)

	tag := truncateRight(string(agent.AttachmentMention), contentW-8)
	ruleW := contentW - lipgloss.Width(tag) - 1
	top := dim.Render(strings.Repeat("┄", ruleW)) + " " + tagStyle.Render(tag)

	target := att.Path
	if target == "" {
		target = chipLabel(att.Kind, att.Nickname)
	}
	pathRow := mentionLinkDisplay(target, truncateRight(target, contentW))

	var mid string
	if info, err := os.Stat(target); err == nil {
		mid = dim.Render(truncateRight(
			byteSizeLabel(int(info.Size()))+" · modified "+
				info.ModTime().Format("2006-01-02 15:04"), contentW))
	} else {
		mid = lipgloss.NewStyle().Foreground(colorWarn).Render(
			truncateRight("↳ file no longer exists", contentW))
	}

	return strings.Join([]string{
		indent + top,
		indent + pathRow,
		indent + mid,
		indent + dim.Render(strings.Repeat("┄", contentW)),
	}, "\n")
}
