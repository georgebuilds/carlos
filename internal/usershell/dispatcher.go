package usershell

import (
	"context"
	"errors"
	"sync"

	"github.com/georgebuilds/carlos/internal/tools"
)

// BackgroundDispatcher adapts a Manager to tools.BackgroundShell: the seam
// the agent's bash tool uses to launch detached jobs and that BashOutput /
// KillShell read and stop. It also records which job ids the AGENT started
// (via run_in_background) as opposed to the user's `!cmd`, so the
// wake-on-completion path can react only to agent-owned jobs.
//
// usershell may import tools because the dependency arrow is one-way: tools
// imports neither usershell nor agent, so usershell -> tools closes no cycle
// (usershell -> agent -> tools already exists).
type BackgroundDispatcher struct {
	mgr *Manager

	mu    sync.Mutex
	owned map[string]struct{}
}

// NewBackgroundDispatcher wraps mgr. The returned value implements
// tools.BackgroundShell and is safe for concurrent use.
func NewBackgroundDispatcher(mgr *Manager) *BackgroundDispatcher {
	return &BackgroundDispatcher{mgr: mgr, owned: map[string]struct{}{}}
}

// StartBackground submits command as a background job and records its id as
// agent-owned so IsAgentJob reports true for it later.
func (d *BackgroundDispatcher) StartBackground(ctx context.Context, command, cwd string) (string, error) {
	job, err := d.mgr.SubmitIn(ctx, command, cwd, Background)
	if err != nil {
		return "", err
	}
	d.mu.Lock()
	d.owned[job.ID] = struct{}{}
	d.mu.Unlock()
	return job.ID, nil
}

// Output returns the job's captured output plus status, mapping an unknown
// id to tools.ErrUnknownBackgroundJob so the tool can render a clean note.
func (d *BackgroundDispatcher) Output(id string) (out []byte, state string, exitCode int, done bool, err error) {
	snap, gerr := d.mgr.Get(id)
	if gerr != nil {
		if errors.Is(gerr, ErrUnknownJob) {
			return nil, "", 0, false, tools.ErrUnknownBackgroundJob
		}
		return nil, "", 0, false, gerr
	}
	return d.mgr.Output(id), snap.State.String(), snap.ExitCode, snap.State.IsTerminal(), nil
}

// Kill terminates the job. Unknown id maps to tools.ErrUnknownBackgroundJob;
// killing an already-terminal job is a no-op (Manager.Cancel is idempotent).
func (d *BackgroundDispatcher) Kill(id string) error {
	if err := d.mgr.Cancel(id); err != nil {
		if errors.Is(err, ErrUnknownJob) {
			return tools.ErrUnknownBackgroundJob
		}
		return err
	}
	return nil
}

// IsAgentJob reports whether id was started by the agent via
// StartBackground (rather than a user `!cmd`). The wake-on-completion path
// uses this to react only to jobs the model itself dispatched.
func (d *BackgroundDispatcher) IsAgentJob(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.owned[id]
	return ok
}

// Compile-time check: the dispatcher satisfies the tools seam.
var _ tools.BackgroundShell = (*BackgroundDispatcher)(nil)
