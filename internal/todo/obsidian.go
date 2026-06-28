package todo

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
)

// vaultMu serialises read-modify-write mutations per vault root across every
// ObsidianStore in this process. Without it, two concurrent Add/Complete/Update
// calls touching the same file could each read the old contents and the later
// writer would silently drop the earlier writer's change. Reads (List) do not
// take the lock: writeFileAtomic publishes via os.Rename, so a concurrent List
// always observes either the whole old file or the whole new one, never a torn
// mix. (Cross-process writers still rely on that atomic-rename property;
// last-writer-wins, but never corruption.)
var vaultMu sync.Map // vault root → *sync.Mutex

// lockVault acquires the per-root mutex and returns its unlock func.
func lockVault(root string) func() {
	m, _ := vaultMu.LoadOrStore(root, &sync.Mutex{})
	mu, ok := m.(*sync.Mutex)
	if !ok {
		// vaultMu only ever stores *sync.Mutex; this guards against a
		// future store of a different type rather than panicking deep in
		// a lock path.
		panic(fmt.Sprintf("lockVault: vaultMu holds %T, want *sync.Mutex", m))
	}
	mu.Lock()
	return mu.Unlock
}

// ObsidianStore is the out-of-the-box backend: todos live as standard
// `- [ ] task` checkbox lines inside the user's Obsidian vault, scoped to a
// frame's vault_subtree. Because a subtree is a folder prefix, a frame
// transparently "sees" the todos in every descendant note, which is how the
// targeted lens shows a frame and its children.
//
// Reads walk the subtree and parse checkbox lines; writes mutate the single
// owning file and rewrite it atomically (temp + fsync + rename), mirroring the
// notes_write recipe so a crash mid-write never corrupts a note. After every
// write the optional Invalidator drops the notes cache entry so notes_search /
// notes_get reflect the change immediately.
type ObsidianStore struct {
	// vault is the absolute, cleaned vault root.
	vault string
	// inbox is the vault-relative filename (within a scope's subtree) that
	// Add appends to, e.g. "todos.md".
	inbox string
	// inval is invoked with the vault path after each write so a shared
	// notes.Cache re-parses. Nil is fine (writes still land on disk).
	inval Invalidator
	// newID mints block-ref slugs; injectable so tests get deterministic ids.
	newID func() string
}

// Invalidator is the subset of *notes.Cache the store needs: drop the cached
// index for a vault path so the next read re-walks. Declared here so the todo
// package does not import the notes package.
type Invalidator interface {
	ResetPath(path string)
}

// ObsidianOption configures an ObsidianStore.
type ObsidianOption func(*ObsidianStore)

// WithInbox overrides the default "todos.md" inbox filename.
func WithInbox(name string) ObsidianOption {
	return func(s *ObsidianStore) {
		if strings.TrimSpace(name) != "" {
			s.inbox = name
		}
	}
}

// WithInvalidator wires a notes-cache invalidator called after each write.
func WithInvalidator(inv Invalidator) ObsidianOption {
	return func(s *ObsidianStore) { s.inval = inv }
}

// WithIDFunc overrides the block-ref id generator (tests inject a counter).
func WithIDFunc(fn func() string) ObsidianOption {
	return func(s *ObsidianStore) {
		if fn != nil {
			s.newID = fn
		}
	}
}

// NewObsidianStore builds a store over the vault rooted at vaultPath.
func NewObsidianStore(vaultPath string, opts ...ObsidianOption) *ObsidianStore {
	s := &ObsidianStore{
		vault: filepath.Clean(vaultPath),
		inbox: "todos.md",
		newID: NewID,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Name identifies this backend.
func (*ObsidianStore) Name() string { return "obsidian" }

// List walks each scope's subtree, parses checkbox lines, and returns the
// items matching the filter, ordered by file path then line. An empty Scopes
// slice scans the whole vault under a blank frame label.
func (s *ObsidianStore) List(ctx context.Context, q Query) ([]Item, error) {
	scopes := q.Scopes
	if len(scopes) == 0 {
		scopes = []Scope{{}}
	}
	filter := q.Filter
	if filter == "" {
		filter = FilterOpen
	}
	var out []Item
	for _, sc := range scopes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		items, err := s.listScope(sc, filter)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
	}
	return out, nil
}

// listScope walks one subtree.
func (s *ObsidianStore) listScope(sc Scope, filter Filter) ([]Item, error) {
	subtree := cleanSubtree(sc.Subtree)
	root := s.vault
	if subtree != "" {
		root = filepath.Join(s.vault, subtree)
	}
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // no notes yet in this subtree
		}
		return nil, fmt.Errorf("todo: stat %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, nil
	}

	var out []Item
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(p), ".md") {
			return nil
		}
		rel := relSlash(s.vault, p)
		lines, err := readLines(p)
		if err != nil {
			return err
		}
		for i, line := range lines {
			tl, ok := parseTaskLine(line)
			if !ok || tl.ID == "" {
				// Only carlos-managed tasks (those carrying a block-ref id)
				// are surfaced; freeform checkboxes the user wrote by hand
				// without an id are left alone so we never claim ownership of
				// arbitrary list items.
				continue
			}
			if !filter.match(tl.Done) {
				continue
			}
			source := fmt.Sprintf("%s:%d", rel, i+1)
			out = append(out, tl.item(sc.Frame, s.Name(), source))
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("todo: walk %s: %w", root, walkErr)
	}
	return out, nil
}

