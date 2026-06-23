// Package todo is carlos's pluggable task/reminder layer.
//
// The model the rest of carlos sees is small and backend-agnostic:
//
//   - Item   - one task, with a stable backend-owned ID, optional due date
//     and tags, labelled with the frame it belongs to.
//   - Store  - the four operations a backend must implement: List / Add /
//     Complete / Update. Obsidian is the out-of-the-box backend;
//     a generic REST backend ships as the reference external one.
//   - Scope  - "where" a query or mutation lands: a frame name plus the
//     Obsidian vault_subtree (so descendant folders nest naturally)
//     plus opaque per-backend params (e.g. a REST project id).
//   - Router - resolves each frame to its backend + scope, and answers the
//     two lenses: Master (union across every frame) and Frame (one
//     frame and the folders beneath it).
//
// The package deliberately depends only on the standard library plus the
// frame config type, so it stays unit-testable without a vault, a daemon,
// or a network. Backends pull in their own dependencies (the Obsidian
// backend touches the filesystem; the REST backend an *http.Client).
package todo

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is returned by Complete/Update when no item with the given ID
// exists in the resolved scope.
var ErrNotFound = errors.New("todo: item not found")

// Item is one task as carlos sees it, independent of which backend stores it.
//
// Slice fields are normalised to non-nil so JSON encodes `[]` rather than
// `null`. ID is stable for the lifetime of the item within its backend: the
// Obsidian backend mints an Obsidian block reference (`^todo-xxxx`); a REST
// backend echoes whatever id its service assigns.
type Item struct {
	// ID is the backend-stable handle Complete/Update target. For Obsidian
	// it is the block-ref slug WITHOUT the leading caret (e.g. "todo-ab12cd").
	ID string `json:"id"`
	// Text is the human description with the due marker, block-ref, and
	// standalone #tags stripped out into their structured fields.
	Text string `json:"text"`
	// Done is the checkbox state.
	Done bool `json:"done"`
	// Frame labels which frame the item belongs to (the master view sets it
	// per source frame; a single-frame query echoes that frame).
	Frame string `json:"frame,omitempty"`
	// Backend names the Store that produced the item ("obsidian", "rest", …).
	Backend string `json:"backend,omitempty"`
	// Source is a backend-specific locator for display: for Obsidian the
	// "relpath:line" of the checkbox; for REST the endpoint path.
	Source string `json:"source,omitempty"`
	// Due is the due date in YYYY-MM-DD form, or "" when none. Kept as a
	// string so JSON stays timezone-free and round-trips Obsidian's
	// `📅 YYYY-MM-DD` marker exactly.
	Due string `json:"due,omitempty"`
	// Tags are the item's #tags, without the leading '#'.
	Tags []string `json:"tags,omitempty"`
}

// Draft is the input to Add: everything a new item needs before the backend
// assigns it an ID.
type Draft struct {
	Text string
	Due  string
	Tags []string
}

// Patch is a partial update for Update. Nil fields are left untouched so a
// caller can flip Done without disturbing Text, retag without re-dating, etc.
type Patch struct {
	Text *string
	Done *bool
	Due  *string
	Tags *[]string
}

// Filter selects which items List returns by completion state.
type Filter string

const (
	// FilterOpen returns only not-done items. The default.
	FilterOpen Filter = "open"
	// FilterDone returns only completed items.
	FilterDone Filter = "done"
	// FilterAll returns every item regardless of state.
	FilterAll Filter = "all"
)

// match reports whether an item passes the filter.
func (f Filter) match(done bool) bool {
	switch f {
	case FilterDone:
		return done
	case FilterAll:
		return true
	default: // FilterOpen and any unknown value
		return !done
	}
}

// Scope is "where" a query or mutation applies. For Obsidian, Subtree is the
// cleaned vault-relative folder prefix (descendant folders are included, which
// is how a frame "sees its children's todos"). Params carries opaque
// per-backend routing (e.g. {"project": "Ludus"} for a REST backend).
type Scope struct {
	Frame   string
	Subtree string
	Params  map[string]string
}

// Query is a List request. Scopes lists every (frame, subtree) the query
// should sweep; the master view passes one per frame, a targeted lens passes
// one. An empty Scopes slice means "the backend's whole space" (legacy
// single-shelf mode).
type Query struct {
	Scopes []Scope
	Filter Filter
}

// Store is the contract every todo backend implements. Implementations must be
// safe for concurrent use by multiple goroutines.
type Store interface {
	// Name is the backend identifier used in config and on Item.Backend.
	Name() string
	// List returns items matching q, ordered deterministically (the Obsidian
	// backend orders by file path then line).
	List(ctx context.Context, q Query) ([]Item, error)
	// Add appends a new item in sc and returns it with its minted ID.
	Add(ctx context.Context, sc Scope, draft Draft) (Item, error)
	// Complete marks the item with id done within sc and returns it.
	Complete(ctx context.Context, sc Scope, id string) (Item, error)
	// Update applies patch to the item with id within sc and returns it.
	Update(ctx context.Context, sc Scope, id string, patch Patch) (Item, error)
}

// ParseDue parses a YYYY-MM-DD due string into a UTC midnight time. ok is
// false for the empty string or an unparseable value, so callers can treat
// "no due date" and "garbage" alike (both simply never fire a reminder).
func ParseDue(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// DueOnOrBefore reports whether the item has a due date on or before the given
// day, compared at date granularity in day's own location (so callers pass a
// local "now" to get reminders keyed to the user's wall-clock date). Used by
// the reminder scanner to find overdue + due-today items.
func (it Item) DueOnOrBefore(day time.Time) bool {
	due, ok := ParseDue(it.Due)
	if !ok {
		return false
	}
	cutoff := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
	return !due.After(cutoff)
}
