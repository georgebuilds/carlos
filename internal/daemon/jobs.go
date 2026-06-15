package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/usershell"
)

// startJobsRuntime constructs the daemon-owned usershell.Manager that
// backs `carlos run`/attach/logs/stop. Called once from Run. Best-effort:
// a jobs.db open failure logs + degrades to an in-memory-only runtime
// rather than aborting daemon startup — a background job that runs but
// isn't audited still beats refusing to schedule.
func (d *Daemon) startJobsRuntime() {
	dir := d.jobsDir()
	opts := usershell.Options{OutputDir: dir}
	if dbPath := d.jobsDBPath(); dbPath != "" {
		if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
			d.slogger().Warn("jobs runtime: mkdir jobs.db parent failed, persistence off", "err", err)
		} else if log, err := agent.OpenStateDB(dbPath); err != nil {
			d.slogger().Warn("jobs runtime: open jobs.db failed, persistence off", "path", dbPath, "err", err)
		} else {
			d.jobsLog = log
			opts.Log = log
		}
	}
	mgr := usershell.New(opts)
	d.mu.Lock()
	d.jobsMgr = mgr
	d.mu.Unlock()
	d.slogger().Info("jobs runtime ready", "dir", dir, "persisted", opts.Log != nil)
}

// stopJobsRuntime cancels every still-running daemon job and closes the
// jobs.db handle. Deferred from Run so it fires on any shutdown path.
// Idempotent against a nil runtime (test-mode daemons that never called
// startJobsRuntime).
func (d *Daemon) stopJobsRuntime() {
	d.mu.Lock()
	mgr := d.jobsMgr
	log := d.jobsLog
	d.jobsMgr = nil
	d.jobsLog = nil
	d.mu.Unlock()
	if mgr != nil {
		_ = mgr.Close()
	}
	if log != nil {
		_ = log.Close()
	}
}

// jobsDir resolves the per-job output directory. Honors Options.JobsDir
// (tests), then ~/.carlos/jobs, then a relative fallback so a missing
// $HOME never panics.
func (d *Daemon) jobsDir() string {
	if d.opts.JobsDir != "" {
		return d.opts.JobsDir
	}
	if d.opts.Home != "" {
		return filepath.Join(d.opts.Home, ".carlos", "jobs")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".carlos", "jobs")
	}
	return filepath.Join(".carlos", "jobs")
}

// jobsDBPath resolves the jobs.db event-log path. Honors
// Options.JobsDBPath (tests), then ~/.carlos/jobs.db. Returns "" when no
// home is resolvable, which startJobsRuntime treats as "persistence off".
func (d *Daemon) jobsDBPath() string {
	if d.opts.JobsDBPath != "" {
		return d.opts.JobsDBPath
	}
	if d.opts.Home != "" {
		return filepath.Join(d.opts.Home, ".carlos", "jobs.db")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".carlos", "jobs.db")
	}
	return ""
}

// --- IPC handlers ------------------------------------------------------

// jobsSpawnResponse launches a new background job in the daemon runtime
// and returns its freshly-minted status. The job runs in req.Cwd (or the
// daemon's own cwd when empty) and is always Background — foreground
// semantics are a TUI-interactive concept that has no meaning across the
// IPC boundary.
func (d *Daemon) jobsSpawnResponse(req Request) Response {
	mgr := d.jobsManager()
	if mgr == nil {
		return Response{Ok: false, Msg: "jobs-spawn: job runtime not available on this daemon"}
	}
	if req.Command == "" {
		return Response{Ok: false, Msg: "jobs-spawn: command required"}
	}
	// context.Background, NOT a request-scoped ctx: Submit's first
	// promotion derives the job's cancellation context from the ctx we
	// pass, so a deferred cancel here would immediately kill the very
	// job we just launched. Submit is non-blocking, so no timeout is
	// needed; the daemon owns job lifetime via Cancel / Close.
	job, err := mgr.SubmitIn(context.Background(), req.Command, req.Cwd, usershell.Background)
	if err != nil {
		return Response{Ok: false, Msg: fmt.Sprintf("jobs-spawn: %v", err)}
	}
	st := jobStatusFromSnapshot(job.Snapshot())
	return Response{Ok: true, Msg: fmt.Sprintf("started job %s", st.ID), Job: &st}
}

