// Package agent lifecycle: open/close/recover the state.db.
//
// This file plumbs the lifecycle for ~/.carlos/state.db on top of
// OpenSQLiteEventLog. It is intentionally thin: open ensures the parent
// dir exists and the schema is healthy; close defers to the eventlog's
// Close (which closes the *sql.DB); recover scans the `agents` projection
// cache for non-terminal rows whose last_heartbeat_at is too stale and
// transitions them to `orphaned`.
//
// Atomic-shutdown discipline (design § Storage):
//
//	The DB is opened with synchronous=NORMAL and journal_mode=WAL. Each
//	COMMIT is durable across an app crash (we proved this in the Phase 1
//	preflight TestEventLogIsSourceOfTruth_OSExitKill). The only failure
//	window is OS/power loss between fsync of the WAL frame and fsync of
//	the database file. A clean CloseStateDB() is sufficient for
//	durability; no explicit checkpoint is required.
//
// Recovery semantics (SPEC § Manage mode § Heartbeat + orphan detection;
// design § Crash recovery):
//
//	On startup we scan the `agents` projection for rows in non-terminal
//	states. Any row whose last_heartbeat_at is more than the recovery
//	tolerance (default 60s - generously larger than 2x heartbeat or the
//	sweep interval, so a clean shutdown immediately followed by a startup
//	doesn't false-orphan agents that genuinely were healthy) stale gets a
//	state_change kind=transition to=orphaned event appended and the
//	projection row updated. The user retries orphans explicitly through
//	the TUI; Recover never auto-retries.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// DefaultRecoveryTolerance is how stale a non-terminal agent's heartbeat
// must be (at Recover time) before we promote it to `orphaned`. It is
// deliberately larger than 2x HeartbeatInterval (= 10s) and the sweep
// cadence (= 10s) combined, so a clean restart that races with the live
// sweeper doesn't double-orphan agents in flight.
const DefaultRecoveryTolerance = 60 * time.Second

// RecoveryReport summarizes what Recover did. Suitable for logging or
// surfacing in the chat view's status line ("recovered N agents,
// orphaned M").
type RecoveryReport struct {
	// Orphaned is the list of agent IDs that Recover transitioned to
	// `orphaned` because their heartbeats were too stale.
	Orphaned []string
	// StillActive is the list of agent IDs that were in a non-terminal
	// state and had a recent-enough heartbeat to keep going. The user can
	// resume any of these explicitly through the TUI; Recover does not
	// auto-resume.
	StillActive []string
	// Counts are useful totals for the status line.
	Counts struct {
		// Events is the total number of events on disk.
		Events int64
		// Agents is the total number of rows in the projection cache.
		Agents int64
	}
}

// OpenStateDB opens (creating if needed) the SQLite event log at path.
// Differences vs OpenSQLiteEventLog:
//
//   - Ensures the parent directory exists with mode 0700 (matches the
//     onboarding mode for ~/.carlos/config.yaml - same security posture).
//   - Verifies the schema is healthy by running a trivial SELECT against
//     each of the three tables the supervisor needs. OpenSQLiteEventLog
//     creates the schema idempotently, so this is belt-and-braces against
//     a corrupt DB where the file exists but the tables don't.
func OpenStateDB(path string) (*SQLiteEventLog, error) {
	if path == "" {
		return nil, errors.New("lifecycle: empty path")
	}
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("lifecycle: mkdir %s: %w", dir, err)
		}
		// MkdirAll respects an existing dir's mode - re-chmod ourselves to
		// guarantee 0700 even if a previous run created it differently.
		if err := os.Chmod(dir, 0o700); err != nil {
			// Non-fatal: on some filesystems chmod is a no-op, and we'd
			// rather succeed than hard-fail on a working dir. Log via
			// slog at Warn so the operator notices the relaxed perms
			// even though the open still proceeds.
			slog.Default().Warn("state.db parent chmod 0700 failed (non-fatal)",
				"dir", dir, "err", err)
		}
	}
	log, err := OpenSQLiteEventLog(path)
	if err != nil {
		return nil, err
	}
	if err := verifySchema(log); err != nil {
		_ = log.Close()
		return nil, err
	}
	return log, nil
}

