package notes

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// VaultIndex is the per-vault parsed state. Construction goes through
// Cache.Open - callers should not instantiate VaultIndex directly so
// the lazy + idempotent-open contract holds.
type VaultIndex struct {
	// Root is the absolute, cleaned path to the vault root.
	Root string

	mu       sync.RWMutex
	notes    map[string]*Note   // relpath → note
	byTitle  map[string][]*Note // lowercase title (or alias) → notes
	tags     map[string][]*Note // tag (no `#`) → notes
	excludes []string           // shared glob patterns from the cache
}

// SearchOptions configures VaultIndex.Search.
type SearchOptions struct {
	// Tag, when non-empty, restricts results to notes carrying this
	// tag (with or without leading `#`).
	Tag string
	// Where, when non-nil, restricts results to notes whose
	// frontmatter has the listed key=value pairs. Values are compared
	// via fmt.Sprintf("%v") so YAML numbers/strings/bools all work.
	Where map[string]any
	// Limit caps the number of matches. 0 = use the default (10).
	// A negative value is treated as 0.
	Limit int
}

// Match is one notes_search hit.
type Match struct {
	Path    string
	Title   string
	Snippet string
	Line    int
	Score   float64
}

// Backlink is one entry in a notes_backlinks response.
type Backlink struct {
	Path    string
	Title   string
	Context string
	Line    int
}

// newVaultIndex parses every .md under root and returns the populated
// index. Walks the tree once, skipping excluded paths.
func newVaultIndex(root string, excludes []string) (*VaultIndex, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("notes: abs %s: %w", root, err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("notes: stat %s: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("notes: %s is not a directory", abs)
	}
	vi := &VaultIndex{
		Root:     abs,
		notes:    map[string]*Note{},
		byTitle:  map[string][]*Note{},
		tags:     map[string][]*Note{},
		excludes: excludes,
	}
	if err := vi.scan(); err != nil {
		return nil, err
	}
	return vi, nil
}

// scan walks the vault tree, parses each .md, and rebuilds the
// title/tag/backlink indices. Acquires the write lock for the duration
// so concurrent reads see a consistent snapshot.
func (v *VaultIndex) scan() error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.notes = map[string]*Note{}
	v.byTitle = map[string][]*Note{}
	v.tags = map[string][]*Note{}

	err := filepath.WalkDir(v.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Honor excludes on directory entries: pruning here
			// saves descending into ignored subtrees entirely.
			rel := relPath(v.Root, path)
			if rel != "" && rel != "." && isExcluded(rel+"/", v.excludes) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		rel := relPath(v.Root, path)
		if isExcluded(rel, v.excludes) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		n, perr := parseFile(path, rel, info)
		if perr != nil {
			// Parse errors are non-fatal: skip the bad file but
			// continue indexing the rest of the vault. The model
			// can still query everything else.
			return nil
		}
		v.notes[rel] = n
		return nil
	})
	if err != nil {
		return err
	}
	v.rebuildIndices()
	return nil
}

// rebuildIndices recomputes byTitle / tags / Backlinks from v.notes.
// Held under the write lock by callers; package-private so only
// scan / refreshOne invoke it.
func (v *VaultIndex) rebuildIndices() {
	v.byTitle = map[string][]*Note{}
	v.tags = map[string][]*Note{}

	// Title + alias index. Lowercase keys for case-insensitive lookup.
	for _, n := range v.notes {
		titleKey := strings.ToLower(n.Title)
		v.byTitle[titleKey] = append(v.byTitle[titleKey], n)
		for _, a := range n.Aliases {
			aKey := strings.ToLower(a)
			if aKey != titleKey {
				v.byTitle[aKey] = append(v.byTitle[aKey], n)
			}
		}
		for _, t := range n.Tags {
			v.tags[t] = append(v.tags[t], n)
		}
		// Clear any stale backlinks; we recompute below.
		n.Backlinks = n.Backlinks[:0]
	}

	// Resolve outgoing links + build backlinks. Walk every note's
	// raw links and rewrite Target to the resolved relpath (or "" for
	// unresolved). When a target resolves, append a Link to the
	// target's Backlinks with Source=this note's path.
	for srcPath, n := range v.notes {
		for i, l := range n.Links {
			resolved, _ := v.resolveLink(l.Target)
			n.Links[i].Target = resolved
			if resolved == "" {
				continue
			}
			target := v.notes[resolved]
			if target == nil {
				continue
			}
			b := Link{
				Target:  resolved,
				Source:  srcPath,
				Display: l.Display,
				Section: l.Section,
				Line:    l.Line,
				Context: l.Context,
			}
			target.Backlinks = append(target.Backlinks, b)
		}
	}
}

