package tools

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

type fakeRefresher struct{ err error }

func (f fakeRefresher) MaybeRefresh() error { return f.err }

// A failed vault refresh is logged rather than silently swallowed (it was
// previously discarded with `_ = rerr`), while the tool call still proceeds
// on the last-good index.
func TestRefreshOrLog_LogsOnError(t *testing.T) {
	var msg string
	orig := notesLogf
	notesLogf = func(format string, args ...any) { msg = fmt.Sprintf(format, args...) }
	defer func() { notesLogf = orig }()

	refreshOrLog(fakeRefresher{err: errors.New("disk gone")})
	if !strings.Contains(msg, "disk gone") {
		t.Errorf("refresh error not logged: %q", msg)
	}
}

// A successful refresh logs nothing.
func TestRefreshOrLog_SilentOnSuccess(t *testing.T) {
	logged := false
	orig := notesLogf
	notesLogf = func(string, ...any) { logged = true }
	defer func() { notesLogf = orig }()

	refreshOrLog(fakeRefresher{err: nil})
	if logged {
		t.Error("refreshOrLog logged on a successful refresh")
	}
}
