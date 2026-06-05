package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/fake"
)

// drive runs one provider Stream end-to-end through the event log + an
// in-memory projection, appending events at the documented semantic
// boundaries (state_change, tool_call, tool_result, token_usage, heartbeat).
// Returns the live projection at the end of the script.
//
// The point of the test is NOT that this is the production agent loop —
// the production loop lives in `internal/agent/subagent.go` (Phase 1d).
// The point is that an honest implementation of the documented write
// discipline can be replayed bit-identically.
func drive(t *testing.T, ctx context.Context, log *agent.SQLiteEventLog, p providers.Provider, agentID, title, model string) *agent.Projection {
	t.Helper()
	proj := agent.NewProjection()

	now := func() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

	// 1. Create the agent (state_change kind=created).
	createdPayload, err := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: agentID, RootID: agentID, Title: title, Model: model,
	})
	if err != nil {
		t.Fatalf("marshal created: %v", err)
	}
	ts0 := now()
	ev0 := agent.Event{AgentID: agentID, TS: ts0, Type: agent.EvtStateChange, Payload: createdPayload}
	if _, err := log.Append(ctx, ev0); err != nil {
		t.Fatalf("append created: %v", err)
	}
	if err := proj.Apply(ev0); err != nil {
		t.Fatalf("apply created: %v", err)
	}

	// 2. State -> running (state_change kind=transition).
	toRunning, _ := agent.NewStateChangeTransition(agent.StateRunning)
	ev1 := agent.Event{AgentID: agentID, TS: now(), Type: agent.EvtStateChange, Payload: toRunning}
	if _, err := log.Append(ctx, ev1); err != nil {
		t.Fatalf("append running: %v", err)
	}
	if err := proj.Apply(ev1); err != nil {
		t.Fatalf("apply running: %v", err)
	}

	// 3. Drive the provider stream. Coalesce text deltas (per DESIGN write
	//    discipline: do NOT write a row per token) — flush a single
	//    token_usage event at the end of the run.
	stream, err := p.Stream(ctx, providers.Request{Model: model})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	var tokenChars int64
	for ev := range stream {
		switch ev.Kind {
		case providers.EventTextDelta:
			tokenChars += int64(len(ev.Text))
		case providers.EventToolUseStart:
			payload, _ := json.Marshal(agent.ToolCall{Name: ev.ToolUse.Name})
			e := agent.Event{AgentID: agentID, TS: now(), Type: agent.EvtToolCall, Payload: payload}
			if _, err := log.Append(ctx, e); err != nil {
				t.Fatalf("append tool_call: %v", err)
			}
			if err := proj.Apply(e); err != nil {
				t.Fatalf("apply tool_call: %v", err)
			}
		case providers.EventToolUseEnd:
			e := agent.Event{AgentID: agentID, TS: now(), Type: agent.EvtToolResult, Payload: []byte(`{}`)}
			if _, err := log.Append(ctx, e); err != nil {
				t.Fatalf("append tool_result: %v", err)
			}
			if err := proj.Apply(e); err != nil {
				t.Fatalf("apply tool_result: %v", err)
			}
		case providers.EventStopReason:
			// flush coalesced token usage
			payload, _ := json.Marshal(agent.TokenUsage{DeltaIn: 0, DeltaOut: tokenChars, DeltaCost: 0})
			e := agent.Event{AgentID: agentID, TS: now(), Type: agent.EvtTokenUsage, Payload: payload}
			if _, err := log.Append(ctx, e); err != nil {
				t.Fatalf("append token_usage: %v", err)
			}
			if err := proj.Apply(e); err != nil {
				t.Fatalf("apply token_usage: %v", err)
			}
			// terminal state_change -> done
			toDone, _ := agent.NewStateChangeTransition(agent.StateDone)
			d := agent.Event{AgentID: agentID, TS: now(), Type: agent.EvtStateChange, Payload: toDone}
			if _, err := log.Append(ctx, d); err != nil {
				t.Fatalf("append done: %v", err)
			}
			if err := proj.Apply(d); err != nil {
				t.Fatalf("apply done: %v", err)
			}
		case providers.EventError:
			t.Fatalf("provider error: %v", ev.Err)
		}
	}
	return proj
}

