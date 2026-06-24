package todo

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/frame"
)

func TestBuildRouter_ObsidianDefault(t *testing.T) {
	vault := tempVault(t, map[string]string{"todos.md": "- [ ] x ^todo-1\n"})
	r, err := BuildRouter(vault, frame.Config{}, "", BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	items, err := r.Master(context.Background(), FilterOpen)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Errorf("default obsidian backend not wired: %+v", items)
	}
}

func TestBuildRouter_RESTBackend(t *testing.T) {
	vault := tempVault(t, nil)
	r, err := BuildRouter(vault, frame.Config{}, "", BuildOptions{
		Backends: []BackendSpec{{Name: "todoist", Type: "rest", BaseURL: "https://example.invalid"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.stores["todoist"]; !ok {
		t.Error("rest backend not registered")
	}
}

func TestBuildRouter_RejectsBadSpecs(t *testing.T) {
	vault := tempVault(t, nil)
	if _, err := BuildRouter(vault, frame.Config{}, "", BuildOptions{
		Backends: []BackendSpec{{Name: "obsidian", Type: "rest"}},
	}); err == nil {
		t.Error("reusing reserved name 'obsidian' should error")
	}
	if _, err := BuildRouter(vault, frame.Config{}, "", BuildOptions{
		Backends: []BackendSpec{{Name: "x", Type: "smoke-signal"}},
	}); err == nil {
		t.Error("unsupported backend type should error")
	}
}

func TestBuildRouter_ActiveOverride(t *testing.T) {
	vault := tempVault(t, map[string]string{
		"personal/todos.md": "- [ ] p ^todo-1\n",
		"work/todos.md":     "- [ ] w ^todo-2\n",
	})
	frames := frame.Config{
		Default: "personal",
		List: []frame.Frame{
			{Name: "personal", VaultSubtree: "personal"},
			{Name: "work", VaultSubtree: "work"},
		},
	}
	r, err := BuildRouter(vault, frames, "work", BuildOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Empty frame name should resolve to the session-active "work".
	items, err := r.Frame(context.Background(), "", FilterOpen)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "todo-2" {
		t.Errorf("active override not honoured: %+v", items)
	}
}

func TestRouter_CompleteAndUpdateRoute(t *testing.T) {
	vault := tempVault(t, map[string]string{"personal/todos.md": "- [ ] do ^todo-1\n"})
	obs := NewObsidianStore(vault)
	frames := frame.Config{
		Active: "personal",
		List:   []frame.Frame{{Name: "personal", VaultSubtree: "personal"}},
	}
	r := NewRouter(frames, "obsidian", map[string]Store{"obsidian": obs})

	upd, err := r.Update(context.Background(), "personal", "todo-1", Patch{Text: ptr("done soon")})
	if err != nil {
		t.Fatal(err)
	}
	if upd.Text != "done soon" {
		t.Errorf("router.Update wrong: %+v", upd)
	}
	cmp, err := r.Complete(context.Background(), "", "todo-1")
	if err != nil {
		t.Fatal(err)
	}
	if !cmp.Done {
		t.Errorf("router.Complete wrong: %+v", cmp)
	}
}

func TestRouter_CompleteUnknownBackendErrors(t *testing.T) {
	frames := frame.Config{
		Active: "work",
		List: []frame.Frame{{
			Name:         "work",
			Capabilities: map[string]map[string]any{"todos": {"backend": "ghost"}},
		}},
	}
	r := NewRouter(frames, "obsidian", map[string]Store{"obsidian": &fakeStore{name: "obsidian"}})
	if _, err := r.Complete(context.Background(), "work", "x"); err == nil {
		t.Error("complete on a frame with an unknown backend should error")
	}
	if _, err := r.Update(context.Background(), "work", "x", Patch{}); err == nil {
		t.Error("update on a frame with an unknown backend should error")
	}
}

func TestDueOnOrBefore(t *testing.T) {
	day := time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		due  string
		want bool
	}{
		{"2026-06-01", true},  // overdue
		{"2026-06-23", true},  // today
		{"2026-06-24", false}, // future
		{"", false},           // none
		{"garbage", false},    // unparseable
	}
	for _, c := range cases {
		if got := (Item{Due: c.due}).DueOnOrBefore(day); got != c.want {
			t.Errorf("DueOnOrBefore(%q) = %v want %v", c.due, got, c.want)
		}
	}
}

func TestWithInbox_CustomFilename(t *testing.T) {
	vault := tempVault(t, nil)
	s := NewObsidianStore(vault, WithInbox("inbox/tasks.md"), WithIDFunc(counterID()))
	it, err := s.Add(context.Background(), Scope{}, Draft{Text: "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(it.Source, "inbox/tasks.md") {
		t.Errorf("custom inbox not used: %q", it.Source)
	}
	// Blank override keeps the default.
	s2 := NewObsidianStore(vault, WithInbox("  "), WithIDFunc(counterID()))
	it2, _ := s2.Add(context.Background(), Scope{}, Draft{Text: "y"})
	if !strings.HasPrefix(it2.Source, "todos.md") {
		t.Errorf("blank inbox should keep default: %q", it2.Source)
	}
}

func ptr(s string) *string { return &s }
