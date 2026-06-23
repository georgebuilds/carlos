package todo

import (
	"fmt"

	"github.com/georgebuilds/carlos/internal/frame"
)

// This file holds the one assembly point that turns plain configuration values
// into a wired Router. It is deliberately config-package-free (callers in
// internal/tools and internal/daemon translate their config structs into the
// primitive BuildOptions below) so the todo package never imports config and
// stays unit-testable in isolation.

// BackendSpec describes one external backend to construct. Type selects the
// transport; the remaining fields are transport-specific (currently REST).
type BackendSpec struct {
	Name       string
	Type       string
	BaseURL    string
	AuthHeader string
	AuthValue  string
}

// BuildOptions parameterise BuildRouter.
type BuildOptions struct {
	// DefaultBackend is the fallback backend name (empty → "obsidian").
	DefaultBackend string
	// Inbox is the Obsidian inbox filename (empty → "todos.md").
	Inbox string
	// Invalidator, when set, is wired into the Obsidian store so notes-cache
	// entries are dropped after writes.
	Invalidator Invalidator
	// Backends are the external backends to register alongside Obsidian.
	Backends []BackendSpec
}

// BuildRouter assembles a Router over the vault plus any declared external
// backends. The Obsidian backend is always registered under "obsidian". active
// overrides frames.Active for session-scoped frame focus (empty keeps the
// on-disk active frame).
func BuildRouter(vaultPath string, frames frame.Config, active string, opts BuildOptions) (*Router, error) {
	stores := map[string]Store{
		"obsidian": NewObsidianStore(vaultPath, WithInbox(opts.Inbox), WithInvalidator(opts.Invalidator)),
	}
	for _, spec := range opts.Backends {
		if spec.Name == "" || spec.Name == "obsidian" {
			return nil, fmt.Errorf("todo: invalid backend name %q", spec.Name)
		}
		switch spec.Type {
		case "rest":
			stores[spec.Name] = NewRESTStore(RESTConfig{
				Name:       spec.Name,
				BaseURL:    spec.BaseURL,
				AuthHeader: spec.AuthHeader,
				AuthValue:  spec.AuthValue,
			})
		default:
			return nil, fmt.Errorf("todo: backend %q has unsupported type %q", spec.Name, spec.Type)
		}
	}
	if active != "" {
		frames.Active = active
	}
	return NewRouter(frames, opts.DefaultBackend, stores), nil
}
