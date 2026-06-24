package todo

import (
	"context"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/frame"
)

// fakeStore is an in-memory Store used to prove the router dispatches to a
// non-Obsidian backend and threads scope params through.
type fakeStore struct {
	name       string
	items      []Item
	lastScopes []Scope
	addedTo    []Scope
}

func (f *fakeStore) Name() string { return f.name }

func (f *fakeStore) List(_ context.Context, q Query) ([]Item, error) {
	f.lastScopes = q.Scopes
	out := make([]Item, 0, len(f.items))
	for _, it := range f.items {
		if q.Filter.match(it.Done) {
			// Label with the scope frame + backend so master labelling and
			// routing are observable.
			it.Backend = f.name
			if len(q.Scopes) > 0 {
				it.Frame = q.Scopes[0].Frame
			}
			out = append(out, it)
		}
	}
	return out, nil
}

func (f *fakeStore) Add(_ context.Context, sc Scope, d Draft) (Item, error) {
	f.addedTo = append(f.addedTo, sc)
	return Item{ID: "fake-1", Text: d.Text, Frame: sc.Frame, Backend: f.name}, nil
}

func (f *fakeStore) Complete(_ context.Context, _ Scope, id string) (Item, error) {
	return Item{ID: id, Done: true, Backend: f.name}, nil
}

func (f *fakeStore) Update(_ context.Context, _ Scope, id string, _ Patch) (Item, error) {
	return Item{ID: id, Backend: f.name}, nil
}

func twoFrameConfig() frame.Config {
	return frame.Config{
		Active:  "work",
		Default: "personal",
		List: []frame.Frame{
			{Name: "personal", VaultSubtree: "personal"},
			{
				Name:         "work",
				VaultSubtree: "work",
				Capabilities: map[string]map[string]any{
					"todos": {"backend": "rest", "project": "Ludus"},
				},
			},
		},
	}
}

func TestRouter_MasterUnionAcrossBackends(t *testing.T) {
	vault := tempVault(t, map[string]string{
		"personal/todos.md": "- [ ] personal thing ^todo-p1\n",
	})
	obs := NewObsidianStore(vault)
	rest := &fakeStore{name: "rest", items: []Item{{ID: "r1", Text: "work thing", Backend: "rest"}}}
	r := NewRouter(twoFrameConfig(), "obsidian", map[string]Store{"obsidian": obs, "rest": rest})

	items, err := r.Master(context.Background(), FilterOpen)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("master should union both backends (2), got %d: %+v", len(items), items)
	}
	// Sorted by frame: personal before work.
	if items[0].Frame != "personal" || items[1].Frame != "work" {
		t.Errorf("frame labelling/order wrong: %+v", items)
	}
	// The work frame routed to the REST backend with its project param.
	if len(rest.lastScopes) != 1 || rest.lastScopes[0].Params["project"] != "Ludus" {
		t.Errorf("rest scope params not threaded: %+v", rest.lastScopes)
	}
}

func TestRouter_FrameLensTargetsOneBackend(t *testing.T) {
	vault := tempVault(t, map[string]string{
		"personal/todos.md": "- [ ] p ^todo-p1\n",
		"work/todos.md":     "- [ ] should-not-appear ^todo-w1\n",
	})
	obs := NewObsidianStore(vault)
	rest := &fakeStore{name: "rest", items: []Item{{ID: "r1", Text: "work thing"}}}
	r := NewRouter(twoFrameConfig(), "obsidian", map[string]Store{"obsidian": obs, "rest": rest})

	// personal lens → obsidian only.
	items, err := r.Frame(context.Background(), "personal", FilterOpen)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "todo-p1" {
		t.Errorf("personal lens wrong: %+v", items)
	}

	// work lens → rest backend.
	witems, err := r.Frame(context.Background(), "work", FilterOpen)
	if err != nil {
		t.Fatal(err)
	}
	if len(witems) != 1 || witems[0].Backend != "rest" {
		t.Errorf("work lens should hit rest backend: %+v", witems)
	}
}

func TestRouter_EmptyFrameTargetsActive(t *testing.T) {
	rest := &fakeStore{name: "rest"}
	r := NewRouter(twoFrameConfig(), "obsidian", map[string]Store{
		"obsidian": &fakeStore{name: "obsidian"},
		"rest":     rest,
	})
	// Active frame is "work" → rest backend.
	if _, err := r.Add(context.Background(), "", Draft{Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if len(rest.addedTo) != 1 || rest.addedTo[0].Frame != "work" {
		t.Errorf("empty frame should resolve to active 'work': %+v", rest.addedTo)
	}
}

func TestRouter_AddRoutesToNamedFrame(t *testing.T) {
	vault := tempVault(t, nil)
	obs := NewObsidianStore(vault, WithIDFunc(counterID()))
	r := NewRouter(twoFrameConfig(), "obsidian", map[string]Store{
		"obsidian": obs,
		"rest":     &fakeStore{name: "rest"},
	})
	it, err := r.Add(context.Background(), "personal", Draft{Text: "buy milk"})
	if err != nil {
		t.Fatal(err)
	}
	if it.Backend != "obsidian" || it.Frame != "personal" {
		t.Errorf("add routed wrong: %+v", it)
	}
	if !strings.HasPrefix(it.Source, "personal/todos.md") {
		t.Errorf("source = %q", it.Source)
	}
}

func TestRouter_UnknownFrameErrors(t *testing.T) {
	r := NewRouter(twoFrameConfig(), "obsidian", map[string]Store{"obsidian": &fakeStore{name: "obsidian"}, "rest": &fakeStore{name: "rest"}})
	if _, err := r.Frame(context.Background(), "ghost", FilterAll); err == nil {
		t.Error("unknown frame should error")
	}
}

func TestRouter_UnknownBackendErrors(t *testing.T) {
	// work frame pins backend "rest" but we don't register it.
	r := NewRouter(twoFrameConfig(), "obsidian", map[string]Store{"obsidian": &fakeStore{name: "obsidian"}})
	if _, err := r.Master(context.Background(), FilterAll); err == nil {
		t.Error("master should surface unknown-backend misconfig")
	}
}

func TestRouter_LegacyNoFrames(t *testing.T) {
	vault := tempVault(t, map[string]string{"todos.md": "- [ ] solo ^todo-1\n"})
	obs := NewObsidianStore(vault)
	r := NewRouter(frame.Config{}, "obsidian", map[string]Store{"obsidian": obs})
	items, err := r.Master(context.Background(), FilterOpen)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != "todo-1" {
		t.Errorf("legacy single-shelf master wrong: %+v", items)
	}
}