// TestEventLogIsSourceOfTruth_BitIdentical is the headline preflight assertion:
//
//  1. Drive a scripted provider end-to-end through the event log + projection.
//  2. Snapshot the live projection's CanonicalJSON.
//  3. Close the DB (simulating a clean process exit; for the kill case see
//     the next test).
//  4. Reopen the DB, ReplayAll(), and assert the replayed projection's
//     CanonicalJSON is **byte-identical** to the live snapshot.
//
// If this fails the architecture changes (SPEC § Manage mode commitment is
// invalidated).
func TestEventLogIsSourceOfTruth_BitIdentical(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")

	ctx := context.Background()
	log, err := agent.OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	p := fake.New("fake", fake.CannedScript())
	live := drive(t, ctx, log, p, "agent-sot-1", "say hello + ls", "fake-model")

	wantJSON, err := live.CanonicalJSON()
	if err != nil {
		t.Fatalf("live snapshot: %v", err)
	}

	if err := log.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	log2, err := agent.OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer log2.Close()

	replayed, err := agent.ReplayAll(ctx, log2)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	gotJSON, err := replayed.CanonicalJSON()
	if err != nil {
		t.Fatalf("replayed snapshot: %v", err)
	}

	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("projection drift after replay:\nwant=%s\ngot =%s", wantJSON, gotJSON)
	}
}