// Add appends a new task to the scope's inbox file and returns it.
func (s *ObsidianStore) Add(_ context.Context, sc Scope, draft Draft) (Item, error) {
	if strings.TrimSpace(draft.Text) == "" {
		return Item{}, fmt.Errorf("todo: empty task text")
	}
	defer lockVault(s.vault)()
	subtree := cleanSubtree(sc.Subtree)
	rel := s.inbox
	if subtree != "" {
		rel = path.Join(subtree, s.inbox)
	}
	target := filepath.Join(s.vault, filepath.FromSlash(rel))
	if !isInside(target, s.vault) {
		return Item{}, fmt.Errorf("todo: inbox %s escapes vault", target)
	}

	lines, err := readLinesAllowMissing(target)
	if err != nil {
		return Item{}, err
	}
	id := s.mintUniqueID(lines)
	tl := newTaskLine(draft, id)
	lines = appendTaskLine(lines, tl.render())

	if err := writeFileAtomic(target, strings.Join(lines, "\n")); err != nil {
		return Item{}, err
	}
	s.invalidate()
	source := fmt.Sprintf("%s:%d", rel, len(lines))
	return tl.item(sc.Frame, s.Name(), source), nil
}

// Complete flips the task with id to done, preserving the line's exact
// formatting (only the checkbox character changes).
func (s *ObsidianStore) Complete(_ context.Context, sc Scope, id string) (Item, error) {
	return s.mutate(sc, id, func(line string) (string, taskLine, bool) {
		raw, tl, ok := toggleRawLine(line, true)
		return raw, tl, ok
	})
}

// Update applies a partial patch to the task with id. Because the content can
// change, the line is re-rendered canonically (id and list marker preserved).
func (s *ObsidianStore) Update(_ context.Context, sc Scope, id string, patch Patch) (Item, error) {
	return s.mutate(sc, id, func(line string) (string, taskLine, bool) {
		tl, ok := parseTaskLine(line)
		if !ok {
			return line, taskLine{}, false
		}
		tl.applyPatch(patch)
		return tl.render(), tl, true
	})
}

// mutate finds the single line carrying id within the scope, applies fn to it,
// rewrites the owning file atomically, and returns the resulting Item.
func (s *ObsidianStore) mutate(sc Scope, id string, fn func(line string) (string, taskLine, bool)) (Item, error) {
	id = strings.TrimSpace(strings.TrimPrefix(id, "^"))
	if id == "" {
		return Item{}, fmt.Errorf("todo: empty id")
	}
	defer lockVault(s.vault)()
	subtree := cleanSubtree(sc.Subtree)
	root := s.vault
	if subtree != "" {
		root = filepath.Join(s.vault, subtree)
	}

	var (
		foundFile string
		foundRel  string
		foundLine int
		foundItem Item
		done      bool
	)
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if done || d.IsDir() || !strings.EqualFold(filepath.Ext(p), ".md") {
			return nil
		}
		lines, rerr := readLines(p)
		if rerr != nil {
			return rerr
		}
		for i, line := range lines {
			tl, ok := parseTaskLine(line)
			if !ok || tl.ID != id {
				continue
			}
			newLine, newTL, applied := fn(line)
			if !applied {
				continue
			}
			lines[i] = newLine
			if werr := writeFileAtomic(p, strings.Join(lines, "\n")); werr != nil {
				return werr
			}
			foundFile = p
			foundRel = relSlash(s.vault, p)
			foundLine = i + 1
			foundItem = newTL.item(sc.Frame, s.Name(), fmt.Sprintf("%s:%d", foundRel, foundLine))
			done = true
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil && walkErr != filepath.SkipAll {
		return Item{}, fmt.Errorf("todo: scan for %s: %w", id, walkErr)
	}
	if !done {
		return Item{}, ErrNotFound
	}
	_ = foundFile
	s.invalidate()
	return foundItem, nil
}

// mintUniqueID returns a fresh id not already present among lines.
func (s *ObsidianStore) mintUniqueID(lines []string) string {
	existing := map[string]bool{}
	for _, line := range lines {
		if tl, ok := parseTaskLine(line); ok && tl.ID != "" {
			existing[tl.ID] = true
		}
	}
	for {
		id := s.newID()
		if !existing[id] {
			return id
		}
	}
}

func (s *ObsidianStore) invalidate() {
	if s.inval != nil {
		s.inval.ResetPath(s.vault)
	}
}

// toggleRawLine flips only the checkbox state of a parsed task line, returning
// the rewritten line plus the parsed form. ok is false for non-task lines.
func toggleRawLine(line string, done bool) (string, taskLine, bool) {
	stripped := strings.TrimRight(line, "\r")
	m := checkboxRe.FindStringSubmatch(stripped)
	if m == nil {
		return line, taskLine{}, false
	}
	state := " "
	if done {
		state = "x"
	}
	rebuilt := m[1] + m[2] + " [" + state + "] " + m[4]
	tl, _ := parseTaskLine(rebuilt)
	return rebuilt, tl, true
}

// appendTaskLine adds a task line to a file body, ensuring it lands on its own
// line and the file does not accumulate a trailing blank gap.
func appendTaskLine(lines []string, rendered string) []string {
	// Drop a single trailing empty line (from a file ending in "\n") so the
	// new task appends cleanly rather than after a blank.
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return append(lines, rendered)
}
