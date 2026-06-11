package chat

import (
	"context"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/usershell"
)

func TestCommitJobsOverlay_RunningIsAlreadyForeground(t *testing.T) {
	mgr, _ := blockingManager(t)
	m := newTestModel(t)
	m.usershell = mgr
	m.showJobs = true
	job, _ := mgr.Submit(context.Background(), "vim", usershell.Foreground)
	waitForRunningState(t, mgr, job.ID)
	// Cursor 0 is the running fg job.
	_, cmd, _ := m.commitJobsOverlay()
	if cmd == nil {
		t.Fatal("commit on a running job should emit a status")
	}
	st := cmd().(statusMsg)
	if !strings.Contains(st.text, "already foreground") {
		t.Errorf("running commit should report already-foreground; got %+v", st)
	}
}

func TestCommitJobsOverlay_QueuedReportsWait(t *testing.T) {
	mgr, _ := blockingManager(t)
	m := newTestModel(t)
	m.usershell = mgr
	m.showJobs = true
	// First job grabs fg; the second foreground submit queues.
	fg, _ := mgr.Submit(context.Background(), "vim", usershell.Foreground)
	waitForRunningState(t, mgr, fg.ID)
	if _, err := mgr.Submit(context.Background(), "queued-cmd", usershell.Foreground); err != nil {
		t.Fatalf("submit queued: %v", err)
	}
	// Rows: [running fg, queued]. Move cursor to the queued row.
	rows := buildJobsRows(mgr.Jobs(), "")
	for i, r := range rows {
		if r.section == jobsSectionQueued {
			m.jobsCursor = i
		}
	}
	_, cmd, _ := m.commitJobsOverlay()
	if cmd == nil {
		t.Fatal("commit on a queued job should emit a status")
	}
	st := cmd().(statusMsg)
	if !strings.Contains(st.text, "queued") {
		t.Errorf("queued commit should report queued; got %+v", st)
	}
}

func TestCommitJobsOverlay_RecentReportsCompleted(t *testing.T) {
	mgr := minimalManager(t)
	defer mgr.Close()
	m := newTestModel(t)
	m.usershell = mgr
	m.showJobs = true
	job, _ := mgr.Submit(context.Background(), "true", usershell.Background)
	waitForTerminal(t, mgr, job.ID)
	_, cmd, _ := m.commitJobsOverlay()
	if cmd == nil {
		t.Fatal("commit on a recent job should emit a status")
	}
	st := cmd().(statusMsg)
	if !strings.Contains(st.text, "completed") {
		t.Errorf("recent commit should report completed; got %+v", st)
	}
}

func TestShellSlashBackground_BackgroundsSpecificJob(t *testing.T) {
	mgr, _ := blockingManager(t)
	m := newTestModel(t)
	m.usershell = mgr
	job, _ := mgr.Submit(context.Background(), "vim", usershell.Foreground)
	waitForRunningState(t, mgr, job.ID)
	// /bg <id> backgrounds the named running job.
	st := m.shellSlashBackground(job.ID)().(statusMsg)
	if !strings.Contains(st.text, "background") {
		t.Errorf("/bg <id> should move the job to background; got %+v", st)
	}
}
