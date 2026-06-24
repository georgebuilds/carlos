package todo

import (
	"context"
	"fmt"
	"sort"

	"github.com/georgebuilds/carlos/internal/frame"
)

// Router is the aggregation layer. It maps each frame to a backend Store and a
// Scope (via the frame's Capabilities["todos"] block, defaulting to the
// Obsidian backend + the frame's vault_subtree), and answers the two lenses
// the user asked for:
//
//   - Master: the union of every frame's todos, each item labelled with its
//     source frame. The high-level overview.
//   - Frame:  one frame and the folders nested beneath its subtree. The
//     targeted lens.
//
// Mutations (Add / Complete / Update) route to the single frame's backend.
type Router struct {
	stores map[string]Store
	def    string
	frames frame.Config
}

// NewRouter wires the backend stores and the frame configuration. def is the
// fallback backend name for frames that do not pin one (typically
// "obsidian"); an empty def defaults to "obsidian". stores must contain def.
func NewRouter(frames frame.Config, def string, stores map[string]Store) *Router {
	if def == "" {
		def = "obsidian"
	}
	return &Router{stores: stores, def: def, frames: frames}
}

// resolve maps a frame to its backend store and scope.
func (r *Router) resolve(f frame.Frame) (Store, Scope, error) {
	backend := r.def
	params := map[string]string{}
	if caps := f.Capabilities["todos"]; caps != nil {
		if b, ok := caps["backend"].(string); ok && b != "" {
			backend = b
		}
		for k, v := range caps {
			if k == "backend" {
				continue
			}
			if sv, ok := v.(string); ok {
				params[k] = sv
			}
		}
	}
	st, ok := r.stores[backend]
	if !ok {
		return nil, Scope{}, fmt.Errorf("todo: frame %q references unknown backend %q", f.Name, backend)
	}
	return st, Scope{Frame: f.Name, Subtree: f.VaultSubtree, Params: params}, nil
}

// frameList returns the configured frames, or a single synthetic anonymous
// frame (whole default backend) when none are configured. This keeps the
// legacy single-shelf path working: a vault with no frames still has one
// master list.
func (r *Router) frameList() []frame.Frame {
	if len(r.frames.List) == 0 {
		return []frame.Frame{{}}
	}
	return r.frames.List
}

// findFrame resolves a frame by name. An empty name resolves the active frame
// (falling back to default, then the first frame). Returns an error for an
// unknown explicit name.
func (r *Router) findFrame(name string) (frame.Frame, error) {
	if len(r.frames.List) == 0 {
		return frame.Frame{}, nil // legacy single shelf
	}
	if name == "" {
		if f := r.activeFrame(); f != nil {
			return *f, nil
		}
		return r.frames.List[0], nil
	}
	if f := r.frames.Find(name); f != nil {
		return *f, nil
	}
	return frame.Frame{}, fmt.Errorf("todo: unknown frame %q", name)
}

// activeFrame mirrors notesEnv.activeFrame resolution order: active → default.
func (r *Router) activeFrame() *frame.Frame {
	name := r.frames.Active
	if name == "" {
		name = r.frames.Default
	}
	if name == "" {
		return nil
	}
	return r.frames.Find(name)
}

// Master returns the union of every frame's todos under the given filter, each
// item labelled with its source frame. Frames are grouped by backend so each
// store is queried once with all its scopes. Items are ordered by frame name,
// then by their backend order (file path / line for Obsidian).
func (r *Router) Master(ctx context.Context, filter Filter) ([]Item, error) {
	// Group scopes by resolved store so a single backend call covers all of
	// its frames.
	type group struct {
		store  Store
		scopes []Scope
	}
	groups := map[string]*group{}
	var order []string
	for _, f := range r.frameList() {
		st, sc, err := r.resolve(f)
		if err != nil {
			return nil, err
		}
		g, ok := groups[st.Name()]
		if !ok {
			g = &group{store: st}
			groups[st.Name()] = g
			order = append(order, st.Name())
		}
		g.scopes = append(g.scopes, sc)
	}

	var out []Item
	for _, name := range order {
		g := groups[name]
		items, err := g.store.List(ctx, Query{Scopes: g.scopes, Filter: filter})
		if err != nil {
			return nil, fmt.Errorf("todo: backend %q: %w", name, err)
		}
		out = append(out, items...)
	}
	sortItems(out)
	return out, nil
}

// Frame returns the todos for a single frame (and the folders nested beneath
// its subtree) under the given filter. An empty name targets the active frame.
func (r *Router) Frame(ctx context.Context, name string, filter Filter) ([]Item, error) {
	f, err := r.findFrame(name)
	if err != nil {
		return nil, err
	}
	st, sc, err := r.resolve(f)
	if err != nil {
		return nil, err
	}
	items, err := st.List(ctx, Query{Scopes: []Scope{sc}, Filter: filter})
	if err != nil {
		return nil, err
	}
	sortItems(items)
	return items, nil
}

// Add routes a new task to the named frame's backend (active frame if empty).
func (r *Router) Add(ctx context.Context, frameName string, draft Draft) (Item, error) {
	st, sc, err := r.resolveByName(frameName)
	if err != nil {
		return Item{}, err
	}
	return st.Add(ctx, sc, draft)
}

// Complete marks the task with id done in the named frame's backend.
func (r *Router) Complete(ctx context.Context, frameName, id string) (Item, error) {
	st, sc, err := r.resolveByName(frameName)
	if err != nil {
		return Item{}, err
	}
	return st.Complete(ctx, sc, id)
}

// Update applies patch to the task with id in the named frame's backend.
func (r *Router) Update(ctx context.Context, frameName, id string, patch Patch) (Item, error) {
	st, sc, err := r.resolveByName(frameName)
	if err != nil {
		return Item{}, err
	}
	return st.Update(ctx, sc, id, patch)
}

// resolveByName resolves a frame by name to its store + scope.
func (r *Router) resolveByName(name string) (Store, Scope, error) {
	f, err := r.findFrame(name)
	if err != nil {
		return nil, Scope{}, err
	}
	return r.resolve(f)
}

// sortItems orders items by frame, then backend source, then text, so output
// is stable across runs (Go map iteration in the grouping step is not).
func sortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Frame != items[j].Frame {
			return items[i].Frame < items[j].Frame
		}
		if items[i].Source != items[j].Source {
			return items[i].Source < items[j].Source
		}
		return items[i].Text < items[j].Text
	})
}
