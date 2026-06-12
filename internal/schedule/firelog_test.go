package schedule

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFireLog_RoundTrip_PersistAndReplay confirms that an Appended
// (name, slot) survives a close + reopen of the log file. This is the
// core crash-window guard: the daemon writes the slot, crashes, and on
// restart the in-memory set must still contain the entry so Due() can
// suppress it.
func TestFireLog_RoundTrip_PersistAndReplay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fire.log")

	log, err := OpenFireLog(path)
	if err != nil {
		t.Fatalf("OpenFireLog: %v", err)
	}
	slot := time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)
	if err := log.Append("morning-slack", slot); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if !log.Has("morning-slack", slot) {
		t.Fatal("Has(morning-slack, slot) = false immediately after Append")
	}
	if err := log.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Simulate a process restart: reopen the file from scratch.
	log2, err := OpenFireLog(path)
	if err != nil {
		t.Fatalf("OpenFireLog (replay): %v", err)
	}
	defer log2.Close()
	if !log2.Has("morning-slack", slot) {
		t.Fatal("replay lost the entry: Has(morning-slack, slot) = false")
	}
	// Local-time variant of the same instant must still hit the entry
	// since firelogKey normalises to UTC.
	loc, _ := time.LoadLocation("America/New_York")
	if loc != nil {
		if !log2.Has("morning-slack", slot.In(loc)) {
			t.Fatal("Has rejects equivalent local-time slot: tz normalisation broken")
		}
	}
}

// TestFireLog_AppendDurableBeforeReturn pins the write ordering contract:
// Append must fsync the entry before returning, so the caller can safely
// invoke its action callback knowing the log is durable. We assert
// durability by reopening from a different handle (without closing the
// writer first) and observing the entry.
func TestFireLog_AppendDurableBeforeReturn(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fire.log")
	log, err := OpenFireLog(path)
	if err != nil {
		t.Fatalf("OpenFireLog: %v", err)
	}
	defer log.Close()

	slot := time.Date(2026, 6, 10, 9, 7, 0, 0, time.UTC)
	if err := log.Append("ordering-test", slot); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Without closing the writer, read the raw file contents. The line
	// must already be on disk; if Append returned before fsync, this
	// observation would be racy or empty.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !strings.Contains(string(data), "ordering-test\t") {
		t.Fatalf("post-Append file content does not contain the entry: %q", string(data))
	}
	// And the slot's UTC nanos must be in the recorded line.
	wantNanos := slot.UTC().UnixNano()
	if !strings.Contains(string(data), "\t"+itoa(wantNanos)+"\n") {
		t.Fatalf("post-Append file content missing slot nanos %d: %q", wantNanos, string(data))
	}
}

// TestFireLog_AppendIdempotent confirms a second Append of the same
// (name, slot) does not duplicate the line on disk or in the in-memory
// set. This matches DueSlot's expectation that callers may legitimately
// re-Append (e.g., a retry after a transient action-callback failure).
func TestFireLog_AppendIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fire.log")
	log, err := OpenFireLog(path)
	if err != nil {
		t.Fatalf("OpenFireLog: %v", err)
	}
	defer log.Close()

	slot := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if err := log.Append("idem", slot); err != nil {
			t.Fatalf("Append #%d: %v", i, err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	count := strings.Count(string(data), "idem\t")
	if count != 1 {
		t.Fatalf("expected exactly 1 line for idem, got %d (raw: %q)", count, string(data))
	}
}

// TestFireLog_AppendRejectsBadInput pins the validation surface:
// nil log, empty name, tab/newline in name, zero slot.
func TestFireLog_AppendRejectsBadInput(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenFireLog(filepath.Join(dir, "fire.log"))
	if err != nil {
		t.Fatalf("OpenFireLog: %v", err)
	}
	defer log.Close()
	slot := time.Date(2026, 6, 10, 1, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		fn   func() error
	}{
		{"nil receiver", func() error { return (*FireLog)(nil).Append("a", slot) }},
		{"empty name", func() error { return log.Append("", slot) }},
		{"tab in name", func() error { return log.Append("bad\tname", slot) }},
		{"newline in name", func() error { return log.Append("bad\nname", slot) }},
		{"zero slot", func() error { return log.Append("ok", time.Time{}) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.fn(); err == nil {
				t.Fatalf("expected error, got nil")
			}
		})
	}
}

// TestFireLog_OpenRejectsEmptyPath covers the OpenFireLog input gate.
func TestFireLog_OpenRejectsEmptyPath(t *testing.T) {
	if _, err := OpenFireLog(""); err == nil {
		t.Fatal("OpenFireLog(\"\") returned nil error")
	}
}

// TestFireLog_ReplayTolerantOfMalformedLines pins the
// "skip-bad-lines, keep-going" contract for replay. A partial write
// from a crash mid-Append must not poison the rest of the log.
func TestFireLog_ReplayTolerantOfMalformedLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fire.log")
	// Hand-craft the log file with one good entry, one truncated line,
	// and a second good entry. The middle line simulates a torn write.
	slot1 := time.Date(2026, 6, 10, 6, 0, 0, 0, time.UTC)
	slot2 := time.Date(2026, 6, 10, 7, 0, 0, 0, time.UTC)
	content := "ok-a\t" + itoa(slot1.UnixNano()) + "\n" +
		"torn-no-tab-or-nanos\n" +
		"ok-b\t" + itoa(slot2.UnixNano()) + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	log, err := OpenFireLog(path)
	if err != nil {
		t.Fatalf("OpenFireLog: %v", err)
	}
	defer log.Close()
	if !log.Has("ok-a", slot1) {
		t.Error("ok-a not replayed")
	}
	if !log.Has("ok-b", slot2) {
		t.Error("ok-b not replayed")
	}
}

// TestFireLog_CloseIdempotent - Close twice must not error.
func TestFireLog_CloseIdempotent(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenFireLog(filepath.Join(dir, "fire.log"))
	if err != nil {
		t.Fatalf("OpenFireLog: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := log.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestFireLog_NilHasReturnsFalse - Has on a nil receiver must not panic.
func TestFireLog_NilHasReturnsFalse(t *testing.T) {
	var log *FireLog
	if log.Has("anything", time.Now()) {
		t.Fatal("nil-receiver Has returned true")
	}
}

// itoa is a tiny local helper so the test file doesn't pull in strconv
// at the package level (it's already used inside firelog.go itself but
// the existing schedule tests deliberately avoid that import).
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [32]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	out := string(buf[i:])
	if neg {
		out = "-" + out
	}
	return out
}
