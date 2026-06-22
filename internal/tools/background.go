package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// BackgroundShell is the seam the bash tool uses to dispatch a detached
// (background) job, and that BashOutput / KillShell use to read and stop
// one. It is implemented over internal/usershell.Manager by an adapter in
// the wiring layer and injected at construction time.
//
// Why an interface here rather than importing usershell directly: usershell
// imports internal/agent, which imports internal/tools, so a tools ->
// usershell import would close a cycle. Defining the seam in tools and
// injecting the concrete runner keeps the dependency arrow pointing one way.
//
// All methods take/return primitive types so the adapter never has to leak
// usershell's Job / Snapshot / State types across the boundary.
type BackgroundShell interface {
	// StartBackground spawns command in cwd as a detached background job and
	// returns its id immediately, without waiting for completion. An empty
	// cwd means "the runner's configured working directory".
	StartBackground(ctx context.Context, command, cwd string) (id string, err error)
	// Output returns the job's captured output so far plus its status:
	// state is a human-readable label ("running", "done", "failed",
	// "cancelled"), exitCode is meaningful only once done, and done reports
	// whether the job has reached a terminal state. ErrUnknownBackgroundJob
	// (wrapped) for an id the runner doesn't know.
	Output(id string) (out []byte, state string, exitCode int, done bool, err error)
	// Kill requests termination of the job. Idempotent: killing an
	// already-finished job is not an error.
	Kill(id string) error
}

// ErrUnknownBackgroundJob is returned (wrapped) by Output / Kill for an id
// the runner has no record of, so the tools can render a clear message to
// the model rather than a generic failure.
var ErrUnknownBackgroundJob = errors.New("unknown background job id")

// BashOutputTool reads the accumulated output and status of a background
// shell job started via bash with run_in_background=true. Mirrors Claude
// Code's BashOutput so prompts and skills carry over.
type BashOutputTool struct {
	Shell BackgroundShell
}

func (*BashOutputTool) Name() string { return "BashOutput" }

func (*BashOutputTool) Description() string {
	return "Read the current output and status of a background shell job previously started by bash with run_in_background=true. Returns everything the job has printed so far plus whether it is still running or has exited (with its exit code). Safe to call repeatedly to poll a long-running command."
}

func (*BashOutputTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"bash_id": {
				"type": "string",
				"description": "The id returned by the bash tool when the command was started with run_in_background=true."
			}
		},
		"required": ["bash_id"]
	}`)
}

type bashOutputInput struct {
	BashID string `json:"bash_id"`
}

func (t *BashOutputTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	if t.Shell == nil {
		return nil, errors.New("BashOutput: background shell unavailable in this context")
	}
	var in bashOutputInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("BashOutput: parse input: %w", err)
	}
	if in.BashID == "" {
		return nil, errors.New("BashOutput: empty bash_id")
	}
	out, state, exit, done, err := t.Shell.Output(in.BashID)
	if err != nil {
		// Surface unknown-id as a readable tool_result (returned as a value,
		// not an infra error) so the model can correct the id rather than
		// the loop tagging a hard failure.
		if errors.Is(err, ErrUnknownBackgroundJob) {
			return []byte(fmt.Sprintf("no background job with id %q (it may have been cleared)", in.BashID)), nil
		}
		return nil, fmt.Errorf("BashOutput: %w", err)
	}
	var b []byte
	if done {
		b = []byte(fmt.Sprintf("[job %s: %s, exit %d]\n", in.BashID, state, exit))
	} else {
		b = []byte(fmt.Sprintf("[job %s: %s]\n", in.BashID, state))
	}
	return append(b, out...), nil
}

// KillShellTool terminates a background shell job. Mirrors Claude Code's
// KillShell.
type KillShellTool struct {
	Shell BackgroundShell
}

func (*KillShellTool) Name() string { return "KillShell" }

func (*KillShellTool) Description() string {
	return "Terminate a background shell job started by bash with run_in_background=true. Use this to stop a long-running or runaway command. No-op if the job already finished."
}

func (*KillShellTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"shell_id": {
				"type": "string",
				"description": "The id returned by the bash tool when the command was started with run_in_background=true."
			}
		},
		"required": ["shell_id"]
	}`)
}

type killShellInput struct {
	ShellID string `json:"shell_id"`
}

func (t *KillShellTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	if t.Shell == nil {
		return nil, errors.New("KillShell: background shell unavailable in this context")
	}
	var in killShellInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("KillShell: parse input: %w", err)
	}
	if in.ShellID == "" {
		return nil, errors.New("KillShell: empty shell_id")
	}
	if err := t.Shell.Kill(in.ShellID); err != nil {
		if errors.Is(err, ErrUnknownBackgroundJob) {
			return []byte(fmt.Sprintf("no background job with id %q", in.ShellID)), nil
		}
		return nil, fmt.Errorf("KillShell: %w", err)
	}
	return []byte(fmt.Sprintf("requested termination of job %s", in.ShellID)), nil
}

// Compile-time checks.
var (
	_ Tool = (*BashOutputTool)(nil)
	_ Tool = (*KillShellTool)(nil)
)
