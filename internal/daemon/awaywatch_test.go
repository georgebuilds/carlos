package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/usershell"
)

type awayRecorder struct {
	calls []struct {
		start usershell.StartPayload
		end   usershell.EndPayload
	}
}

func (r *awayRecorder) notify(_ context.Context, s usershell.StartPayload, e usershell.EndPayload) {
	r.calls = append(r.calls, struct {
		start usershell.StartPayload
		end   usershell.EndPayload
	}{s, e})
}

func awayTestLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	log, err := agent.OpenSQLiteEventLog(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func setPresence(t *testing.T, log *agent.SQLiteEventLog, away bool) {
	t.Helper()
	payload, _ := json.Marshal(agent.PresencePayload{Away: away})
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: agent.PresenceAgentID, TS: time.Now().UTC(), Type: agent.EvtPresence, Payload: payload,
	}); err != nil {
		t.Fatalf("append presence: %v", err)
	}
}

func startJob(t *testing.T, log *agent.SQLiteEventLog, id, cmd string, bg bool) {
	t.Helper()
	mode := "foreground"
	if bg {
		mode = "background"
	}
	if _, err := usershell.AppendStart(context.Background(), log, usershell.StartPayload{
		JobID: id, Command: cmd, Mode: mode, Background: bg, StartedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("append start: %v", err)
	}
}

func endJob(t *testing.T, log *agent.SQLiteEventLog, id string, bg bool, exit int) {
	t.Helper()
	if _, err := usershell.AppendEnd(context.Background(), log, usershell.EndPayload{
		JobID: id, Backgrounded: bg, ExitCode: exit, Duration: time.Second,
	}); err != nil {
		t.Fatalf("append end: %v", err)
	}
}

// TestAwayWatcher_NotifiesWhenAwayBackgrounded: away + a backgrounded job
// completing fires exactly one notification carrying the job's command.
func TestAwayWatcher_NotifiesWhenAwayBackgrounded(t *testing.T) {
	log := awayTestLog(t)
	setPresence(t, log, true)
	startJob(t, log, "j1", "go test ./...", true)
	endJob(t, log, "j1", true, 0)

	r := &awayRecorder{}
	w := newAwayWatcher(log, r.notify)
	w.tick(context.Background())

	if len(r.calls) != 1 {
		t.Fatalf("want 1 notification, got %d", len(r.calls))
	}
	if r.calls[0].start.Command != "go test ./..." {
		t.Errorf("notification lost the command: %q", r.calls[0].start.Command)
	}
	if r.calls[0].end.JobID != "j1" {
		t.Errorf("wrong job: %q", r.calls[0].end.JobID)
	}
}

// TestAwayWatcher_SilentWhenPresent: not away => no notification, even for a
// backgrounded completion.
func TestAwayWatcher_SilentWhenPresent(t *testing.T) {
	log := awayTestLog(t)
	setPresence(t, log, false) // explicitly back
	startJob(t, log, "j1", "sleep 5", true)
	endJob(t, log, "j1", true, 0)

	r := &awayRecorder{}
	newAwayWatcher(log, r.notify).tick(context.Background())
	if len(r.calls) != 0 {
		t.Errorf("present user should get no notifications, got %d", len(r.calls))
	}
}

// TestAwayWatcher_SkipsForegroundJobs: away but the completed job wasn't
// backgrounded => no notification (foreground jobs are interactive).
func TestAwayWatcher_SkipsForegroundJobs(t *testing.T) {
	log := awayTestLog(t)
	setPresence(t, log, true)
	startJob(t, log, "j1", "ls", false)
	endJob(t, log, "j1", false, 0)

	r := &awayRecorder{}
	newAwayWatcher(log, r.notify).tick(context.Background())
	if len(r.calls) != 0 {
		t.Errorf("foreground job should not notify, got %d", len(r.calls))
	}
}

// TestAwayWatcher_PrimeSkipsHistory: prime advances past pre-existing
// completions (so a daemon boot doesn't replay history), but forward
// operation still notifies for jobs that finish after prime.
func TestAwayWatcher_PrimeSkipsHistory(t *testing.T) {
	log := awayTestLog(t)
	setPresence(t, log, true)
	startJob(t, log, "old", "old-cmd", true)
	endJob(t, log, "old", true, 0)

	r := &awayRecorder{}
	w := newAwayWatcher(log, r.notify)
	w.prime(context.Background())
	w.tick(context.Background())
	if len(r.calls) != 0 {
		t.Fatalf("prime should skip historical completions, got %d", len(r.calls))
	}

	// A new completion after prime still fires.
	startJob(t, log, "new", "new-cmd", true)
	endJob(t, log, "new", true, 1)
	w.tick(context.Background())
	if len(r.calls) != 1 || r.calls[0].start.Command != "new-cmd" {
		t.Fatalf("forward completion not notified: %+v", r.calls)
	}
}
