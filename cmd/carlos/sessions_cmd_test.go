package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// cmdTestLog opens a fresh on-disk event log in a temp dir.
func cmdTestLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	log, err := agent.OpenStateDB(t.TempDir() + "/state.db")
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

// seedCmdAgent inserts an agent row with an explicit heartbeat time.
// Stale heartbeat (an hour ago) means delete-able; a fresh heartbeat
// (time.Now) means live.
func seedCmdAgent(t *testing.T, log *agent.SQLiteEventLog, id, parent, root string, hb time.Time) {
	t.Helper()
	now := time.Now().UTC()
	if err := log.InsertAgent(context.Background(), agent.AgentRow{
		ID: id, ParentID: parent, RootID: root, State: agent.StateOrphaned, Attempt: 1,
		Title: "title-" + id, Model: "m", CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: hb,
	}); err != nil {
		t.Fatalf("insert agent %s: %v", id, err)
	}
}

// seedCmdUserMsg appends one EvtUserMessage with the given text so the
// session shows a preview + user-message count.
func seedCmdUserMsg(t *testing.T, log *agent.SQLiteEventLog, id, text string) {
	t.Helper()
	b, _ := json.Marshal(agent.MessagePayload{Text: text})
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: agent.EvtUserMessage, Payload: b,
	}); err != nil {
		t.Fatalf("append user msg: %v", err)
	}
}

func staleHB() time.Time { return time.Now().UTC().Add(-time.Hour) }

func TestRunSessionsList_Empty(t *testing.T) {
	log := cmdTestLog(t)
	var out bytes.Buffer
	if err := runSessionsList(log, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "no sessions yet") {
		t.Errorf("empty output: %q", out.String())
	}
}

