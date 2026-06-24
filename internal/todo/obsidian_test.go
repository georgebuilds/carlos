package todo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// tempVault builds a throwaway vault directory seeded with the given
// relpath->content files and returns its absolute path.
func tempVault(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

// counterID returns a deterministic id generator for tests.
func counterID() func() string {
	n := 0
	return func() string {
		n++
		return fmt.Sprintf("todo-t%d", n)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	return string(b)
}

func TestObsidian_ListOnlyManagedTasks(t *testing.T) {
	vault := tempVault(t, map[string]string{
		"work/todos.md": "# Work\n\n- [ ] managed task ^todo-1\n- [ ] freeform no id\n- [x] done managed ^todo-2\n",
	})
	s := NewObsidianStore(vault)
	items, err := s.List(context.Background(), Query{Scopes: []Scope{{Frame: "work", Subtree: "work"}}, Filter: FilterAll})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 managed items (freeform skipped), got %d: %+v", len(items), items)
	}
	if items[0].Frame != "work" || items[0].Backend != "obsidian" {
		t.Errorf("label wrong: %+v", items[0])
	}
	if !strings.HasPrefix(items[0].Source, "work/todos.md:") {
		t.Errorf("source = %q", items[0].Source)
	}
}

func TestObsidian_FilterOpenVsDone(t *testing.T) {
	vault := tempVault(t, map[string]string{
		"todos.md": "- [ ] open one ^todo-1\n- [x] done one ^todo-2\n",
	})
	s := NewObsidianStore(vault)
	open, _ := s.List(context.Background(), Query{Filter: FilterOpen})
	if len(open) != 1 || open[0].ID != "todo-1" {
		t.Errorf("open filter wrong: %+v", open)
	}
	done, _ := s.List(context.Background(), Query{Filter: FilterDone})
	if len(done) != 1 || done[0].ID != "todo-2" {
		t.Errorf("done filter wrong: %+v", done)
	}
}

func TestObsidian_SubtreeNestingSeesChildren(t *testing.T) {
	vault := tempVault(t, map[string]string{
		"work/todos.md":          "- [ ] top-level work ^todo-1\n",
		"work/projectx/notes.md": "- [ ] nested in child folder ^todo-2\n",
		"personal/todos.md":      "- [ ] personal item ^todo-3\n",
	})
	s := NewObsidianStore(vault)
	items, _ := s.List(context.Background(), Query{Scopes: []Scope{{Frame: "work", Subtree: "work"}}, Filter: FilterAll})
	if len(items) != 2 {
		t.Fatalf("work frame should see its own + child folder (2), got %d: %+v", len(items), items)
	}
	// personal/ must NOT leak into the work subtree.
	for _, it := range items {
		if it.ID == "todo-3" {
			t.Error("personal item leaked into work subtree")
		}
	}
}

func TestObsidian_AddCreatesInboxAndID(t *testing.T) {
	vault := tempVault(t, nil)
	s := NewObsidianStore(vault, WithIDFunc(counterID()))
	it, err := s.Add(context.Background(), Scope{Frame: "personal", Subtree: "personal"}, Draft{Text: "write the spec", Due: "2026-07-01", Tags: []string{"carlos"}})
	if err != nil {
		t.Fatal(err)
	}
	if it.ID != "todo-t1" {
		t.Errorf("id = %q", it.ID)
	}
	body := readFile(t, filepath.Join(vault, "personal", "todos.md"))
	if !strings.Contains(body, "- [ ] write the spec 📅 2026-07-01 #carlos ^todo-t1") {
		t.Errorf("inbox body wrong:\n%s", body)
	}
	// Round-trip: List should now find it.
	items, _ := s.List(context.Background(), Query{Scopes: []Scope{{Frame: "personal", Subtree: "personal"}}})
	if len(items) != 1 || items[0].Due != "2026-07-01" {
		t.Errorf("list after add wrong: %+v", items)
	}
}

func TestObsidian_AddAppendsWithoutBlankGap(t *testing.T) {
	vault := tempVault(t, map[string]string{
		"todos.md": "- [ ] first ^todo-1\n",
	})
	s := NewObsidianStore(vault, WithIDFunc(counterID()))
	if _, err := s.Add(context.Background(), Scope{}, Draft{Text: "second"}); err != nil {
		t.Fatal(err)
	}
	body := readFile(t, filepath.Join(vault, "todos.md"))
	want := "- [ ] first ^todo-1\n- [ ] second ^todo-t1\n"
	if body != want {
		t.Errorf("body =\n%q\nwant\n%q", body, want)
	}
}

func TestObsidian_CompletePreservesFormatting(t *testing.T) {
	vault := tempVault(t, map[string]string{
		"todos.md": "- [ ] ship it 📅 2026-06-25 #work ^todo-1\n",
	})
	s := NewObsidianStore(vault)
	it, err := s.Complete(context.Background(), Scope{}, "todo-1")
	if err != nil {
		t.Fatal(err)
	}
	if !it.Done {
		t.Error("returned item not done")
	}
	body := strings.TrimRight(readFile(t, filepath.Join(vault, "todos.md")), "\n")
	// Only the checkbox char should change; due + tag + id stay in place.
	if body != "- [x] ship it 📅 2026-06-25 #work ^todo-1" {
		t.Errorf("complete reformatted the line: %q", body)
	}
}

func TestObsidian_CompleteAcceptsCaretPrefixedID(t *testing.T) {
	vault := tempVault(t, map[string]string{"todos.md": "- [ ] x ^todo-1\n"})
	s := NewObsidianStore(vault)
	if _, err := s.Complete(context.Background(), Scope{}, "^todo-1"); err != nil {
		t.Fatalf("caret-prefixed id should resolve: %v", err)
	}
}

func TestObsidian_UpdatePatch(t *testing.T) {
	vault := tempVault(t, map[string]string{"todos.md": "- [ ] old text ^todo-1\n"})
	s := NewObsidianStore(vault)
	newText := "new text"
	due := "2026-08-08"
	it, err := s.Update(context.Background(), Scope{}, "todo-1", Patch{Text: &newText, Due: &due})
	if err != nil {
		t.Fatal(err)
	}
	if it.Text != "new text" || it.Due != "2026-08-08" {
		t.Errorf("patch result wrong: %+v", it)
	}
	body := readFile(t, filepath.Join(vault, "todos.md"))
	if !strings.Contains(body, "new text") || !strings.Contains(body, "📅 2026-08-08") || !strings.Contains(body, "^todo-1") {
		t.Errorf("update body wrong:\n%s", body)
	}
}

func TestObsidian_MutateNotFound(t *testing.T) {
	vault := tempVault(t, map[string]string{"todos.md": "- [ ] x ^todo-1\n"})
	s := NewObsidianStore(vault)
	if _, err := s.Complete(context.Background(), Scope{}, "todo-nope"); err != ErrNotFound {
		t.Errorf("want ErrNotFound, got %v", err)
	}
}

func TestObsidian_ListMissingSubtreeIsEmpty(t *testing.T) {
	vault := tempVault(t, nil)
	s := NewObsidianStore(vault)
	items, err := s.List(context.Background(), Query{Scopes: []Scope{{Frame: "ghost", Subtree: "ghost"}}})
	if err != nil {
		t.Fatalf("missing subtree should not error: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("want empty, got %+v", items)
	}
}

// Regression for the concurrency fix: many parallel Add calls to the same
// inbox must not lose any task (the per-vault lock serialises the
// read-modify-write so no writer clobbers another).
func TestObsidian_ConcurrentAddsNoLostUpdates(t *testing.T) {
	vault := tempVault(t, nil)
	var counter int64
	var mu sync.Mutex
	id := func() string {
		mu.Lock()
		defer mu.Unlock()
		counter++
		return fmt.Sprintf("todo-c%d", counter)
	}
	s := NewObsidianStore(vault, WithIDFunc(id))

	const n = 25
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Add(context.Background(), Scope{}, Draft{Text: fmt.Sprintf("task %d", i)})
			if err != nil {
				t.Errorf("add: %v", err)
			}
		}(i)
	}
	wg.Wait()

	items, err := s.List(context.Background(), Query{Filter: FilterAll})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != n {
		t.Errorf("want %d items after concurrent adds, got %d (lost update)", n, len(items))
	}
}

