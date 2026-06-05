package manage

import (
	"strings"

	"github.com/georgebuilds/carlos/internal/agent"
)

// Filter narrows the roster to rows whose Title or state name contains
// the (case-insensitive) substring. Empty Query returns all rows
// unchanged; the empty-query path is the common case (filter
// disabled), so we early-return to avoid the allocation.
type Filter struct {
	Query string
}

// Apply returns a filtered slice. Match policy: case-insensitive
// substring against the agent's title OR state-name OR ID. We don't
// pull in a real fuzzy library — the brief explicitly says substring
// is enough — but matching the ID lets the user pin a known agent by
// typing its prefix.
func (f Filter) Apply(rows []agent.AgentRow) []agent.AgentRow {
	q := strings.TrimSpace(strings.ToLower(f.Query))
	if q == "" {
		return rows
	}
	out := rows[:0:0] // fresh allocation so the caller's slice isn't aliased
	out = make([]agent.AgentRow, 0, len(rows))
	for _, r := range rows {
		hay := strings.ToLower(r.Title) + " " + r.State.String() + " " + strings.ToLower(r.ID)
		if strings.Contains(hay, q) {
			out = append(out, r)
		}
	}
	return out
}

// Active reports whether the filter currently narrows results. Used by
// the header to render a "filter: foo" chip when on.
func (f Filter) Active() bool {
	return strings.TrimSpace(f.Query) != ""
}
