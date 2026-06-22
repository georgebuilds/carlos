package usershell

import (
	"context"
	"errors"
	"sync"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/tools"
)

// BackgroundDispatcher adapts a Manager to tools.BackgroundShell: the seam
// the agent's bash tool uses to launch detached jobs and that BashOutput /
// KillShell read and stop. It also records WHICH agent dispatched each job
// (read from the spawn-parent on the dispatch ctx) so the wake-on-completion
// path can route the wake to that exact agent rather than a fixed default -
// the wake then follows a /agents switch, and a user `!cmd` (which never
// flows through here) stays passive.
//
// usershell may import tools + agent because the dependency arrow is one-way:
// tools imports neither, so usershell -> tools closes no cycle, and
// usershell -> agent already exists.
type BackgroundDispatcher struct {
	mgr *Manager

	mu     sync.Mutex
	owners map[string]string // job id -> dispatching agent id
}

// NewBackgroundDispatcher wraps mgr. The returned value implements
// tools.BackgroundShell and is safe for concurrent use.
func NewBackgroundDispatcher(mgr *Manager) *BackgroundDispatcher {
	return &BackgroundDispatcher{mgr: mgr, owners: map[string]string{}}
}

// StartBackground submits command as a background job and records the
// dispatching agent (from the ctx spawn-parent) so OwnerOf can route the
// wake back to it. The agent id may be "" when the caller set no spawn-parent
// (a non-chat dispatch); the job is still tracked as agent-owned.
func (d *BackgroundDispatcher) StartBackground(ctx context.Context, command, cwd string) (string, error) {
	job, err := d.mgr.SubmitIn(ctx, command, cwd, Background)
	if err != nil {
		return "", err
	}
	d.mu.Lock()
	d.owners[job.ID] = agent.SpawnParentFromContext(ctx)
	d.mu.Unlock()
	return job.ID, nil
}

// OwnerOf returns the id of the agent that dispatched job id, plus whether
// the job was agent-dispatched at all. A returned ("", true) means the job
// was started by the agent but with no spawn-parent on the ctx, so there's
// no agent to wake.
func (d *BackgroundDispatcher) OwnerOf(id string) (agentID string, ok bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	owner, ok := d.owners[id]
	return owner, ok
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
// StartBackground (rather than a user `!cmd`).
func (d *BackgroundDispatcher) IsAgentJob(id string) bool {
	_, ok := d.OwnerOf(id)
	return ok
}

// Compile-time check: the dispatcher satisfies the tools seam.
var _ tools.BackgroundShell = (*BackgroundDispatcher)(nil)
