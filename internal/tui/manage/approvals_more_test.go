package manage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// TestErrString_Error confirms the local errString type carries the
// constructor string through Error().
func TestErrString_Error(t *testing.T) {
	var e error = errString("not wired")
	if got := e.Error(); got != "not wired" {
		t.Errorf("errString.Error() = %q", got)
	}
}

// TestFmtErr_WrapsString covers the fmtErr helper used when the lister
// is nil.
func TestFmtErr_WrapsString(t *testing.T) {
	err := fmtErr("nope")
	if err == nil || err.Error() != "nope" {
		t.Errorf("fmtErr(nope) = %v", err)
	}
}

// TestHumanAge_Buckets exercises every branch of the relative-time
// formatter.
func TestHumanAge_Buckets(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Millisecond, "0s"},
		{5 * time.Second, "5s"},
		{2 * time.Minute, "2m"},
		{3 * time.Hour, "3h"},
		{48 * time.Hour, "2d"},
	}
	for _, c := range cases {
		if got := humanAge(c.d); got != c.want {
			t.Errorf("humanAge(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// TestApprovalsPane_SelectedEmpty confirms selected() reports false
// when the pane has nothing pending.
func TestApprovalsPane_SelectedEmpty(t *testing.T) {
	p := approvalsPane{}
	if _, ok := p.selected(); ok {
		t.Errorf("selected() on empty pane returned ok")
	}

	// Cursor out of bounds with non-empty pending returns false too.
	p.pending = []agent.PendingApproval{mkPending("a", "x", "k", "ag", time.Second)}
	p.cursor = 5
	if _, ok := p.selected(); ok {
		t.Errorf("selected() with cursor out of bounds returned ok")
	}
}

// TestFetchApprovalsCmd_NoListerSurfacesError confirms the nil-lister
// branch produces a fetchApprovalsMsg with an error rather than
// crashing.
func TestFetchApprovalsCmd_NoListerSurfacesError(t *testing.T) {
	cmd := fetchApprovalsCmd(nil)
	msg, ok := cmd().(fetchApprovalsMsg)
	if !ok {
		t.Fatalf("nil-lister cmd = %T", cmd())
	}
	if msg.err == nil || !strings.Contains(msg.err.Error(), "no approval log") {
		t.Errorf("nil-lister err = %v", msg.err)
	}
}

// TestFetchApprovalsCmd_DelegatesToLister confirms the wired-lister
// branch invokes the lister + threads the result back.
func TestFetchApprovalsCmd_DelegatesToLister(t *testing.T) {
	want := []agent.PendingApproval{mkPending("x", "y", "k", "ag", time.Second)}
	lister := func(ctx context.Context) ([]agent.PendingApproval, error) {
		return want, nil
	}
	cmd := fetchApprovalsCmd(lister)
	msg := cmd().(fetchApprovalsMsg)
	if msg.err != nil {
		t.Errorf("err = %v", msg.err)
	}
	if len(msg.pending) != 1 || msg.pending[0].Ref.ID != "x" {
		t.Errorf("pending = %+v", msg.pending)
	}
}

// TestFetchApprovalsCmd_ListerErrorPropagates confirms an error from
// the lister flows into fetchApprovalsMsg.err.
func TestFetchApprovalsCmd_ListerErrorPropagates(t *testing.T) {
	lister := func(ctx context.Context) ([]agent.PendingApproval, error) {
		return nil, errors.New("read failed")
	}
	cmd := fetchApprovalsCmd(lister)
	msg := cmd().(fetchApprovalsMsg)
	if msg.err == nil || msg.err.Error() != "read failed" {
		t.Errorf("err = %v, want 'read failed'", msg.err)
	}
}

// TestApprovalsPane_RenderTruncatesWhenHeightTight feeds a long list
// into a short height to exercise the "…N more" trailer branch.
func TestApprovalsPane_RenderTruncatesWhenHeightTight(t *testing.T) {
	p := approvalsPane{fetched: time.Now()}
	for i := 0; i < 20; i++ {
		p.pending = append(p.pending, mkPending(
			"id-"+itoa(i), "title-"+itoa(i), "plan", "ag", time.Second,
		))
	}
	out := p.render(120, 6)
	if !strings.Contains(out, "more") {
		t.Errorf("tight-height render didn't show 'more' tail:\n%s", out)
	}
}