// TestEventLogIsSourceOfTruth_KillMidStream simulates an abrupt process
// kill mid-provider-stream and verifies:
//
//   - No committed event row is lost (MaxSeq in the reopened DB >= the
//     last seq we appended before the kill).
//   - Replaying the log produces a projection bit-identical to the live
//     projection at the moment of the kill.
//
// Abrupt kill is simulated by closing the DB handle without doing any
// graceful flush (modernc/sqlite WAL flushes on each COMMIT, so committed
// inserts must already be durable on disk by the time we return from
// Append; that's the invariant we're checking).
func TestEventLogIsSourceOfTruth_KillMidStream(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	ctx := context.Background()

	log, err := agent.OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Build a long script and cut it off partway through.
	long := append(fake.CannedScript(), fake.CannedScript()...)
	long = append(long, fake.CannedScript()...)
	cut := len(long) - 4 // drop the final tool + stop sequence
	p := fake.New("fake", long).WithStopAfter(cut)

	live := drive(t, ctx, log, p, "agent-kill", "long convo", "fake-model")
	wantJSON, err := live.CanonicalJSON()
	if err != nil {
		t.Fatalf("live snapshot: %v", err)
	}
	preKillSeq, err := agent.MaxSeq(ctx, log.DB())
	if err != nil {
		t.Fatalf("max seq pre-kill: %v", err)
	}

	// Simulate abrupt kill: drop the handle without a normal teardown.
	// (We still need to Close to release the file lock for reopen on macOS,
	// but we deliberately do NOT call any explicit checkpoint / sync first.)
	if err := log.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen and verify.
	log2, err := agent.OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer log2.Close()

	postSeq, err := agent.MaxSeq(ctx, log2.DB())
	if err != nil {
		t.Fatalf("max seq post-kill: %v", err)
	}
	if postSeq < preKillSeq {
		t.Fatalf("committed events lost: pre-kill maxSeq=%d, post-kill maxSeq=%d", preKillSeq, postSeq)
	}

	replayed, err := agent.ReplayAll(ctx, log2)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	gotJSON, err := replayed.CanonicalJSON()
	if err != nil {
		t.Fatalf("replayed snapshot: %v", err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("post-kill projection drift:\nwant=%s\ngot =%s", wantJSON, gotJSON)
	}
}

// TestEventLogIsSourceOfTruth_OSExitKill is the *strongest* form of the
// kill test: it spawns a subprocess that opens the DB, appends N events,
// then exits via os.Exit(1) WITHOUT closing the DB. The parent then
// reopens and verifies durability.
//
// Skipped automatically if the test binary isn't being re-invoked as the
// child (controlled via CARLOS_PREFLIGHT_KILL_CHILD env).
func TestEventLogIsSourceOfTruth_OSExitKill(t *testing.T) {
	if os.Getenv("CARLOS_PREFLIGHT_KILL_CHILD") == "1" {
		// Child: open the DB at the path the parent passed, append events,
		// then exit hard without close.
		dbPath := os.Getenv("CARLOS_PREFLIGHT_DB")
		nEvents := 50
		ctx := context.Background()
		log, err := agent.OpenSQLiteEventLog(dbPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, "child open:", err)
			os.Exit(2)
		}
		// Create agent first.
		created, _ := agent.NewStateChangeCreated(agent.AgentCreated{
			ID: "child-agent", RootID: "child-agent", Title: "child", Model: "fake",
		})
		if _, err := log.Append(ctx, agent.Event{AgentID: "child-agent", TS: time.Now().UTC(), Type: agent.EvtStateChange, Payload: created}); err != nil {
			fmt.Fprintln(os.Stderr, "child append created:", err)
			os.Exit(3)
		}
		for i := 0; i < nEvents; i++ {
			p, _ := json.Marshal(agent.TokenUsage{DeltaOut: int64(i)})
			if _, err := log.Append(ctx, agent.Event{AgentID: "child-agent", TS: time.Now().UTC(), Type: agent.EvtTokenUsage, Payload: p}); err != nil {
				fmt.Fprintln(os.Stderr, "child append:", err)
				os.Exit(4)
			}
		}
		// HARD exit: no Close, no defer, no graceful anything.
		os.Exit(99)
	}

	// Parent: locate the test binary, set up a temp DB, exec ourselves.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("exe: %v", err)
	}

	// Re-run only this test, with the env that flips us into the child branch.
	cmd := exec.Command(exe, "-test.run=TestEventLogIsSourceOfTruth_OSExitKill", "-test.timeout=30s")
	cmd.Env = append(os.Environ(),
		"CARLOS_PREFLIGHT_KILL_CHILD=1",
		"CARLOS_PREFLIGHT_DB="+dbPath,
	)
	out, err := cmd.CombinedOutput()
	// We *expect* exit code 99 (signals hard-kill simulation succeeded).
	if err == nil {
		t.Fatalf("child exited 0 unexpectedly; output:\n%s", out)
	}
	// On macOS exec.ExitError; we don't decode the code beyond "non-zero".

	// Parent reopens and validates.
	ctx := context.Background()
	log, err := agent.OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("parent reopen: %v", err)
	}
	defer log.Close()

	n, err := agent.CountEvents(ctx, log)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	// 1 created + 50 token_usage = 51 expected.
	const want = int64(51)
	if n != want {
		t.Fatalf("after hard child kill: got %d events on disk, want %d", n, want)
	}
	replayed, err := agent.ReplayAll(ctx, log)
	if err != nil {
		t.Fatalf("replay after kill: %v", err)
	}
	rows := replayed.Snapshot()
	if len(rows) != 1 || rows[0].ID != "child-agent" {
		t.Fatalf("unexpected replay: %+v", rows)
	}
	if rows[0].TokensOut != sumIota(50) {
		t.Fatalf("projection tokens_out = %d, want %d", rows[0].TokensOut, sumIota(50))
	}
}

func sumIota(n int) int64 {
	var s int64
	for i := 0; i < n; i++ {
		s += int64(i)
	}
	return s
}