// resolveLink applies Obsidian's resolution rules. Returns the resolved
// relpath ("" for unresolved) and the list of candidate notes for the
// ambiguous case (used by notes_resolve).
//
// Algorithm (mirror Obsidian):
//  1. Exact relpath match (with or without `.md` suffix).
//  2. Case-insensitive title or alias lookup. 1 hit → resolved. Multi →
//     shortest relpath wins; ties broken alphabetically.
//  3. Otherwise unresolved.
func (v *VaultIndex) resolveLink(raw string) (string, []*Note) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	// 1. Exact path match. Try as-is and with the `.md` suffix added.
	if n, ok := v.notes[raw]; ok {
		return n.Path, []*Note{n}
	}
	withExt := raw + ".md"
	if n, ok := v.notes[withExt]; ok {
		return n.Path, []*Note{n}
	}
	// Trailing-slash defense: Obsidian doesn't write these but the
	// model might.
	withExt = strings.TrimSuffix(raw, "/") + ".md"
	if n, ok := v.notes[withExt]; ok {
		return n.Path, []*Note{n}
	}

	// 2. Title / alias lookup.
	key := strings.ToLower(filepath.Base(raw))
	key = strings.TrimSuffix(key, ".md")
	hits := v.byTitle[key]
	if len(hits) == 0 {
		return "", nil
	}
	if len(hits) == 1 {
		return hits[0].Path, hits
	}
	// Multi: shortest path wins; ties alphabetical.
	cands := append([]*Note(nil), hits...)
	sort.Slice(cands, func(i, j int) bool {
		li, lj := len(cands[i].Path), len(cands[j].Path)
		if li != lj {
			return li < lj
		}
		return cands[i].Path < cands[j].Path
	})
	return cands[0].Path, cands
}