// jobsListResponse returns every job the daemon runtime knows about, in
// the Manager's stable order (pending → running → recent).
func (d *Daemon) jobsListResponse() Response {
	mgr := d.jobsManager()
	if mgr == nil {
		return Response{Ok: false, Msg: "jobs-list: job runtime not available on this daemon"}
	}
	snaps := mgr.Jobs()
	out := make([]JobStatus, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, jobStatusFromSnapshot(s))
	}
	return Response{Ok: true, Msg: fmt.Sprintf("%d job(s)", len(out)), Jobs: out}
}

// jobsGetResponse returns one job's status, or ok=false if the runtime
// doesn't know the id.
func (d *Daemon) jobsGetResponse(jobID string) Response {
	mgr := d.jobsManager()
	if mgr == nil {
		return Response{Ok: false, Msg: "jobs-get: job runtime not available on this daemon"}
	}
	if jobID == "" {
		return Response{Ok: false, Msg: "jobs-get: job_id required"}
	}
	snap, err := mgr.Get(jobID)
	if err != nil {
		return Response{Ok: false, Msg: fmt.Sprintf("jobs-get: %v", err)}
	}
	st := jobStatusFromSnapshot(snap)
	return Response{Ok: true, Job: &st}
}

// jobsLogsResponse returns the captured output of one job from req.Offset
// onward, plus the next offset to poll from and whether the job has
// reached a terminal state. An `attach` client loops this verb with the
// returned NextOffset until Done flips true. Unknown id → ok=false so
// the CLI can fall back to reading the on-disk <id>.log directly.
func (d *Daemon) jobsLogsResponse(jobID string, offset int) Response {
	mgr := d.jobsManager()
	if mgr == nil {
		return Response{Ok: false, Msg: "jobs-logs: job runtime not available on this daemon"}
	}
	if jobID == "" {
		return Response{Ok: false, Msg: "jobs-logs: job_id required"}
	}
	snap, err := mgr.Get(jobID)
	if err != nil {
		return Response{Ok: false, Msg: fmt.Sprintf("jobs-logs: %v", err)}
	}
	out := mgr.Output(jobID)
	if offset < 0 {
		offset = 0
	}
	if offset > len(out) {
		offset = len(out)
	}
	delta := out[offset:]
	return Response{
		Ok:         true,
		Output:     string(delta),
		NextOffset: len(out),
		Done:       snap.State.IsTerminal(),
	}
}

// jobsStopResponse cancels one running-or-pending job. Idempotent on the
// Manager side (cancelling a terminal job is a no-op), so a double stop
// still returns ok=true.
func (d *Daemon) jobsStopResponse(jobID string) Response {
	mgr := d.jobsManager()
	if mgr == nil {
		return Response{Ok: false, Msg: "jobs-stop: job runtime not available on this daemon"}
	}
	if jobID == "" {
		return Response{Ok: false, Msg: "jobs-stop: job_id required"}
	}
	if err := mgr.Cancel(jobID); err != nil {
		if errors.Is(err, usershell.ErrUnknownJob) {
			return Response{Ok: false, Msg: fmt.Sprintf("jobs-stop: unknown job %q", jobID)}
		}
		return Response{Ok: false, Msg: fmt.Sprintf("jobs-stop: %v", err)}
	}
	return Response{Ok: true, Msg: fmt.Sprintf("stopped job %s", jobID)}
}

// jobsManager reads the runtime pointer under the lock. Returns nil when
// the daemon hasn't wired one (test-mode daemons that skip Run).
func (d *Daemon) jobsManager() *usershell.Manager {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.jobsMgr
}

// jobStatusFromSnapshot projects a usershell.Snapshot into the wire shape
// the CLI consumes. Zero timestamps map to nil pointers so the JSON omits
// them rather than emitting a "0001-01-01" zero time.
func jobStatusFromSnapshot(s usershell.Snapshot) JobStatus {
	st := JobStatus{
		ID:          s.ID,
		Command:     s.Command,
		Cwd:         s.Cwd,
		State:       s.State.String(),
		ExitCode:    s.ExitCode,
		SubmittedAt: s.SubmittedAt,
		FailErr:     s.FailErrMsg,
	}
	if !s.StartedAt.IsZero() {
		t := s.StartedAt
		st.StartedAt = &t
	}
	if !s.EndedAt.IsZero() {
		t := s.EndedAt
		st.EndedAt = &t
	}
	if d := s.Duration(); d > 0 {
		st.DurationMS = d.Milliseconds()
	}
	return st
}