// verifySchema runs a no-op SELECT against each table the supervisor
// uses. If any table is missing, the SELECT returns a sqlite error which
// we wrap. The schema-create in OpenSQLiteEventLog is idempotent, so the
// only way this fails is genuine DB corruption.
func verifySchema(log *SQLiteEventLog) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for _, q := range []string{
		`SELECT 1 FROM events    LIMIT 0`,
		`SELECT 1 FROM agents    LIMIT 0`,
		`SELECT 1 FROM artifacts LIMIT 0`,
	} {
		if _, err := log.DB().ExecContext(ctx, q); err != nil {
			return fmt.Errorf("lifecycle: schema verify %q: %w", q, err)
		}
	}
	return nil
}

// CloseStateDB closes the underlying *sql.DB. With synchronous=NORMAL the
// WAL is durable across app crash; no explicit checkpoint is required.
// The only failure window is OS/power loss between two fsyncs (see
// package doc).
func CloseStateDB(log *SQLiteEventLog) error {
	if log == nil {
		return nil
	}
	return log.Close()
}

// Recover scans the agents projection cache for non-terminal rows. Any
// row whose last_heartbeat_at is more than DefaultRecoveryTolerance stale
// (relative to time.Now) is transitioned to `orphaned` by appending a
// state_change event AND updating the projection cache row. Returns a
// RecoveryReport for callers to log / display.
//
// Recover NEVER auto-retries an orphan. Per spec § Manage mode, the user
// explicitly retries (creating a new attempt-id) or abandons.
func Recover(ctx context.Context, log *SQLiteEventLog) (*RecoveryReport, error) {
	return RecoverWith(ctx, log, time.Now().UTC(), DefaultRecoveryTolerance)
}

// RecoverWith is the testable variant of Recover that takes the wall-clock
// "now" and the staleness tolerance as parameters. Production code calls
// Recover; tests inject a frozen clock and a tight tolerance.
func RecoverWith(ctx context.Context, log *SQLiteEventLog, now time.Time, tolerance time.Duration) (*RecoveryReport, error) {
	if log == nil {
		return nil, errors.New("recover: nil log")
	}
	threshold := now.Add(-tolerance)

	rep := &RecoveryReport{}

	// 1. Find stale non-terminal agents.
	staleIDs, err := log.StaleAgents(ctx, threshold)
	if err != nil {
		return nil, fmt.Errorf("recover: stale scan: %w", err)
	}

	// 2. For each stale agent: append state_change kind=transition
	//    to=orphaned, then update the projection cache row.
	for _, id := range staleIDs {
		payload, err := NewStateChangeTransition(StateOrphaned)
		if err != nil {
			return nil, fmt.Errorf("recover: marshal transition for %s: %w", id, err)
		}
		ev := Event{
			AgentID: id,
			TS:      now,
			Type:    EvtStateChange,
			Payload: payload,
		}
		if _, err := log.Append(ctx, ev); err != nil {
			return nil, fmt.Errorf("recover: append orphan event for %s: %w", id, err)
		}
		if err := log.UpdateAgentState(ctx, id, StateOrphaned, now); err != nil {
			return nil, fmt.Errorf("recover: update orphan row for %s: %w", id, err)
		}
		rep.Orphaned = append(rep.Orphaned, id)
	}

	// 3. Find still-active agents: non-terminal rows that are NOT in the
	//    stale list. The user resumes these explicitly through the TUI;
	//    Recover doesn't auto-resume.
	activeIDs, err := log.NonTerminalAgents(ctx)
	if err != nil {
		return nil, fmt.Errorf("recover: active scan: %w", err)
	}
	staleSet := make(map[string]struct{}, len(staleIDs))
	for _, id := range staleIDs {
		staleSet[id] = struct{}{}
	}
	for _, id := range activeIDs {
		if _, isStale := staleSet[id]; isStale {
			continue
		}
		rep.StillActive = append(rep.StillActive, id)
	}

	// 4. Totals for the status line.
	if n, err := CountEvents(ctx, log); err == nil {
		rep.Counts.Events = n
	}
	if n, err := countAgents(ctx, log); err == nil {
		rep.Counts.Agents = n
	}

	return rep, nil
}

// countAgents returns the number of rows in the projection cache.
func countAgents(ctx context.Context, log *SQLiteEventLog) (int64, error) {
	row := log.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM agents`)
	var n int64
	return n, row.Scan(&n)
}