// Get resolves name to a Note via Obsidian's shortest-unique-path
// algorithm. Returns ErrNotFound when no candidate matches.
func (v *VaultIndex) Get(name string) (*Note, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	resolved, _ := v.resolveLink(name)
	if resolved == "" {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	return v.notes[resolved], nil
}

// Resolve is the verbose form of resolveLink - surfaces the candidate
// list so notes_resolve can report ambiguity.
//
// Resolve returns ALL candidates whose title (or alias) matches the
// link, even when one of them happens to share the relpath. This lets
// notes_resolve flag ambiguity for a query like "notes" that maps to
// both `notes.md` and `sub/notes.md` - the resolver in resolveLink
// short-circuits on exact-path for plain Get / link-rewriting where
// the model already committed to a target. Resolve is the introspective
// surface, so it widens the candidate set.
func (v *VaultIndex) Resolve(link string) (string, []*Note, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	// Strip enclosing brackets so a model that copy-pastes "[[X]]"
	// gets the same result as a clean "X".
	link = strings.TrimSpace(link)
	link = strings.TrimPrefix(link, "[[")
	link = strings.TrimSuffix(link, "]]")
	// Drop any section anchor - resolution is at the note level.
	if i := strings.Index(link, "#"); i >= 0 {
		link = link[:i]
	}
	resolved, _ := v.resolveLink(link)
	if resolved == "" {
		return "", nil, fmt.Errorf("%w: %q", ErrNotFound, link)
	}
	// Widen the candidate set: any note whose title or alias
	// matches the lowercase basename also counts as a candidate, so
	// the caller can decide whether the (single-shortest) resolved
	// pick is actually unambiguous.
	key := strings.ToLower(filepath.Base(link))
	key = strings.TrimSuffix(key, ".md")
	cands := append([]*Note(nil), v.byTitle[key]...)
	if len(cands) == 0 {
		// The resolved path was found via exact-path lookup; fall
		// back to a single-candidate list so the caller still sees
		// the resolved note.
		if n, ok := v.notes[resolved]; ok {
			cands = []*Note{n}
		}
	}
	// Dedup + sort shortest-first / alpha-tie like resolveLink does.
	sort.Slice(cands, func(i, j int) bool {
		li, lj := len(cands[i].Path), len(cands[j].Path)
		if li != lj {
			return li < lj
		}
		return cands[i].Path < cands[j].Path
	})
	return resolved, cands, nil
}

// Search runs a case-insensitive substring search across body text and
// frontmatter scalars. Snippets are paragraph-bounded (split on blank
// lines or hard wraps).
//
// Scoring is intentionally cheap: tag-hit + title-hit + body-hit all
// contribute, with title weighted heaviest. v0 prioritises predictability
// over BM25-style ranking; we revisit if real usage shows the simple
// scheme is too noisy.
func (v *VaultIndex) Search(query string, opts SearchOptions) ([]Match, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil, fmt.Errorf("notes: empty query")
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	tagFilter := strings.TrimPrefix(opts.Tag, "#")

	type scored struct {
		match Match
		note  *Note
	}
	var results []scored

	// Iterate in a deterministic order so tied scores produce stable
	// output across runs (map iteration order is randomized).
	paths := make([]string, 0, len(v.notes))
	for p := range v.notes {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, p := range paths {
		n := v.notes[p]
		if tagFilter != "" && !containsString(n.Tags, tagFilter) {
			continue
		}
		if !matchesWhere(n.Frontmatter, opts.Where) {
			continue
		}
		score, snippet, line := scoreNote(n, q)
		if score == 0 {
			continue
		}
		results = append(results, scored{
			match: Match{
				Path:    n.Path,
				Title:   n.Title,
				Snippet: snippet,
				Line:    line,
				Score:   score,
			},
			note: n,
		})
	}

	sort.SliceStable(results, func(i, j int) bool {
		return results[i].match.Score > results[j].match.Score
	})

	out := make([]Match, 0, len(results))
	for _, r := range results {
		out = append(out, r.match)
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Total returns the total result count BEFORE limit truncation. Used by
// the notes_search tool to surface "truncated: true" + a total field.
func (v *VaultIndex) SearchTotal(query string, opts SearchOptions) (int, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return 0, fmt.Errorf("notes: empty query")
	}
	tagFilter := strings.TrimPrefix(opts.Tag, "#")
	count := 0
	for _, n := range v.notes {
		if tagFilter != "" && !containsString(n.Tags, tagFilter) {
			continue
		}
		if !matchesWhere(n.Frontmatter, opts.Where) {
			continue
		}
		if score, _, _ := scoreNote(n, q); score > 0 {
			count++
		}
	}
	return count, nil
}

// Backlinks returns every Link pointing at the resolved target. The
// caller's `target` parameter is itself resolved via Obsidian rules so
// `notes_backlinks {"note": "carlos"}` works regardless of whether the
// caller passed a title or a path.
func (v *VaultIndex) Backlinks(target string, limit int) ([]Backlink, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	resolved, _ := v.resolveLink(target)
	if resolved == "" {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, target)
	}
	n := v.notes[resolved]
	if limit <= 0 {
		limit = 50
	}
	out := make([]Backlink, 0, len(n.Backlinks))
	for _, b := range n.Backlinks {
		src := v.notes[b.Source]
		if src == nil {
			continue
		}
		out = append(out, Backlink{
			Path:    src.Path,
			Title:   src.Title,
			Context: b.Context,
			Line:    b.Line,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Line < out[j].Line
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Tagged returns every note carrying tag (no leading `#`). Sorted by
// modtime descending so the model sees freshest first.
func (v *VaultIndex) Tagged(tag string, limit int) ([]*Note, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	t := strings.TrimPrefix(tag, "#")
	if t == "" {
		return nil, fmt.Errorf("notes: empty tag")
	}
	hits := v.tags[t]
	if limit <= 0 {
		limit = 50
	}
	out := append([]*Note(nil), hits...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].ModTime.After(out[j].ModTime)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Recent returns the most-recently-modified notes. since=0 disables the
// time cutoff; limit=0 defaults to 10. Sorted modtime descending.
func (v *VaultIndex) Recent(limit int, since time.Duration) []*Note {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if limit <= 0 {
		limit = 10
	}
	cutoff := time.Time{}
	if since > 0 {
		cutoff = time.Now().Add(-since)
	}
	out := make([]*Note, 0, len(v.notes))
	for _, n := range v.notes {
		if !cutoff.IsZero() && n.ModTime.Before(cutoff) {
			continue
		}
		out = append(out, n)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].ModTime.After(out[j].ModTime)
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// Neighbors returns outgoing + incoming neighbors of the named note,
// plus the list of unresolved outgoing wikilinks (so the model can
// surface "ghost links" without following them).
func (v *VaultIndex) Neighbors(name string) (out, in []*Note, unresolved []Link, err error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	resolved, _ := v.resolveLink(name)
	if resolved == "" {
		return nil, nil, nil, fmt.Errorf("%w: %q", ErrNotFound, name)
	}
	n := v.notes[resolved]

	seenOut := map[string]bool{}
	for _, l := range n.Links {
		if l.Target == "" {
			unresolved = append(unresolved, l)
			continue
		}
		if seenOut[l.Target] {
			continue
		}
		seenOut[l.Target] = true
		if t := v.notes[l.Target]; t != nil {
			out = append(out, t)
		}
	}
	seenIn := map[string]bool{}
	for _, b := range n.Backlinks {
		if seenIn[b.Source] {
			continue
		}
		seenIn[b.Source] = true
		if s := v.notes[b.Source]; s != nil {
			in = append(in, s)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	sort.SliceStable(in, func(i, j int) bool { return in[i].Path < in[j].Path })
	return out, in, unresolved, nil
}

// Refresh re-walks the vault from scratch. Called when the cheap mtime
// poll detects a change in directory structure (file added / removed).
func (v *VaultIndex) Refresh() error {
	return v.scan()
}

// MaybeRefresh re-parses only the files whose mtime or size changed
// since the last scan; new + deleted files trigger a full Refresh so
// the index covers them. Cheap on warm vaults - single os.Stat per
// note + a directory walk to detect new files.
//
// Returns nil if nothing changed.
func (v *VaultIndex) MaybeRefresh() error {
	v.mu.RLock()
	prev := make(map[string]struct {
		mt time.Time
		sz int64
	}, len(v.notes))
	for p, n := range v.notes {
		prev[p] = struct {
			mt time.Time
			sz int64
		}{n.ModTime, n.Size}
	}
	v.mu.RUnlock()

	// Walk the tree to find current files. Cheap: WalkDir doesn't
	// open any files, just stat the directory entries.
	current := map[string]os.FileInfo{}
	err := filepath.WalkDir(v.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			rel := relPath(v.Root, path)
			if rel != "" && rel != "." && isExcluded(rel+"/", v.excludes) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			return nil
		}
		rel := relPath(v.Root, path)
		if isExcluded(rel, v.excludes) {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		current[rel] = info
		return nil
	})
	if err != nil {
		return err
	}

	// Compare keysets. If they differ (add or remove), full scan.
	if len(current) != len(prev) {
		return v.scan()
	}
	for rel := range current {
		if _, ok := prev[rel]; !ok {
			return v.scan()
		}
	}

	// Same keyset; check mtime/size per file.
	changed := false
	for rel, info := range current {
		p := prev[rel]
		if !info.ModTime().Equal(p.mt) || info.Size() != p.sz {
			changed = true
			break
		}
	}
	if !changed {
		return nil
	}
	// At least one file changed; rebuild fully (re-parsing only the
	// changed files is a 12-d-or-later optimisation - for a 1000-note
	// vault a full re-walk is <100ms).
	return v.scan()
}

// scoreNote runs the cheap substring scorer + extracts a snippet. The
// scoring rules:
//
//   - +3.0 for a substring hit in the title
//   - +2.0 for any tag whose name contains q
//   - +1.0 for the first body-paragraph hit
//   - +0.1 per additional body hit (max 5 to dampen long-note bias)
//
// Returns score=0 + empty snippet when nothing matched. The snippet is
// ~200 chars of body context around the first body hit; if no body hit
// occurred but the title/tag scored, the snippet is the first non-empty
// body paragraph (so the model still has something to read).
func scoreNote(n *Note, q string) (float64, string, int) {
	score := 0.0
	if strings.Contains(strings.ToLower(n.Title), q) {
		score += 3.0
	}
	for _, t := range n.Tags {
		if strings.Contains(strings.ToLower(t), q) {
			score += 2.0
			break
		}
	}
	// Frontmatter scalar values are searchable so `where: {status:
	// alpha}` style queries via free-text still work.
	for _, v := range n.Frontmatter {
		if s, ok := v.(string); ok && strings.Contains(strings.ToLower(s), q) {
			score += 1.0
			break
		}
	}

	snippet, line, bodyHits := bodySnippet(n.body, q)
	if bodyHits > 0 {
		score += 1.0
		extra := float64(bodyHits-1) * 0.1
		if extra > 0.5 {
			extra = 0.5
		}
		score += extra
	}
	if score == 0 {
		return 0, "", 0
	}
	if snippet == "" {
		// Fallback: first non-empty paragraph.
		snippet, line = firstParagraph(n.body)
	}
	return score, snippet, line
}

// bodySnippet returns (snippet, line, count). Snippet is ~200 chars
// centered on the first occurrence of q. count reports total body hits
// so the caller can use it as an additional scoring signal.
func bodySnippet(body, q string) (string, int, int) {
	lower := strings.ToLower(body)
	idx := strings.Index(lower, q)
	if idx < 0 {
		return "", 0, 0
	}
	count := strings.Count(lower, q)

	// Expand to paragraph boundaries (blank line on either side) or
	// the configured cap, whichever comes first.
	const cap = 200
	start := idx - cap/2
	if start < 0 {
		start = 0
	}
	end := idx + len(q) + cap/2
	if end > len(body) {
		end = len(body)
	}
	// Trim back to nearest whitespace so we don't cut a word.
	for start > 0 && !isSpaceByte(body[start]) {
		start--
	}
	for end < len(body) && !isSpaceByte(body[end]) {
		end++
	}
	snippet := strings.TrimSpace(body[start:end])
	// Replace internal newlines with spaces so the response is a
	// single line of text.
	snippet = strings.ReplaceAll(snippet, "\n", " ")
	snippet = strings.Join(strings.Fields(snippet), " ")
	if len(snippet) > cap+40 {
		snippet = snippet[:cap+40]
	}
	line := strings.Count(body[:idx], "\n") + 1
	return snippet, line, count
}

// firstParagraph returns the first non-blank paragraph in body, with
// its starting line number. Used as a snippet fallback for title/tag-
// only matches.
func firstParagraph(body string) (string, int) {
	var b strings.Builder
	lines := strings.Split(body, "\n")
	startLine := 0
	in := false
	for i, l := range lines {
		tr := strings.TrimSpace(l)
		if tr == "" {
			if in {
				break
			}
			continue
		}
		if !in {
			startLine = i + 1
			in = true
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(tr)
		if b.Len() > 240 {
			break
		}
	}
	return strings.TrimSpace(b.String()), startLine
}

func isSpaceByte(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}

// matchesWhere reports whether fm satisfies every key=value pair in
// where. fmt.Sprintf("%v") comparison handles YAML's polymorphic
// scalar types without each caller writing the switch.
func matchesWhere(fm map[string]any, where map[string]any) bool {
	if len(where) == 0 {
		return true
	}
	for k, want := range where {
		got, ok := fm[k]
		if !ok {
			return false
		}
		if fmt.Sprintf("%v", got) != fmt.Sprintf("%v", want) {
			return false
		}
	}
	return true
}

// isExcluded reports whether rel (or a prefix of it) matches any of
// the configured glob patterns. Patterns may end with `**` to mean
// "this directory and everything under it".
func isExcluded(rel string, patterns []string) bool {
	rel = filepath.ToSlash(rel)
	for _, p := range patterns {
		p = filepath.ToSlash(p)
		if p == "" {
			continue
		}
		// `templates/**` → match anything starting with `templates/`.
		if strings.HasSuffix(p, "/**") {
			prefix := strings.TrimSuffix(p, "/**") + "/"
			if strings.HasPrefix(rel, prefix) {
				return true
			}
		}
		if matched, _ := filepath.Match(p, rel); matched {
			return true
		}
		// Single-segment globs against the basename too - Obsidian
		// users tend to write `*.draft.md` style patterns.
		if matched, _ := filepath.Match(p, filepath.Base(rel)); matched {
			return true
		}
	}
	return false
}

// relPath returns the vault-root-relative slash-separated path for an
// absolute file path. Returns "" if the file is not inside root (which
// shouldn't happen during WalkDir but defensively guard anyway).
func relPath(root, abs string) string {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}
