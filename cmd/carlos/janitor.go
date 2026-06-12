// janitor.go - slice 9f: the empty-orphan prune, moved off the boot
// critical path.
//
// Before 9f the prune ran inline inside ensureDefaultAgent's brand-new-
// session branch, between "agent row seeded" and "first frame" - the
// only unbounded pre-frame cost on the boot path (it scans the agents +
// events + artifacts tables, so a long-lived state.db pays more every
// boot). It now runs on a background goroutine kicked from the SAME two
// call sites (fresh boot + /resume picking an empty session), gated on
// the same created-new-agent condition, so it fires exactly as often as
// before - it just no longer blocks the first frame.
//
// Concurrency safety:
//
//   - What it deletes: agents with state='orphaned' AND zero
//     events/children/artifacts AND updated_at older than 7 days
//     (agent.DefaultOrphanPruneAge). The live chat agent was JUST
//     seeded as StateRunning with two state-change events and a fresh
//     updated_at - it fails every predicate, so the prune can never
//     race the session that spawned it. Recover() already ran before
//     ensureDefaultAgent, so no concurrent orphan-marking is in flight
//     either; sub-agents spawned later start as 'spawning'/'running'
//     and are likewise out of scope.
//   - SQLite: state.db is opened WAL + busy_timeout=5000, and the
//     prune is one short transaction on the shared *sql.DB handle, so
//     a concurrent TUI append simply serializes behind it (and vice
//     versa) instead of erroring.
//   - Shutdown: runDefault holds the goroutine in a sync.WaitGroup
//     whose Wait() defer is registered AFTER the log.Close/diagCleanup
//     defers, so (LIFO) it runs BEFORE them - the DB handle and the
//     diag sink stay alive until the prune goroutine has exited. The
//     signal-context cancel defer is registered later still, so a
//     prune stuck on a wedged DB is cancelled rather than hanging exit.
package main

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/georgebuilds/carlos/internal/agent"
)

// startJanitorPrune runs pruneEmptyOrphans on a background goroutine
// tracked by wg, keeping the janitor pass off the boot critical path.
func startJanitorPrune(ctx context.Context, log *agent.SQLiteEventLog, diag io.Writer, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		pruneEmptyOrphans(ctx, log, diag)
	}()
}

// pruneEmptyOrphans prunes empty orphaned agents (top-level sessions the
// user never typed in, plus sub-agents that never made a tool call)
// older than the grace window. These accumulate on every abrupt exit and
// bury the /resume picker and /agents under stale rows. Failure is
// logged to diag, never fatal - a janitor pass should never stop the
// user from getting a working chat. Semantics are identical to the
// pre-9f inline prune in ensureDefaultAgent.
func pruneEmptyOrphans(ctx context.Context, log *agent.SQLiteEventLog, diag io.Writer) {
	if pruned, err := log.DeleteEmptyOrphanedAgents(ctx, agent.DefaultOrphanPruneAge); err != nil {
		fmt.Fprintf(diag, "carlos: prune empty orphans: %v\n", err)
	} else if len(pruned) > 0 {
		fmt.Fprintf(diag, "carlos: pruned %d empty orphaned session(s)\n", len(pruned))
	}
}
