package main

// Error-branch coverage for ensureDefaultAgent (runtime_tui.go). The
// happy create/resume paths are pinned by ensure_default_agent_prune_test
// and the boot-path tests; this file drives the seven reachable error
// returns by sabotaging the underlying SQLite handle the same way
// internal/agent's coverage_eventlog_test does: closing the *sql.DB so
// the next query fails, or installing RAISE(ABORT) triggers through the
// exposed DB() seam so a specific later statement fails while everything
// before it succeeds. The three remaining uncovered returns are the
// json.Marshal failures inside NewStateChangeCreated/Transition, which
// cannot fail for plain structs and stay as documented defensive code.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
)

// openEnsureLog opens a fresh state DB in a temp dir and registers
// cleanup. Tests sabotage it afterwards via log.DB().
func openEnsureLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	log, err := agent.OpenStateDB(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = agent.CloseStateDB(log) })
	return log
}

func TestEnsureDefaultAgent_ErrorBranches(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name string
		// seedResume runs the happy create first so the call under
		// test takes the resume (existing events) branch.
		seedResume bool
		// sabotage breaks the DB after seeding, before the call
		// under test.
		sabotage func(t *testing.T, log *agent.SQLiteEventLog)
		wantErr  string
	}{
		{
			name: "read fails on closed db",
			sabotage: func(t *testing.T, log *agent.SQLiteEventLog) {
				if err := log.DB().Close(); err != nil {
					t.Fatalf("close db: %v", err)
				}
			},
			wantErr: "read existing",
		},
		{
			name:       "resume append blocked",
			seedResume: true,
			sabotage: func(t *testing.T, log *agent.SQLiteEventLog) {
				mustExec(t, log, `CREATE TRIGGER block_ev BEFORE INSERT ON events
					BEGIN SELECT RAISE(ABORT, 'sabotage-events'); END`)
			},
			wantErr: "append resume transition",
		},
		{
			name:       "resume state refresh blocked",
			seedResume: true,
			sabotage: func(t *testing.T, log *agent.SQLiteEventLog) {
				mustExec(t, log, `CREATE TRIGGER block_state BEFORE UPDATE OF state ON agents
					BEGIN SELECT RAISE(ABORT, 'sabotage-state'); END`)
			},
			wantErr: "refresh agent state",
		},
		{
			name:       "resume heartbeat refresh blocked",
			seedResume: true,
			sabotage: func(t *testing.T, log *agent.SQLiteEventLog) {
				// Fires only when last_heartbeat_at is in the SET list:
				// UpdateAgentState (state, updated_at) passes, the
				// follow-up UpdateHeartbeat trips it.
				mustExec(t, log, `CREATE TRIGGER block_hb BEFORE UPDATE OF last_heartbeat_at ON agents
					BEGIN SELECT RAISE(ABORT, 'sabotage-heartbeat'); END`)
			},
			wantErr: "refresh agent heartbeat",
		},
		{
			name: "fresh first append blocked",
			sabotage: func(t *testing.T, log *agent.SQLiteEventLog) {
				mustExec(t, log, `CREATE TRIGGER block_ev BEFORE INSERT ON events
					BEGIN SELECT RAISE(ABORT, 'sabotage-events'); END`)
			},
			wantErr: "sabotage-events",
		},
		{
			name: "fresh transition append blocked",
			sabotage: func(t *testing.T, log *agent.SQLiteEventLog) {
				// The created payload carries kind=created; only the
				// second append (kind=transition) trips the trigger.
				mustExec(t, log, `CREATE TRIGGER block_trans BEFORE INSERT ON events
					WHEN CAST(NEW.payload AS TEXT) LIKE '%transition%'
					BEGIN SELECT RAISE(ABORT, 'sabotage-transition'); END`)
			},
			wantErr: "append initial transition",
		},
		{
			name: "fresh roster insert blocked",
			sabotage: func(t *testing.T, log *agent.SQLiteEventLog) {
				mustExec(t, log, `CREATE TRIGGER block_roster BEFORE INSERT ON agents
					BEGIN SELECT RAISE(ABORT, 'sabotage-roster'); END`)
			},
			wantErr: "sabotage-roster",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := openEnsureLog(t)
			if tc.seedResume {
				created, err := ensureDefaultAgent(ctx, log, "chat-default", "anthropic", "m", "george")
				if err != nil || !created {
					t.Fatalf("seed create = (%v, %v), want (true, nil)", created, err)
				}
			}
			tc.sabotage(t, log)

			created, err := ensureDefaultAgent(ctx, log, "chat-default", "anthropic", "m", "george")
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %q, want substring %q", err, tc.wantErr)
			}
			if created {
				t.Error("created must be false on the error path")
			}
		})
	}
}

// mustExec runs raw SQL against the log's exposed handle.
func mustExec(t *testing.T, log *agent.SQLiteEventLog, sql string) {
	t.Helper()
	if _, err := log.DB().Exec(sql); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}