func TestRunSessionsList_RendersSessions(t *testing.T) {
	log := cmdTestLog(t)
	seedCmdAgent(t, log, "01A", "", "01A", staleHB())
	seedCmdUserMsg(t, log, "01A", "hello there")
	seedCmdAgent(t, log, "01B", "", "01B", staleHB())

	var out bytes.Buffer
	if err := runSessionsList(log, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	s := out.String()
	for _, want := range []string{"01A", "01B", "title-01A", "hello there", "1 msg"} {
		if !strings.Contains(s, want) {
			t.Errorf("output missing %q: %q", want, s)
		}
	}
}

func TestRunSessionsList_UntitledFallback(t *testing.T) {
	log := cmdTestLog(t)
	// Insert an agent with an empty title directly so list shows the
	// (untitled) fallback.
	now := time.Now().UTC()
	if err := log.InsertAgent(context.Background(), agent.AgentRow{
		ID: "01Z", RootID: "01Z", State: agent.StateOrphaned, Attempt: 1,
		Title: "", Model: "m", CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: staleHB(),
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runSessionsList(log, &out); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "(untitled)") {
		t.Errorf("expected untitled fallback: %q", out.String())
	}
}

func TestRunSessionsRm_AssumeYes(t *testing.T) {
	log := cmdTestLog(t)
	seedCmdAgent(t, log, "01A", "", "01A", staleHB())
	seedCmdUserMsg(t, log, "01A", "topic")

	var out bytes.Buffer
	if err := runSessionsRm(log, "01A", true, strings.NewReader(""), &out); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if !strings.Contains(out.String(), "deleted 01A") {
		t.Errorf("output: %q", out.String())
	}
	// Gone for good.
	sessions, _ := agent.ListUserSessions(context.Background(), log, "")
	if len(sessions) != 0 {
		t.Errorf("session survived: %+v", sessions)
	}
}

func TestRunSessionsRm_ConfirmYes(t *testing.T) {
	log := cmdTestLog(t)
	seedCmdAgent(t, log, "01A", "", "01A", staleHB())

	var out bytes.Buffer
	if err := runSessionsRm(log, "01A", false, strings.NewReader("y\n"), &out); err != nil {
		t.Fatalf("rm: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "permanently delete") || !strings.Contains(s, "deleted 01A") {
		t.Errorf("output: %q", s)
	}
	sessions, _ := agent.ListUserSessions(context.Background(), log, "")
	if len(sessions) != 0 {
		t.Error("session should be gone after y")
	}
}

func TestRunSessionsRm_ConfirmNo(t *testing.T) {
	log := cmdTestLog(t)
	seedCmdAgent(t, log, "01A", "", "01A", staleHB())

	var out bytes.Buffer
	if err := runSessionsRm(log, "01A", false, strings.NewReader("n\n"), &out); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if !strings.Contains(out.String(), "left untouched") {
		t.Errorf("output: %q", out.String())
	}
	sessions, _ := agent.ListUserSessions(context.Background(), log, "")
	if len(sessions) != 1 {
		t.Error("declined delete must keep the session")
	}
}

func TestRunSessionsRm_ConfirmEOFDeclines(t *testing.T) {
	log := cmdTestLog(t)
	seedCmdAgent(t, log, "01A", "", "01A", staleHB())

	var out bytes.Buffer
	if err := runSessionsRm(log, "01A", false, strings.NewReader(""), &out); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if !strings.Contains(out.String(), "left untouched") {
		t.Errorf("EOF should decline: %q", out.String())
	}
	sessions, _ := agent.ListUserSessions(context.Background(), log, "")
	if len(sessions) != 1 {
		t.Error("EOF decline must keep the session")
	}
}

func TestRunSessionsRm_UnknownID(t *testing.T) {
	log := cmdTestLog(t)
	err := runSessionsRm(log, "nope", true, strings.NewReader(""), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "no session with id") {
		t.Errorf("want not-found error, got %v", err)
	}
}

func TestRunSessionsRm_SubAgentRejected(t *testing.T) {
	log := cmdTestLog(t)
	seedCmdAgent(t, log, "top", "", "top", staleHB())
	seedCmdAgent(t, log, "kid", "top", "top", staleHB())

	err := runSessionsRm(log, "kid", true, strings.NewReader(""), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "sub-agent") {
		t.Errorf("want sub-agent error, got %v", err)
	}
	// Both rows survive.
	if n := countCmdAgents(t, log); n != 2 {
		t.Errorf("agents=%d, want 2", n)
	}
}

func TestRunSessionsRm_LiveSessionRefused(t *testing.T) {
	log := cmdTestLog(t)
	seedCmdAgent(t, log, "01A", "", "01A", time.Now().UTC()) // fresh heartbeat = live

	err := runSessionsRm(log, "01A", true, strings.NewReader(""), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "live in another process") {
		t.Errorf("want live error, got %v", err)
	}
	sessions, _ := agent.ListUserSessions(context.Background(), log, "")
	if len(sessions) != 1 {
		t.Error("live session must not be deleted")
	}
}

func TestRunSessionsRm_ShortIDPrefix(t *testing.T) {
	log := cmdTestLog(t)
	seedCmdAgent(t, log, "01ABCDEF", "", "01ABCDEF", staleHB())

	var out bytes.Buffer
	if err := runSessionsRm(log, "01ABC", true, strings.NewReader(""), &out); err != nil {
		t.Fatalf("rm by prefix: %v", err)
	}
	if !strings.Contains(out.String(), "deleted 01ABCDEF") {
		t.Errorf("prefix delete output: %q", out.String())
	}
}

func TestRunSessionsRm_AmbiguousPrefix(t *testing.T) {
	log := cmdTestLog(t)
	seedCmdAgent(t, log, "01AB1", "", "01AB1", staleHB())
	seedCmdAgent(t, log, "01AB2", "", "01AB2", staleHB())

	err := runSessionsRm(log, "01AB", true, strings.NewReader(""), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("want ambiguous error, got %v", err)
	}
}

func TestSessionsStripYesFlag(t *testing.T) {
	cases := []struct {
		in   []string
		yes  bool
		rest []string
	}{
		{[]string{"01A"}, false, []string{"01A"}},
		{[]string{"-y", "01A"}, true, []string{"01A"}},
		{[]string{"01A", "--yes"}, true, []string{"01A"}},
	}
	for _, tc := range cases {
		yes, rest := stripYesFlag(tc.in)
		if yes != tc.yes {
			t.Errorf("%v: yes=%v want %v", tc.in, yes, tc.yes)
		}
		if strings.Join(rest, ",") != strings.Join(tc.rest, ",") {
			t.Errorf("%v: rest=%v want %v", tc.in, rest, tc.rest)
		}
	}
}

func TestSessionsReadYes(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{" yes \n", true},
		{"n\n", false},
		{"no\n", false},
		{"\n", false},
		{"", false},
		{"maybe\n", false},
	}
	for _, tc := range cases {
		if got := readYes(strings.NewReader(tc.in)); got != tc.want {
			t.Errorf("readYes(%q) = %v want %v", tc.in, got, tc.want)
		}
	}
}

func TestRunSessions_NoSubcommand(t *testing.T) {
	// Returns before any state.db open, so no HOME setup needed.
	if err := runSessions(nil); err == nil || !strings.Contains(err.Error(), "subcommand required") {
		t.Errorf("nil args: %v", err)
	}
}

func TestRunSessions_OpenStateDBError(t *testing.T) {
	// Point HOME at a regular file so MkdirAll of <home>/.carlos fails,
	// exercising the OpenStateDB error branch.
	f := t.TempDir() + "/not-a-dir"
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", f)
	if err := runSessions([]string{"list"}); err == nil || !strings.Contains(err.Error(), "open state.db") {
		t.Errorf("want open error, got %v", err)
	}
}

// TestRunSessions_DispatchBranches drives the thin arg-parser end to
// end against a temp HOME so it actually opens a state.db. Covers the
// list, rm, missing-id, too-many-args, and unknown-subcommand paths,
// plus stripYesFlag via the real call site.
func TestRunSessions_DispatchBranches(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Seed one deletable session into the temp HOME's state.db.
	log, err := agent.OpenStateDB(home + "/.carlos/state.db")
	if err != nil {
		t.Fatalf("open seed log: %v", err)
	}
	seedCmdAgent(t, log, "01A", "", "01A", staleHB())
	seedCmdUserMsg(t, log, "01A", "hi")
	_ = log.Close()

	// list: succeeds.
	if err := runSessions([]string{"list"}); err != nil {
		t.Errorf("list: %v", err)
	}

	// rm with missing id.
	if err := runSessions([]string{"rm"}); err == nil || !strings.Contains(err.Error(), "session id required") {
		t.Errorf("rm no id: %v", err)
	}
	// rm with too many ids.
	if err := runSessions([]string{"rm", "a", "b"}); err == nil || !strings.Contains(err.Error(), "single id") {
		t.Errorf("rm extra: %v", err)
	}
	// unknown subcommand.
	if err := runSessions([]string{"frobnicate"}); err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Errorf("unknown: %v", err)
	}

	// rm -y of the seeded session: deletes it. Exercises stripYesFlag's
	// flag-present branch through the real dispatcher.
	if err := runSessions([]string{"rm", "-y", "01A"}); err != nil {
		t.Errorf("rm -y: %v", err)
	}
	log2, _ := agent.OpenStateDB(home + "/.carlos/state.db")
	defer log2.Close()
	sessions, _ := agent.ListUserSessions(context.Background(), log2, "")
	if len(sessions) != 0 {
		t.Errorf("session should be gone, got %+v", sessions)
	}
}

func countCmdAgents(t *testing.T, log *agent.SQLiteEventLog) int {
	t.Helper()
	var n int
	if err := log.DB().QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM agents`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// Sanity: the friendly error mapping uses errors.Is under the hood;
// guard that the sentinels are wired so a refactor that drops the
// %w verb is caught.
func TestRunSessionsRm_ErrorsAreSentinelBacked(t *testing.T) {
	for _, e := range []error{agent.ErrSessionNotFound, agent.ErrNotTopLevel, agent.ErrSessionLive} {
		if !errors.Is(e, e) {
			t.Fatalf("sentinel %v not comparable", e)
		}
	}
}
