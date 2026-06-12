package main

import (
	"bytes"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Disabled state: empty env value must return nil, and every method
// must be safe on the nil receiver - that IS the zero-cost off path.
func TestNewBootTrace_DisabledIsNilAndNilSafe(t *testing.T) {
	tr := newBootTrace("", &bytes.Buffer{}, time.Now())
	if tr != nil {
		t.Fatalf("empty enabled value must yield nil trace, got %#v", tr)
	}
	// Must not panic, must not write anywhere.
	tr.Mark("config_loaded")
	tr.Finish("first_frame")
}

// Enabled state: marks accumulate, Finish prints one line containing
// every checkpoint in order with millisecond values.
func TestBootTrace_MarksAndFinishPrintOneLine(t *testing.T) {
	var out bytes.Buffer
	tr := newBootTrace("1", &out, time.Now().Add(-10*time.Millisecond))
	tr.Mark("config_loaded")
	tr.Mark("db_open")
	tr.Finish("first_frame")

	got := out.String()
	if n := strings.Count(got, "\n"); n != 1 {
		t.Fatalf("want exactly one line, got %d newlines in %q", n, got)
	}
	if !strings.HasPrefix(got, "carlos boot trace:") {
		t.Errorf("missing prefix: %q", got)
	}
	// Checkpoints in order, each with a fixed-point ms value.
	re := regexp.MustCompile(`carlos boot trace: config_loaded=(\d+\.\d)ms db_open=(\d+\.\d)ms first_frame=(\d+\.\d)ms`)
	m := re.FindStringSubmatch(got)
	if m == nil {
		t.Fatalf("line does not match expected shape: %q", got)
	}
	// The trace was anchored 10ms in the past, so every value must be
	// at least ~10ms - a sanity check that the values are cumulative
	// from the start anchor, not deltas between marks.
	for _, v := range m[1:] {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			t.Fatalf("unparsable ms value %q: %v", v, err)
		}
		if f < 9.9 {
			t.Errorf("value %sms smaller than the 10ms anchor offset - not cumulative?", v)
		}
	}
}

// Finish must be idempotent: only the first call prints, later Finish
// AND Mark calls are no-ops (the chat<->manage loop re-enters View).
func TestBootTrace_FinishIdempotentAndSealsMarks(t *testing.T) {
	var out bytes.Buffer
	tr := newBootTrace("yes", &out, time.Now())
	tr.Finish("first_frame")
	first := out.String()

	tr.Mark("late_mark")
	tr.Finish("second_finish")
	if out.String() != first {
		t.Errorf("post-Finish calls must not print; before=%q after=%q", first, out.String())
	}
	if strings.Contains(out.String(), "late_mark") || strings.Contains(out.String(), "second_finish") {
		t.Errorf("sealed trace recorded post-Finish checkpoints: %q", out.String())
	}
}

// Defensive fallbacks: nil writer -> io.Discard (no panic), zero start
// -> now (values stay small and non-negative).
func TestNewBootTrace_Fallbacks(t *testing.T) {
	tr := newBootTrace("1", nil, time.Time{})
	if tr == nil {
		t.Fatal("enabled trace must not be nil")
	}
	if tr.start.IsZero() {
		t.Error("zero start must fall back to time.Now()")
	}
	// Writes go to io.Discard without panicking.
	tr.Mark("a")
	tr.Finish("b")
	if len(tr.marks) != 2 {
		t.Fatalf("want 2 marks, got %d", len(tr.marks))
	}
	for _, m := range tr.marks {
		if m.elapsed < 0 {
			t.Errorf("negative elapsed for %s: %v", m.name, m.elapsed)
		}
	}
}

// bootTraceFromEnv honours CARLOS_BOOT_TRACE: unset/empty -> nil,
// non-empty -> a trace anchored at process start, writing to stderr.
func TestBootTraceFromEnv(t *testing.T) {
	t.Setenv(bootTraceEnv, "")
	if tr := bootTraceFromEnv(); tr != nil {
		t.Errorf("unset env must disable the trace, got %#v", tr)
	}

	t.Setenv(bootTraceEnv, "1")
	tr := bootTraceFromEnv()
	if tr == nil {
		t.Fatal("CARLOS_BOOT_TRACE=1 must enable the trace")
	}
	if tr.out != os.Stderr {
		t.Error("env-built trace must print to stderr")
	}
	if !tr.start.Equal(bootTraceProcessStart) {
		t.Errorf("env-built trace must anchor at process start; got %v want %v",
			tr.start, bootTraceProcessStart)
	}
}

func TestFormatBootDur(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "0.0ms"},
		{1500 * time.Microsecond, "1.5ms"},
		{52 * time.Millisecond, "52.0ms"},
		{1234567 * time.Microsecond, "1234.6ms"},
	}
	for _, c := range cases {
		if got := formatBootDur(c.d); got != c.want {
			t.Errorf("formatBootDur(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

// Mark/Finish under concurrent callers must not race (the first-render
// hook fires on bubbletea's goroutine while runDefault's goroutine owns
// the earlier marks). Run with -race in CI.
func TestBootTrace_ConcurrentMarkFinish(t *testing.T) {
	var out bytes.Buffer
	tr := newBootTrace("1", &out, time.Now())
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 50; i++ {
			tr.Mark("m")
		}
	}()
	tr.Finish("first_frame")
	<-done
	if n := strings.Count(out.String(), "\n"); n != 1 {
		t.Fatalf("want exactly one printed line, got %d", n)
	}
}
