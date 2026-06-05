// Package notes is the Obsidian-flavored markdown indexer that backs the
// notes_* tool family. One package, one purpose: open a vault directory,
// parse every .md inside it, and answer structured queries (get / search
// / backlinks / tagged / neighbors / recent / resolve) cheaply.
//
// Design commitments (mirror the Phase 12 proposal):
//
//   - One VaultIndex per vault root. Each root is opened lazily on first
//     reference via Cache.Open and cached in a process-wide sync.Map. No
//     eager scan at startup.
//   - Wikilink resolution is vault-local. `[[note]]` in vault A NEVER
//     reaches into vault B. The Cache holds independent indexes per path.
//   - Frontmatter is first-class: title / aliases / tags / description
//     are surfaced through structured fields, not raw YAML.
//   - Outline is just "# headings" — Obsidian callouts, Dataview blocks,
//     etc. fall through to body text. v0 keeps the surface narrow.
package notes

import (
	"errors"
	"time"
)

// ErrNotFound is returned by VaultIndex.Get + the link resolver when a
// note name cannot be resolved within the queried vault.
var ErrNotFound = errors.New("notes: note not found")

// ErrNoVaultConfigured is returned by resolveVaultPath when neither the
// per-call override nor cfg.Vault.Path is set. The tools surface this as
// the "vault not configured" error envelope.
var ErrNoVaultConfigured = errors.New("notes: vault not configured")

// Note is the parsed view of a single markdown file inside a vault.
//
// All slice + map fields are populated by parseNote; nil-valued slices
// are normalised to empty (never nil) so JSON encoding produces `[]`
// rather than `null` for empty collections.
type Note struct {
	// Path is the note's relative path from the vault Root, using
	// forward slashes regardless of host OS.
	Path string
	// Title is the display title — frontmatter `title:` if present,
	// otherwise the filename without the .md extension.
	Title string
	// Aliases is the frontmatter `aliases:` list (strings only;
	// non-string entries are ignored).
	Aliases []string
	// Tags is the union of frontmatter `tags:` + inline `#tag` matches
	// in the body, deduplicated, no leading `#`.
	Tags []string
	// Frontmatter is the parsed YAML map at the head of the file
	// (nil if no frontmatter was present).
	Frontmatter map[string]any
	// Headings is the ordered list of `#`-prefixed headings in the
	// body, in source order.
	Headings []Heading
	// Links is the ordered list of outgoing `[[wikilink]]` references
	// in the body, in source order. Target resolution is filled in by
	// the indexer's second pass; before that it stays empty.
	Links []Link
	// Backlinks is the set of incoming wikilinks pointing at this
	// note, populated by the indexer's third pass.
	Backlinks []Link
	// ModTime is the file's last-modified timestamp.
	ModTime time.Time
	// Size is the file size in bytes.
	Size int64

	// body is the markdown body (post-frontmatter) for paragraph
	// snippet extraction. Kept private; tools that need the full
	// markdown source re-read the file via os.ReadFile so we don't
	// hold every byte of every note in memory.
	body string
	// bodyOffset is the byte offset where the body starts in the
	// on-disk file (after the closing `---` of the frontmatter or 0
	// if no frontmatter was present). Reserved for future incremental
	// re-parsing; not used in v0 query paths.
	bodyOffset int
}

// Heading is one `#`-prefixed line in the source.
type Heading struct {
	Level int    // 1-6 for `#` through `######`
	Text  string // heading text, trimmed
	Line  int    // 1-indexed source line
}

// Link is one `[[wikilink]]` occurrence in source.
//
// Target is the resolved relpath inside the same vault, or "" if the
// link couldn't be resolved (typo / future note / target in another
// vault). The model uses unresolved targets as a "do not follow" hint
// via the notes_neighbors response.
type Link struct {
	// Target is the resolved relpath (e.g. "carlos/mvp-roadmap.md"),
	// or "" when unresolved. Populated by VaultIndex.resolveLinks
	// during build.
	Target string
	// Source is the relpath of the note that contains this link.
	// Populated for backlink slices; outgoing-link entries leave it
	// empty since the parent Note's Path already identifies the source.
	Source string
	// Display is the alias from `[[target|display]]`; equals the raw
	// target text when no alias is present.
	Display string
	// Section is the heading text from `[[target#heading]]`; empty
	// when no `#` segment was present.
	Section string
	// Line is the 1-indexed source line of the wikilink.
	Line int
	// Context is a single-line excerpt around the wikilink, populated
	// for backlink responses so the model can see how a note refers
	// to its target without re-reading the source file.
	Context string
}
