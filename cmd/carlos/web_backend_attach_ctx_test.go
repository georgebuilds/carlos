// Regression tests for the web backend's thread lifetime context (the
// "attach an old chat, send a message, header flips to 'state: orphaned'"
// bug).
//
// Root cause: carlosBackend.Attach derived the per-thread context (the
// chatglue loop + the heartbeat ticker) from the ctx its caller passed
// in - which, in production, is the attach/create HTTP request's
// r.Context(). net/http cancels that context the moment the handler
// returns, so the loop goroutine and the heartbeat ticker died right
// after attach. The web process's own OrphanSweeper (10s staleness
// tolerance, 10s cadence) then saw last_heartbeat_at go stale and
// flipped the freshly attached thread to orphaned; the state_change
// event streamed out over SSE and the StageHeader painted "orphaned"
// seconds after the user sent a message (which also never got a reply,
// because the loop was already dead).
//
// The fix derives the thread context from the backend's server-lifetime
// context (lifeCtx) instead. These tests pin both directions:
//
//   - happy path: attach with a request-scoped ctx that is cancelled
//     immediately (exactly what net/http does), send a message, and the
//     turn still runs + a real-time sweep does NOT orphan the thread;
//   - bad path: when the server-lifetime ctx dies (process kill), the
//     heartbeat stops and a later sweep DOES orphan the thread - the
//     fix must not weaken genuine orphan detection.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/fake"
	"github.com/georgebuilds/carlos/internal/tools"
	"github.com/georgebuilds/carlos/internal/web"
)

// frozenClock implements agent.Clock at a fixed instant, letting a test
// run an orphan sweep "from the future" without waiting wall-clock time.
type frozenClock struct{ t time.Time }

func (c frozenClock) Now() time.Time { return c.t }
func (c frozenClock) After(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- c.t.Add(d)
	return ch
}

// scriptedWebProvider mirrors chatglue's test fake: a deterministic
// provider that streams the given deltas then ends the turn.
func scriptedWebProvider(deltas ...string) *fake.Provider {
	script := make(fake.Script, 0, len(deltas)+1)
	for _, d := range deltas {
		script = append(script, providers.Event{Kind: providers.EventTextDelta, Text: d})
	}
	script = append(script, providers.Event{Kind: providers.EventStopReason, Stop: "end_turn"})
	return fake.New("fake", script)
}

// seedOldOrphanedChat writes the fixture George's repro starts from: a
// session whose original process died long ago - real conversation
// events, a persisted orphaned state (a past Recover/sweep wrote it),
// and a heartbeat two days stale.
func seedOldOrphanedChat(t *testing.T, log *agent.SQLiteEventLog, id string) {
	t.Helper()
	ctx := context.Background()
	stale := time.Now().UTC().Add(-48 * time.Hour)

	created, err := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: id, RootID: id, Title: "old chat", Model: "fake-model",
	})
	if err != nil {
		t.Fatalf("marshal created: %v", err)
	}
	appendEv := func(typ agent.EventType, payload []byte) {
		t.Helper()
		if _, err := log.Append(ctx, agent.Event{
			AgentID: id, TS: stale, Type: typ, Payload: payload,
		}); err != nil {
			t.Fatalf("seed append %s: %v", typ, err)
		}
	}
	appendEv(agent.EvtStateChange, created)
	userMsg, _ := json.Marshal(agent.MessagePayload{Text: "hi from last week"})
	appendEv(agent.EvtUserMessage, userMsg)
	asstMsg, _ := json.Marshal(agent.MessagePayload{Text: "hello from last week"})
	appendEv(agent.EvtAssistantMessage, asstMsg)
	orphaned, err := agent.NewStateChangeTransition(agent.StateOrphaned)
	if err != nil {
		t.Fatalf("marshal orphaned transition: %v", err)
	}
	appendEv(agent.EvtStateChange, orphaned)

	if err := log.InsertAgent(ctx, agent.AgentRow{
		ID: id, RootID: id, State: agent.StateOrphaned, Attempt: 1,
		Title: "old chat", Model: "fake-model",
		CreatedAt: stale, UpdatedAt: stale, LastHeartbeatAt: stale,
	}); err != nil {
		t.Fatalf("seed insert agent: %v", err)
	}
}

