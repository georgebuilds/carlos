// Slice 3b: spawn-depth cap helper.
//
// Depth is computed by walking the parent_id chain in the `agents`
// projection cache (which Supervisor.Spawn already maintains via
// InsertAgent on the create event). Walking the events table would also
// work but would force a Replay per spawn; the projection cache is the
// designed-for hot path.
//
// Cap semantics (design § Manage mode safety rails; spec § Manage
// mode § Safety rails): maxSpawnDepth = 1 by default, meaning the root
// agent can spawn children but those children cannot spawn grandchildren.
// "Leaf agents cannot spawn" in SPEC language.
package agent

import (
	"context"
	"fmt"
)

// computeDepth returns the depth of an agent whose parent is parentID.
// A spawn whose parentID is empty is the root (depth 0). A spawn whose
// parent is the root returns 1. A spawn whose parent is a depth-1 agent
// returns 2 — which would normally exceed maxSpawnDepth=1.
//
// The walk is capped at maxSpawnDepth+2 hops as a defensive guard
// against pathological cycles in the parent_id column (which the
// projection cache *should* never contain, since InsertAgent only
// happens once per agent_id and creates flow from parent → child; but
// belt-and-braces against corrupt rows).
//
// On a missing parent row, returns an error rather than silently
// treating the parent as the root — a Spawn against a parentID that
// the supervisor doesn't know about is a caller bug we want to surface.
func (s *Supervisor) computeDepth(ctx context.Context, parentID string) (int, error) {
	if parentID == "" {
		return 0, nil
	}
	if s.log == nil {
		return 0, fmt.Errorf("supervisor.computeDepth: nil log")
	}
	depth := 0
	cur := parentID
	// Cap iterations: maxSpawnDepth+2 is enough to detect "deeper than
	// allowed" without unbounded work. The +2 lets us count the parent
	// itself plus one cycle-detection slack hop.
	maxHops := s.maxSpawnDepth + 2
	if maxHops < 3 {
		maxHops = 3
	}
	for hop := 0; hop < maxHops; hop++ {
		row, ok, err := s.log.GetAgent(ctx, cur)
		if err != nil {
			return 0, fmt.Errorf("supervisor.computeDepth: get %s: %w", cur, err)
		}
		if !ok {
			return 0, fmt.Errorf("supervisor.computeDepth: unknown parent %q", cur)
		}
		depth++
		if row.ParentID == "" {
			return depth, nil
		}
		cur = row.ParentID
	}
	// Exceeded hop budget. We KNOW depth > maxSpawnDepth at this point,
	// which is exactly the condition Spawn will refuse on — return the
	// hop count so the caller's comparison still trips.
	return depth, nil
}
