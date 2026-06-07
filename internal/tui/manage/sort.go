package manage

import (
	"sort"

	"github.com/georgebuilds/carlos/internal/agent"
)

// SortKey identifies the active sort column / mode. Default is
// priority; pressing 1–5 overrides until the next refresh or window
// resize. Each key has both an ascending and a descending direction
// (shift-key reverses).
type SortKey int

const (
	// SortPriority is the default surfaced by SPEC § "what the user
	// monitors for". Awaiting-input, runaway-cost, verification-failed
	// (not yet wired), no-heartbeat, then everything else.
	SortPriority SortKey = iota
	SortID
	SortState
	SortCost
	SortTokens
	SortTime
)

func (k SortKey) String() string {
	switch k {
	case SortPriority:
		return "priority"
	case SortID:
		return "id"
	case SortState:
		return "state"
	case SortCost:
		return "cost"
	case SortTokens:
		return "tokens"
	case SortTime:
		return "time"
	}
	return "unknown"
}

// priorityRank scores a row for the default sort. Lower = higher
// priority (sorted ascending in the rendered list).
//
// Bucket 0: awaiting-input. SPEC's loudest signal - only a human
// unblocks them.
// Bucket 1: runaway cost. Cheap heuristic - top 10% by cost-cents is
// computed by sortPriority below; this rank function only knows about
// the bucket, not the threshold.
// Bucket 2: verification-failed. Not yet wired; the slot exists so we
// don't reshuffle later when the badge lands. Returns the default
// non-flagged rank for now.
// Bucket 3: no-heartbeat (orphaned). Loud "something went wrong".
// Bucket 4: everything else.
//
// runawayBudget is the costThreshold caller computes from the slice
// (top-10% mark); rows at or above it land in bucket 1.
func priorityRank(r agent.AgentRow, runawayBudget int64) int {
	switch {
	case r.State == agent.StateAwaitingInput:
		return 0
	case runawayBudget > 0 && r.CostCents >= runawayBudget:
		return 1
	// Bucket 2: verification-failed - not yet wired; intentionally
	// reserves the slot. When a `verification_failed` badge is added
	// to AgentRow, this branch flips on.
	case r.State == agent.StateOrphaned:
		return 3
	default:
		return 4
	}
}

// runawayThreshold returns the cost (in cents) at the 90th percentile
// of the rows' cost distribution. Rows at or above the threshold are
// considered runaway candidates. Returns 0 when no row has any cost
// recorded (so the bucket stays empty rather than every row falsely
// flagging).
func runawayThreshold(rows []agent.AgentRow) int64 {
	if len(rows) == 0 {
		return 0
	}
	costs := make([]int64, 0, len(rows))
	var anyCost bool
	for _, r := range rows {
		costs = append(costs, r.CostCents)
		if r.CostCents > 0 {
			anyCost = true
		}
	}
	if !anyCost {
		return 0
	}
	sort.Slice(costs, func(i, j int) bool { return costs[i] < costs[j] })
	// 90th percentile by index. ceil((len-1)*0.9) keeps small-N
	// behavior sensible (N=2 picks the higher; N=10 picks the 9th).
	idx := int(float64(len(costs)-1) * 0.9)
	if idx < 0 {
		idx = 0
	}
	return costs[idx]
}

// SortBy returns a sorted copy of rows under the given key. asc=true
// keeps the documented direction (ascending for IDs, ascending state
// label, ascending cost, etc.); asc=false reverses.
//
// The priority sort intentionally ignores asc - the user's request is
// "tell me what needs attention", which has a canonical direction. We
// keep the asc parameter to allow Shift+1 to fall through to priority-
// reversed (descending bucket order) for symmetry, but the typical
// caller passes asc=true.
func SortBy(rows []agent.AgentRow, key SortKey, asc bool) []agent.AgentRow {
	out := make([]agent.AgentRow, len(rows))
	copy(out, rows)
	switch key {
	case SortPriority:
		budget := runawayThreshold(out)
		sort.SliceStable(out, func(i, j int) bool {
			ri := priorityRank(out[i], budget)
			rj := priorityRank(out[j], budget)
			if ri != rj {
				if !asc {
					return ri > rj
				}
				return ri < rj
			}
			// Tie-break by recency: a more recently updated agent
			// surfaces above an older one in the same bucket.
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		})
	case SortID:
		sort.SliceStable(out, func(i, j int) bool {
			if asc {
				return out[i].ID < out[j].ID
			}
			return out[i].ID > out[j].ID
		})
	case SortState:
		sort.SliceStable(out, func(i, j int) bool {
			if asc {
				return out[i].State.String() < out[j].State.String()
			}
			return out[i].State.String() > out[j].State.String()
		})
	case SortCost:
		sort.SliceStable(out, func(i, j int) bool {
			if asc {
				return out[i].CostCents > out[j].CostCents // big-cost first by default
			}
			return out[i].CostCents < out[j].CostCents
		})
	case SortTokens:
		sort.SliceStable(out, func(i, j int) bool {
			ti := out[i].TokensIn + out[i].TokensOut
			tj := out[j].TokensIn + out[j].TokensOut
			if asc {
				return ti > tj // big-token first by default
			}
			return ti < tj
		})
	case SortTime:
		sort.SliceStable(out, func(i, j int) bool {
			if asc {
				return out[i].CreatedAt.Before(out[j].CreatedAt) // oldest first
			}
			return out[i].CreatedAt.After(out[j].CreatedAt)
		})
	}
	return out
}