// newTestWebBackend assembles a carlosBackend the way newCarlosBackend
// does, but with a scripted fake provider so a turn can run hermetically.
// serverCtx plays the role of `carlos web`'s signal context.
func newTestWebBackend(t *testing.T, serverCtx context.Context, log *agent.SQLiteEventLog, deltas ...string) *carlosBackend {
	t.Helper()
	srv := web.NewServer(web.Options{Log: log, Token: "test-token"})
	prov := scriptedWebProvider(deltas...)
	reg := tools.NewRegistry()
	sup := agent.NewSupervisor(log, prov, reg)
	sup.Run(serverCtx)
	t.Cleanup(sup.Shutdown)
	b := &carlosBackend{
		cfg:       &config.Config{UserName: "george"},
		log:       log,
		sup:       sup,
		parent:    reg,
		src:       web.NewWebTextSource(srv.Hub()),
		hub:       srv,
		dispatch:  &dispatch{provider: prov, name: "fake", model: "fake-model"},
		lifeCtx:   serverCtx,
		attached:  map[string]*webThread{},
		frameRoot: map[string]string{},
	}
	t.Cleanup(b.Shutdown)
	return b
}

func openWebTestLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	log, err := agent.OpenSQLiteEventLog(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

// requireState asserts the projection row's state for id.
func requireState(t *testing.T, log *agent.SQLiteEventLog, id string, want agent.State) {
	t.Helper()
	row, ok, err := log.GetAgent(context.Background(), id)
	if err != nil || !ok {
		t.Fatalf("GetAgent(%s): ok=%v err=%v", id, ok, err)
	}
	if row.State != want {
		t.Fatalf("agent %s state = %s, want %s", id, row.State, want)
	}
}

// waitForWebAssistant polls the log until an EvtAssistantMessage whose
// text contains want lands, failing fast on a surfaced loop error.
func waitForWebAssistant(t *testing.T, log *agent.SQLiteEventLog, id, want string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		evs, _ := log.Read(context.Background(), id, 0)
		for _, ev := range evs {
			if ev.Type != agent.EvtAssistantMessage {
				continue
			}
			var p agent.MessagePayload
			_ = json.Unmarshal(ev.Payload, &p)
			if strings.Contains(p.Text, want) {
				return
			}
			if strings.HasPrefix(p.Text, "[carlos-error]") {
				t.Fatalf("loop surfaced an error instead of a turn: %s", p.Text)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no assistant reply containing %q: the attached thread's loop is dead "+
		"(thread context was cancelled with the attach request)", want)
}

// TestWebAttach_SurvivesRequestContextCancel reproduces George's bug:
// attach an old (persisted-orphaned, stale-heartbeat) chat through the
// same call shape handleAttach uses - a request-scoped ctx that net/http
// cancels when the handler returns - then send a message. The thread
// must un-orphan at attach, run the turn, and a sweep at real time must
// leave it alive.
func TestWebAttach_SurvivesRequestContextCancel(t *testing.T) {
	log := openWebTestLog(t)
	const id = "old-chat-1"
	seedOldOrphanedChat(t, log, id)

	serverCtx, cancelServer := context.WithCancel(context.Background())
	t.Cleanup(cancelServer)
	b := newTestWebBackend(t, serverCtx, log, "hey ", "boss, welcome back")

	// handleAttach: backend.Attach(r.Context(), id), then the handler
	// returns and net/http cancels r.Context().
	reqCtx, cancelReq := context.WithCancel(context.Background())
	if err := b.Attach(reqCtx, id); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	cancelReq()

	if !b.Attached(id) {
		t.Fatal("thread not attached after Attach")
	}
	// Attach must have un-orphaned the persisted row (hypothesis 2).
	requireState(t, log, id, agent.StateRunning)

	// handleMessage: a fresh request ctx that also dies right after.
	sendCtx, cancelSend := context.WithCancel(context.Background())
	if _, err := b.Send(sendCtx, id, "hello again"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	cancelSend()

	// The turn must run: the loop's lifetime is the server's, not the
	// attach request's.
	waitForWebAssistant(t, log, id, "welcome back")

	// A sweep at real time (the web process's own sweeper) must NOT
	// orphan the attached thread: its heartbeat was refreshed at attach.
	sw := agent.NewOrphanSweeper(log, nil, 0, 0)
	if err := sw.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	requireState(t, log, id, agent.StateRunning)
}

// TestWebAttach_DeadProcessStillOrphans is the bad path: the fix must not
// weaken real orphan detection. Kill the server-lifetime context (process
// death) after attaching; with heartbeats stopped, a later sweep must
// flip the thread to orphaned.
func TestWebAttach_DeadProcessStillOrphans(t *testing.T) {
	log := openWebTestLog(t)
	const id = "old-chat-2"
	seedOldOrphanedChat(t, log, id)

	serverCtx, cancelServer := context.WithCancel(context.Background())
	b := newTestWebBackend(t, serverCtx, log, "ok")

	reqCtx, cancelReq := context.WithCancel(context.Background())
	if err := b.Attach(reqCtx, id); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	cancelReq()
	requireState(t, log, id, agent.StateRunning)

	// Process death: the server-lifetime ctx dies, taking the loop and
	// the heartbeat ticker with it.
	cancelServer()

	// A sweep from one hour in the future (well past any heartbeat the
	// dead ticker could have written) must orphan the thread.
	sw := agent.NewOrphanSweeper(log, frozenClock{time.Now().UTC().Add(time.Hour)}, 0, 0)
	if err := sw.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	requireState(t, log, id, agent.StateOrphaned)
}

// TestWebAttach_GuardsStillHold pins the two refusal paths around the
// fixed code: re-attaching an owned thread is a no-op, and a thread with
// a fresh foreign heartbeat (a live TUI session) is refused with
// ErrThreadOwned - the fix must not loosen the single-owner invariant.
func TestWebAttach_GuardsStillHold(t *testing.T) {
	log := openWebTestLog(t)
	serverCtx, cancelServer := context.WithCancel(context.Background())
	t.Cleanup(cancelServer)
	b := newTestWebBackend(t, serverCtx, log, "ok")

	// Idempotent re-attach: same process, same thread.
	const ours = "old-chat-4"
	seedOldOrphanedChat(t, log, ours)
	if err := b.Attach(context.Background(), ours); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := b.Attach(context.Background(), ours); err != nil {
		t.Fatalf("re-Attach of an owned thread must be a no-op, got %v", err)
	}

	// Foreign owner: a row heartbeating right now (a TUI session).
	const foreign = "tui-owned-1"
	now := time.Now().UTC()
	if err := log.InsertAgent(context.Background(), agent.AgentRow{
		ID: foreign, RootID: foreign, State: agent.StateRunning, Attempt: 1,
		Title: "tui session", Model: "fake-model",
		CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
	}); err != nil {
		t.Fatalf("seed foreign agent: %v", err)
	}
	err := b.Attach(context.Background(), foreign)
	if !errors.Is(err, web.ErrThreadOwned) {
		t.Fatalf("Attach to a foreign-heartbeat thread = %v, want ErrThreadOwned", err)
	}
}

// TestWebDetach_StoppedThreadStillOrphans covers the explicit-detach
// variant of the bad path: a browser detach stops the loop + heartbeat,
// and the thread is again fair game for the sweeper. It also exercises
// the nil-lifeCtx fallback (tests that build the struct literal without
// a lifetime ctx must not panic Attach).
func TestWebDetach_StoppedThreadStillOrphans(t *testing.T) {
	log := openWebTestLog(t)
	const id = "old-chat-3"
	seedOldOrphanedChat(t, log, id)

	serverCtx, cancelServer := context.WithCancel(context.Background())
	t.Cleanup(cancelServer)
	b := newTestWebBackend(t, serverCtx, log, "ok")
	b.lifeCtx = nil // struct-literal construction path: Attach must not panic

	if err := b.Attach(context.Background(), id); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if err := b.Detach(id); err != nil {
		t.Fatalf("Detach: %v", err)
	}
	if b.Attached(id) {
		t.Fatal("thread still attached after Detach")
	}

	sw := agent.NewOrphanSweeper(log, frozenClock{time.Now().UTC().Add(time.Hour)}, 0, 0)
	if err := sw.Sweep(context.Background()); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	requireState(t, log, id, agent.StateOrphaned)
}
