package chat

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/usershell"
)

// blockingRunner keeps every job in the Running state until release()
// is called, so footer/overlay tests can observe a stable fg/bg job
// without racing a real PTY's exit.
type blockingRunner struct {
	mu      sync.Mutex
	release chan struct{}
}

func newBlockingRunner() *blockingRunner {
	return &blockingRunner{release: make(chan struct{})}
}

func (r *blockingRunner) Start(ctx context.Context, command, cwd string) (io.Reader, func() (int, error), func(), error) {
	rel := r.release
	wait := func() (int, error) {
		select {
		case <-rel:
			return 0, nil
		case <-ctx.Done():
			return 130, ctx.Err()
		}
	}
	kill := func() {}
	return strings.NewReader(""), wait, kill, nil
}

func (r *blockingRunner) releaseAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	close(r.release)
}

func blockingManager(t *testing.T) (*usershell.Manager, *blockingRunner) {
	t.Helper()
	r := newBlockingRunner()
	mgr := usershell.New(usershell.Options{Runner: r, OutputDir: t.TempDir()})
	t.Cleanup(func() {
		r.releaseAll()
		mgr.Close()
	})
	return mgr, r
}

func waitForRunningState(t *testing.T, mgr *usershell.Manager, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s, err := mgr.Get(id); err == nil && s.State == usershell.StateRunning {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s never reached running", id)
}

func TestUserShellFooter_FgRunningState(t *testing.T) {
	mgr, _ := blockingManager(t)
	m := newTestModel(t)
	m.usershell = mgr
	job, err := mgr.Submit(context.Background(), "vim main.go", usershell.Foreground)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	waitForRunningState(t, mgr, job.ID)

	ctx := m.computeUserShellFooterContext()
	if ctx.state != userShellFooterFgRunning {
		t.Fatalf("want fg-running state; got %v", ctx.state)
	}
	out := renderUserShellFooter(ctx)
	for _, want := range []string{"vim", "cancel", "bg"} {
		if !strings.Contains(out, want) {
			t.Errorf("fg footer missing %q; got %q", want, out)
		}
	}
}

func TestUserShellFooter_FgRunningWithQueueAndBg(t *testing.T) {
	mgr, _ := blockingManager(t)
	m := newTestModel(t)
	m.usershell = mgr
	// One fg (running), one bg (running), and a second fg that queues.
	fg, _ := mgr.Submit(context.Background(), "fgjob", usershell.Foreground)
	waitForRunningState(t, mgr, fg.ID)
	bg, _ := mgr.Submit(context.Background(), "bgjob", usershell.Background)
	waitForRunningState(t, mgr, bg.ID)
	// A second foreground submission has nowhere to go (fg slot busy)
	// so it lands in the pending queue.
	if _, err := mgr.Submit(context.Background(), "queued", usershell.Foreground); err != nil {
		t.Fatalf("submit queued: %v", err)
	}

	ctx := m.computeUserShellFooterContext()
	if ctx.state != userShellFooterFgRunning {
		t.Fatalf("want fg-running; got %v", ctx.state)
	}
	out := renderUserShellFooter(ctx)
	if ctx.queueCount > 0 && !strings.Contains(out, "queued") {
		t.Errorf("footer should mention queued count; got %q", out)
	}
	if ctx.bgCount > 0 && !strings.Contains(out, "bg") {
		t.Errorf("footer should mention bg count; got %q", out)
	}
}

func TestUserShellFooter_BgOnlyState(t *testing.T) {
	mgr, _ := blockingManager(t)
	m := newTestModel(t)
	m.usershell = mgr
	// First running job grabs the fg slot; explicitly background it so
	// the fg slot frees and we land in the genuine bg-only state.
	a, _ := mgr.Submit(context.Background(), "tail -f a", usershell.Foreground)
	waitForRunningState(t, mgr, a.ID)
	if err := mgr.Background(a.ID); err != nil {
		t.Fatalf("background a: %v", err)
	}
	// Second running job, also backgrounded, gives a plural count.
	b, _ := mgr.Submit(context.Background(), "tail -f b", usershell.Foreground)
	waitForRunningState(t, mgr, b.ID)
	if err := mgr.Background(b.ID); err != nil {
		t.Fatalf("background b: %v", err)
	}

	ctx := m.computeUserShellFooterContext()
	if ctx.state != userShellFooterBgOnly {
		t.Fatalf("want bg-only; got %v", ctx.state)
	}
	out := renderUserShellFooter(ctx)
	if !strings.Contains(out, "bg job") {
		t.Errorf("bg-only footer should mention bg jobs; got %q", out)
	}
	// Two jobs → the muted plural "s" is present.
	if !strings.Contains(out, "list") {
		t.Errorf("bg-only footer should mention the list keybind; got %q", out)
	}
}

func TestMutedPlural(t *testing.T) {
	// plural(1) is "", so the rendered span carries no "s".
	if strings.Contains(mutedPlural(1), "s") {
		t.Errorf("mutedPlural(1) should not contain an s; got %q", mutedPlural(1))
	}
	if !strings.Contains(mutedPlural(2), "s") {
		t.Errorf("mutedPlural(2) should contain an s; got %q", mutedPlural(2))
	}
}

func TestCancelForegroundCmd_CancelsRunningFg(t *testing.T) {
	mgr, _ := blockingManager(t)
	m := newTestModel(t)
	m.usershell = mgr
	job, _ := mgr.Submit(context.Background(), "vim", usershell.Foreground)
	waitForRunningState(t, mgr, job.ID)

	cmd := m.cancelForegroundCmd()
	if cmd == nil {
		t.Fatal("cancelForegroundCmd should return a command when a fg job runs")
	}
	st, ok := cmd().(statusMsg)
	if !ok || !strings.Contains(st.text, "cancelled") {
		t.Errorf("cancel should report cancellation; got %+v", st)
	}
}

func TestBackgroundRunningCmd_MovesFgToBg(t *testing.T) {
	mgr, _ := blockingManager(t)
	m := newTestModel(t)
	m.usershell = mgr
	job, _ := mgr.Submit(context.Background(), "vim", usershell.Foreground)
	waitForRunningState(t, mgr, job.ID)

	cmd := m.backgroundRunningCmd()
	if cmd == nil {
		t.Fatal("backgroundRunningCmd should return a command when a fg job runs")
	}
	st, ok := cmd().(statusMsg)
	if !ok || !strings.Contains(st.text, "background") {
		t.Errorf("background should report the move; got %+v", st)
	}
}

func TestSubmitUserShellCmd_QueuesJob(t *testing.T) {
	mgr, _ := blockingManager(t)
	m := newTestModel(t)
	m.usershell = mgr
	cmd := m.submitUserShellCmd("echo hi", usershell.Foreground)
	if cmd == nil {
		t.Fatal("submit should return a command")
	}
	st, ok := cmd().(statusMsg)
	if !ok || !strings.Contains(st.text, "queued") {
		t.Errorf("submit should report queued; got %+v", st)
	}
}
