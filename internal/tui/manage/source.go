// Package manage is the bubbletea sub-agent supervision TUI - the
// marquee surface from SPEC § Manage mode.
//
// The package is a read-model + verb dispatcher over the event-sourced
// supervisor in internal/agent. It never owns supervisor state: the
// roster reads from the `agents` projection table, the focus pane
// reads from a per-agent EventLog.Subscribe channel, and the three
// verbs (steer / interrupt / stop) call Supervisor methods.
package manage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// SnapshotSource is the read seam the orchestrator uses to refresh the
// roster every refresh tick. Production wires it to a SQLite-backed
// implementation; tests can swap in a static one.
type SnapshotSource interface {
	Snapshot(ctx context.Context) ([]agent.AgentRow, error)
}

// SQLiteSnapshotSource queries the agents projection table directly.
// We don't go through agent.ReplayAll because that walks the entire
// event log on every tick (250ms cadence) - wasteful when the
// projection cache is exactly what we need. The query matches GetAgent's
// column layout so the deserialization stays consistent.
type SQLiteSnapshotSource struct {
	db *sql.DB
}

// NewSQLiteSnapshotSource constructs a snapshot source bound to the
// SQLiteEventLog's underlying *sql.DB. The log exposes DB() so callers
// can run read-only queries against the projection without going
// through a per-row GetAgent loop.
func NewSQLiteSnapshotSource(log *agent.SQLiteEventLog) *SQLiteSnapshotSource {
	return &SQLiteSnapshotSource{db: log.DB()}
}

// Snapshot returns every row in the agents projection table. Ordering
// is by ID (deterministic, matches Projection.Snapshot) so the TUI's
// later sort is reproducible.
func (s *SQLiteSnapshotSource) Snapshot(ctx context.Context) ([]agent.AgentRow, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, COALESCE(parent_id,''), root_id, state, attempt, title, COALESCE(model,''),
       tokens_in, tokens_out, cost_cents, tool_calls,
       created_at, updated_at, last_heartbeat_at
  FROM agents
 ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("manage: snapshot query: %w", err)
	}
	defer rows.Close()

	var out []agent.AgentRow
	for rows.Next() {
		var (
			r        agent.AgentRow
			stateS   string
			createdM int64
			updatedM int64
			hbM      int64
		)
		if err := rows.Scan(
			&r.ID, &r.ParentID, &r.RootID, &stateS, &r.Attempt, &r.Title, &r.Model,
			&r.TokensIn, &r.TokensOut, &r.CostCents, &r.ToolCalls,
			&createdM, &updatedM, &hbM,
		); err != nil {
			return nil, fmt.Errorf("manage: snapshot scan: %w", err)
		}
		st, ok := parseStateString(stateS)
		if !ok {
			// Unknown state strings shouldn't reach the TUI, but if they
			// do we surface a recognisable placeholder rather than
			// crashing the View loop. The badge renderer falls through
			// to muted gray.
			st = agent.StateSpawning
		}
		r.State = st
		r.CreatedAt = time.UnixMilli(createdM).UTC()
		r.UpdatedAt = time.UnixMilli(updatedM).UTC()
		r.LastHeartbeatAt = time.UnixMilli(hbM).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// StaticSnapshotSource is the test seam: a fixed list of rows that
// every Snapshot call returns. Useful for table-driven tests of the
// renderer and the sort/filter logic.
type StaticSnapshotSource struct {
	Rows []agent.AgentRow
}

// Snapshot returns a copy of the configured rows so callers can't
// mutate the source state across ticks.
func (s *StaticSnapshotSource) Snapshot(ctx context.Context) ([]agent.AgentRow, error) {
	out := make([]agent.AgentRow, len(s.Rows))
	copy(out, s.Rows)
	return out, nil
}

// parseStateString is the inverse of agent.State.String(). Kept local
// because the agent package's parseState is unexported and we only
// need the same mapping for SQL row hydration.
func parseStateString(s string) (agent.State, bool) {
	for _, st := range []agent.State{
		agent.StateSpawning, agent.StateQueued, agent.StateRunning,
		agent.StateAwaitingInput, agent.StateBlocked, agent.StatePausedByUser,
		agent.StateCompacting, agent.StateCancelling, agent.StateDone,
		agent.StateFailed, agent.StateOrphaned,
	} {
		if st.String() == s {
			return st, true
		}
	}
	return 0, false
}
