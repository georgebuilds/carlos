// carlos run / jobs / attach / logs / stop — the CLI surface for
// daemon-owned background shell jobs.
//
// These mirror Claude Code's `--exec` model: a background job runs in the
// long-lived daemon process, not the foreground CLI, so it survives this
// command (and any TUI session) exiting. Every verb talks to the running
// daemon over the same UDS the `daemon status` / `gateway test` verbs use.
//
//	carlos run "<cmd>"   launch a detached job, print its id
//	carlos jobs          list the daemon's jobs
//	carlos attach <id>   stream a job's output until it finishes
//	carlos logs <id>     print a job's output once (disk fallback)
//	carlos stop <id>     cancel a running job
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/georgebuilds/carlos/internal/daemon"
)

// attachPollInterval is how often `carlos attach` polls the daemon for
// new output. 250ms keeps the stream feeling live without hammering the
// UDS; each poll is a fresh short-lived connection.
const attachPollInterval = 250 * time.Millisecond

// daemonNotRunningMsg is the shared hint when the UDS dial fails. Job
// verbs need a live daemon (it owns the runtime); surface how to start it.
const daemonNotRunningMsg = "daemon not running - background jobs live in the daemon. Start it with `carlos daemon enable` (auto-start) or `carlos daemon run` (foreground)."

// runJobRun launches a detached background job: `carlos run "<cmd>"`.
// Exactly one positional argument (the command), matching `carlos please`
// — multi-token commands must be quoted by the shell.
func runJobRun(args []string) error {
	if len(args) != 1 {
		return errors.New(`run: usage - carlos run "<command>"`)
	}
	command := args[0]
	cwd, _ := os.Getwd()
	conn, err := daemon.Dial("")
	if err != nil {
		return errors.New(daemonNotRunningMsg)
	}
	defer conn.Close()
	resp, err := daemon.SendRequest(conn, daemon.Request{Cmd: "jobs-spawn", Command: command, Cwd: cwd})
	if err != nil {
		return fmt.Errorf("run: %w", err)
	}
	if !resp.Ok {
		return errors.New(resp.Msg)
	}
	if resp.Job != nil {
		fmt.Printf("started job %s\n", resp.Job.ID)
		fmt.Printf("  attach: carlos attach %s\n", resp.Job.ID)
	} else {
		fmt.Println(resp.Msg)
	}
	return nil
}

// runJobsList renders the daemon's background-job roster.
func runJobsList(args []string) error {
	if len(args) > 0 {
		return errors.New("jobs: usage - carlos jobs (no arguments)")
	}
	conn, err := daemon.Dial("")
	if err != nil {
		return errors.New(daemonNotRunningMsg)
	}
	defer conn.Close()
	resp, err := daemon.SendRequest(conn, daemon.Request{Cmd: "jobs-list"})
	if err != nil {
		return fmt.Errorf("jobs: %w", err)
	}
	if !resp.Ok {
		return errors.New(resp.Msg)
	}
	if len(resp.Jobs) == 0 {
		fmt.Println("no background jobs (start one with `carlos run \"<command>\"`)")
		return nil
	}
	for _, j := range resp.Jobs {
		dur := ""
		if j.DurationMS > 0 {
			dur = (time.Duration(j.DurationMS) * time.Millisecond).Truncate(time.Millisecond).String()
		}
		fmt.Printf("%s  %-9s  %-8s  %s\n", j.ID, j.State, dur, j.Command)
	}
	return nil
}

// runJobAttach streams a job's output until it reaches a terminal state.
// Polls jobs-logs by byte offset so each iteration only prints the delta.
// Ctrl+C detaches the CLI without stopping the daemon-owned job.
func runJobAttach(args []string) error {
	if len(args) != 1 {
		return errors.New("attach: usage - carlos attach <job-id>")
	}
	jobID := args[0]
	offset := 0
	for {
		conn, err := daemon.Dial("")
		if err != nil {
			return errors.New(daemonNotRunningMsg)
		}
		resp, rerr := daemon.SendRequest(conn, daemon.Request{Cmd: "jobs-logs", JobID: jobID, Offset: offset})
		_ = conn.Close()
		if rerr != nil {
			return fmt.Errorf("attach: %w", rerr)
		}
		if !resp.Ok {
			return errors.New(resp.Msg)
		}
		if resp.Output != "" {
			fmt.Print(resp.Output)
		}
		offset = resp.NextOffset
		if resp.Done {
			return nil
		}
		time.Sleep(attachPollInterval)
	}
}

// runJobLogs prints a job's captured output once. Tries the daemon first
// (covers still-running jobs whose output is only in the ring buffer);
// falls back to the on-disk <id>.log so logs are still readable after a
// daemon restart dropped the in-memory roster.
func runJobLogs(args []string) error {
	if len(args) != 1 {
		return errors.New("logs: usage - carlos logs <job-id>")
	}
	jobID := args[0]
	conn, err := daemon.Dial("")
	if err == nil {
		defer conn.Close()
		resp, rerr := daemon.SendRequest(conn, daemon.Request{Cmd: "jobs-logs", JobID: jobID, Offset: 0})
		if rerr == nil && resp.Ok {
			fmt.Print(resp.Output)
			return nil
		}
		// daemon up but doesn't know the job (e.g. restarted) → disk.
	}
	return printJobLogFromDisk(jobID)
}

// printJobLogFromDisk reads ~/.carlos/jobs/<id>.log directly. The
// fallback path when the daemon is down or has forgotten the job.
func printJobLogFromDisk(jobID string) error {
	home, herr := os.UserHomeDir()
	if herr != nil || home == "" {
		return errors.New(daemonNotRunningMsg)
	}
	path := filepath.Join(home, ".carlos", "jobs", jobID+".log")
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		if errors.Is(rerr, os.ErrNotExist) {
			return fmt.Errorf("logs: no output on disk for job %q (and the daemon doesn't know it)", jobID)
		}
		return fmt.Errorf("logs: read %s: %w", path, rerr)
	}
	os.Stdout.Write(data)
	return nil
}

// runJobStop cancels a running daemon job.
func runJobStop(args []string) error {
	if len(args) != 1 {
		return errors.New("stop: usage - carlos stop <job-id>")
	}
	jobID := args[0]
	conn, err := daemon.Dial("")
	if err != nil {
		return errors.New(daemonNotRunningMsg)
	}
	defer conn.Close()
	resp, rerr := daemon.SendRequest(conn, daemon.Request{Cmd: "jobs-stop", JobID: jobID})
	if rerr != nil {
		return fmt.Errorf("stop: %w", rerr)
	}
	if !resp.Ok {
		return errors.New(resp.Msg)
	}
	fmt.Println(resp.Msg)
	return nil
}