// recordingInval captures invalidation calls.
type recordingInval struct{ paths []string }

func (r *recordingInval) ResetPath(p string) { r.paths = append(r.paths, p) }

func TestObsidian_InvalidatesAfterWrite(t *testing.T) {
	vault := tempVault(t, nil)
	rec := &recordingInval{}
	s := NewObsidianStore(vault, WithInvalidator(rec), WithIDFunc(counterID()))
	if _, err := s.Add(context.Background(), Scope{}, Draft{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if len(rec.paths) != 1 || rec.paths[0] != vault {
		t.Errorf("expected one invalidation of the vault, got %v", rec.paths)
	}
}

func TestObsidian_AddUniqueIDAvoidsCollision(t *testing.T) {
	vault := tempVault(t, map[string]string{"todos.md": "- [ ] existing ^todo-dup\n"})
	// idFunc returns the colliding id once, then a fresh one.
	calls := 0
	s := NewObsidianStore(vault, WithIDFunc(func() string {
		calls++
		if calls == 1 {
			return "todo-dup"
		}
		return "todo-fresh"
	}))
	it, err := s.Add(context.Background(), Scope{}, Draft{Text: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if it.ID != "todo-fresh" {
		t.Errorf("collision not avoided: id = %q", it.ID)
	}
}
