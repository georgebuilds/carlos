package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// Registry multiplexes the HTTP surface over N backends (spec §12 / the
// multi-backend plan §5). Every thread id resolves to exactly one backend;
// the roster is the concatenation of each backend's ListThreads. carlos is
// the sole backend in v1; Claude Code / opencode register as peers later
// without the handlers special-casing any of them.
//
// Thread-id grammar: "<backend>:<native>" (e.g. "cc:<uuid>"). A legacy id
// with no prefix (carlos ULIDs) resolves to the default backend, the first
// registered. Backends operate on the FULL wire id; the prefix is for
// routing and validation only, never stripped before the backend sees it,
// so a backend's WireEvents carry the same id the SPA holds.
type Registry struct {
	mu       sync.RWMutex
	order    []string // registration order; order[0] is the default
	backends map[string]Backend
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{backends: map[string]Backend{}}
}

// Register adds or replaces a backend keyed by its Name(). Replacing
// preserves position (the carlos slot stays default when the read-only
// placeholder is swapped for the interactive backend via SetBackend).
func (r *Registry) Register(b Backend) {
	if b == nil {
		return
	}
	name := b.Name()
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.backends[name]; !ok {
		r.order = append(r.order, name)
	}
	r.backends[name] = b
}

// Default returns the first-registered backend (carlos), or nil if none.
// Used for id-less operations (create, meta) until per-backend creation
// lands.
func (r *Registry) Default() Backend {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.order) == 0 {
		return nil
	}
	return r.backends[r.order[0]]
}

// For routes a thread id to its backend. An unprefixed (legacy) id goes to
// the default; a prefixed id goes to the named backend, ok=false when no
// backend claims that prefix (the handler maps it to 404 unknown_backend).
func (r *Registry) For(threadID string) (Backend, bool) {
	name, _, prefixed := splitThreadID(threadID)
	r.mu.RLock()
	defer r.mu.RUnlock()
	if !prefixed {
		if len(r.order) == 0 {
			return nil, false
		}
		return r.backends[r.order[0]], true
	}
	b, ok := r.backends[name]
	return b, ok
}

// Names returns the registered backend names in registration order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// ListThreads fans out across every backend and concatenates the rosters
// in registration order. A backend that errors is skipped (its threads are
// absent) rather than failing the whole roster, EXCEPT when every backend
// fails, in which case the joined error surfaces (so a single-backend list
// failure still 500s, preserving the pre-router behavior). The result is
// always a non-nil slice so an empty roster serializes as [] not null.
func (r *Registry) ListThreads(ctx context.Context) ([]ThreadSummary, error) {
	r.mu.RLock()
	backends := make([]Backend, 0, len(r.order))
	for _, n := range r.order {
		backends = append(backends, r.backends[n])
	}
	r.mu.RUnlock()

	out := []ThreadSummary{}
	var errs []error
	for _, b := range backends {
		ts, err := b.ListThreads(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", b.Name(), err))
			continue
		}
		out = append(out, ts...)
	}
	if len(out) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

// splitThreadID parses a wire thread id of the form "<backend>:<native>".
// Returns (backend, native, true) when a non-empty prefix precedes a ':',
// else ("", id, false) for a legacy unprefixed id (carlos ULIDs carry no
// ':' and route to the default backend).
func splitThreadID(id string) (backend, native string, prefixed bool) {
	if i := strings.IndexByte(id, ':'); i > 0 {
		return id[:i], id[i+1:], true
	}
	return "", id, false
}
