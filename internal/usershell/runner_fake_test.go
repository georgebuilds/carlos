package usershell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeRunner is a deterministic in-process runner used by the
// Manager's queue + lifecycle tests. It avoids spawning a real shell
// + PTY (slow, environment-dependent) while still exercising the
// full runJob goroutine + state-transition path.
//
// Behavior:
//   - Returns `output` as the reader's payload, then EOF
//   - Wait returns `exit` and `failErr`
//   - Kill cancels the per-job ctx so Wait returns "cancelled"
//   - block=true: Wait blocks until release() is called (lets tests
//     hold a job in StateRunning while they verify queue state)
type fakeRunner struct {
	mu        sync.Mutex
	output    string
	exit      int
	failErr   error
	startErr  error
	block     bool
	released  chan struct{}
	startsRun int // bumped on every Start call
	startedAt []time.Time
	commands  []string
}

func (f *fakeRunner) Start(ctx context.Context, command, cwd string) (io.Reader, func() (int, error), func(), error) {
	f.mu.Lock()
	if f.startErr != nil {
		err := f.startErr
		f.mu.Unlock()
		return nil, nil, nil, err
	}
	f.startsRun++
	f.startedAt = append(f.startedAt, time.Now())
	f.commands = append(f.commands, command)
	out := f.output
	exit := f.exit
	failErr := f.failErr
	block := f.block
	released := f.released
	f.mu.Unlock()

	reader := strings.NewReader(out)
	killed := make(chan struct{})
	killOnce := sync.Once{}
	doKill := func() {
		killOnce.Do(func() { close(killed) })
	}
	go func() {
		<-ctx.Done()
		doKill()
	}()
	wait := func() (int, error) {
		if block && released != nil {
			select {
			case <-released:
			case <-killed:
				return -1, errors.New("killed")
			}
		} else {
			// Yield so the reader goroutine sees the output before
			// we return EOF on wait.
			time.Sleep(2 * time.Millisecond)
		}
		select {
		case <-killed:
			return -1, errors.New("killed")
		default:
		}
		if failErr != nil {
			return -1, failErr
		}
		return exit, nil
	}
	return reader, wait, doKill, nil
}

// release unblocks the wait() call when block=true. Safe to call
// even if release was never armed.
func (f *fakeRunner) release() {
	f.mu.Lock()
	if f.released != nil {
		select {
		case <-f.released:
		default:
			close(f.released)
		}
	}
	f.mu.Unlock()
}

// newBlockingRunner returns a runner whose Wait blocks until
// release() is called. Use to drive queue tests where you want to
// keep a job in StateRunning while you submit more.
func newBlockingRunner(output string, exit int) *fakeRunner {
	return &fakeRunner{
		output:   output,
		exit:     exit,
		block:    true,
		released: make(chan struct{}),
	}
}

// waitForState polls m.Get(id) until the state matches or timeout
// elapses. Returns nil on success, descriptive error otherwise.
// Avoids hardcoded sleeps in tests against the goroutine-driven
// Manager.
func waitForState(t *testing.T, m *Manager, id string, want State, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last State
	for time.Now().Before(deadline) {
		snap, err := m.Get(id)
		if err == nil {
			last = snap.State
			if last == want {
				return nil
			}
		}
		time.Sleep(time.Millisecond)
	}
	return fmt.Errorf("job %s: want state %v, still at %v after %s", id, want, last, timeout)
}
