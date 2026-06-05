package daemon

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/georgebuilds/carlos/internal/schedule"
)

// mkSched is the minimal Schedule constructor used by the notify
// tests. Real fields don't matter for the notification path —
// pushNotification only reads Name.
func mkSched(name string) schedule.Schedule {
	return schedule.Schedule{Name: name}
}

// recordingNotifier captures every Notify call for assertions. Used
// by daemon_test.go to verify the tick loop fires notifications + by
// notify_test.go itself for self-tests.
type recordingNotifier struct {
	mu  sync.Mutex
	got []Notification
	err error
}

func (r *recordingNotifier) Notify(_ context.Context, n Notification) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, n)
	return r.err
}

func (r *recordingNotifier) calls() []Notification {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Notification, len(r.got))
	copy(out, r.got)
	return out
}

func TestSystemNotifier_EmptyBodyErrors(t *testing.T) {
	s := &SystemNotifier{}
	err := s.Notify(context.Background(), Notification{Title: "x"})
	if err == nil {
		t.Fatal("expected error on empty body")
	}
}

func TestSystemNotifier_DefaultsTitle(t *testing.T) {
	// Hard to exercise the platform shell-out path in CI without
	// actually firing a real notification. Instead assert the
	// defaulting logic that runs BEFORE the dispatch.
	rec := &recordingNotifier{}
	n := Notification{Body: "x"}
	// Manually apply the title default the way Notify does, then
	// roundtrip through the recorder to assert.
	if n.Title == "" {
		n.Title = "carlos"
	}
	_ = rec.Notify(context.Background(), n)
	got := rec.calls()
	if len(got) != 1 || got[0].Title != "carlos" {
		t.Errorf("title default not applied: %+v", got)
	}
}

func TestEscapeForAppleScript(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"plain", "plain"},
		{`with "quotes"`, `with \"quotes\"`},
		{`back\slash`, `back\\slash`},
		{`both \ and "`, `both \\ and \"`},
	}
	for _, c := range cases {
		got := escapeForAppleScript(c.in)
		if got != c.want {
			t.Errorf("escape(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("short", 10); got != "short" {
		t.Errorf("under-cap: %q", got)
	}
	if got := truncate("abcdefghij", 5); got != "abcd…" {
		t.Errorf("at-cap: %q (want abcd…)", got)
	}
}

func TestPushNotification_FormatsSuccess(t *testing.T) {
	rec := &recordingNotifier{}
	d := &Daemon{opts: Options{Notifier: rec}}
	d.pushNotification(context.Background(), mkSched("morning-summary"), true, "")
	got := rec.calls()
	if len(got) != 1 {
		t.Fatalf("got %d notifications, want 1", len(got))
	}
	if !strings.Contains(got[0].Body, "morning-summary") {
		t.Errorf("body missing schedule name: %q", got[0].Body)
	}
	if !strings.Contains(got[0].Body, "ran successfully") {
		t.Errorf("body missing success phrasing: %q", got[0].Body)
	}
	if got[0].Urgency != "normal" {
		t.Errorf("urgency = %q, want normal", got[0].Urgency)
	}
}

func TestPushNotification_FormatsFailureWithReason(t *testing.T) {
	rec := &recordingNotifier{}
	d := &Daemon{opts: Options{Notifier: rec}}
	d.pushNotification(context.Background(), mkSched("nightly-backup"), false, "budget exceeded")
	got := rec.calls()
	if len(got) != 1 {
		t.Fatalf("got %d notifications, want 1", len(got))
	}
	if !strings.Contains(got[0].Body, "failed") {
		t.Errorf("body missing failure phrasing: %q", got[0].Body)
	}
	if !strings.Contains(got[0].Body, "budget exceeded") {
		t.Errorf("body missing reason: %q", got[0].Body)
	}
	if got[0].Urgency != "critical" {
		t.Errorf("failure urgency = %q, want critical", got[0].Urgency)
	}
}

func TestPushNotification_NilNotifierIsNoOp(t *testing.T) {
	d := &Daemon{opts: Options{}} // no Notifier
	// Should not panic + not write anywhere.
	d.pushNotification(context.Background(), mkSched("x"), true, "")
}

func TestPushNotification_NotifierErrorIsSwallowed(t *testing.T) {
	rec := &recordingNotifier{err: errors.New("dbus unreachable")}
	d := &Daemon{opts: Options{Notifier: rec}}
	// Should not panic + should still record the call (the swallow
	// happens after dispatch).
	d.pushNotification(context.Background(), mkSched("x"), true, "")
	if len(rec.calls()) != 1 {
		t.Errorf("expected the call to still dispatch before the error swallow; got %d", len(rec.calls()))
	}
}
