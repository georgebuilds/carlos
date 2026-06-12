package schedule

import (
	"strings"
	"testing"
	"time"
)

// TestParseNatural_KnownForms exercises every documented cron-emitting
// natural-language form and asserts the resulting cron spec matches the
// form's promise. Each row's "wantSpec" doubles as documentation for
// what the user should expect. Interval-kind forms ("every N minutes"
// / "every N hours") are covered by TestParseNatural_IntervalForms
// since they no longer emit a 5-field cron string.
func TestParseNatural_KnownForms(t *testing.T) {
	cases := []struct {
		in       string
		wantSpec string
		wantOnce bool
	}{
		{"every weekday at 9am", "0 9 * * 1-5", false},
		{"every weekday morning", "0 9 * * 1-5", false},
		{"every weekend morning", "0 9 * * 0,6", false},
		{"every monday at noon", "0 12 * * 1", false},
		{"every Friday at 5pm", "0 17 * * 5", false},
		{"every sunday at midnight", "0 0 * * 0", false},
		{"every hour", "0 * * * *", false},
		{"hourly", "0 * * * *", false},
		{"daily at 7am", "0 7 * * *", false},
		{"daily at 7:30am", "30 7 * * *", false},
		{"daily at 17:00", "0 17 * * *", false},
		{"every day at 6pm", "0 18 * * *", false},
		{"DAILY AT 9AM", "0 9 * * *", false}, // case-insensitive
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := ParseNatural(c.in)
			if err != nil {
				t.Fatalf("ParseNatural(%q): %v", c.in, err)
			}
			if got.Spec != c.wantSpec {
				t.Fatalf("Spec mismatch: got %q want %q", got.Spec, c.wantSpec)
			}
			if got.Once != c.wantOnce {
				t.Fatalf("Once mismatch: got %v want %v", got.Once, c.wantOnce)
			}
			if got.Kind.Effective() != KindCron {
				t.Fatalf("expected KindCron, got %q", got.Kind)
			}
			// Round-trip: the resulting Spec must be parseable.
			if _, err := ParseCron(got.Spec); err != nil {
				t.Fatalf("emitted Spec %q is not valid cron: %v", got.Spec, err)
			}
		})
	}
}

// TestParseNatural_Tomorrow uses tryTomorrow's clock-injectable form so
// the test is deterministic across runs.
func TestParseNatural_Tomorrow(t *testing.T) {
	// Anchor: 2026-06-05 (Friday). Tomorrow is 2026-06-06 (Saturday).
	now := time.Date(2026, 6, 5, 10, 0, 0, 0, time.Local)
	sch, ok, err := tryTomorrow("tomorrow at 3pm", now)
	if err != nil || !ok {
		t.Fatalf("tryTomorrow: ok=%v err=%v", ok, err)
	}
	if !sch.Once {
		t.Fatal("expected Once=true for 'tomorrow at 3pm'")
	}
	want := "0 15 6 6 *"
	if sch.Spec != want {
		t.Fatalf("Spec: got %q want %q", sch.Spec, want)
	}
}

// TestParseNatural_FallbackCron checks that a raw cron expression
// passes through.
func TestParseNatural_FallbackCron(t *testing.T) {
	sch, err := ParseNatural("0 9 * * 1-5")
	if err != nil {
		t.Fatalf("fallback cron: %v", err)
	}
	if sch.Spec != "0 9 * * 1-5" {
		t.Fatalf("Spec: %q", sch.Spec)
	}
}

// TestParseNatural_Invalid asserts a handful of inputs that should
// surface a clear error rather than silently misparse. "every 100
// minutes" is no longer in this list: with the KindInterval primitive
// the cron-minute-range limit no longer applies, so the form is
// accepted (and tested in TestParseNatural_IntervalForms).
func TestParseNatural_Invalid(t *testing.T) {
	bad := []string{
		"",
		"  ",
		"every monday at 25:00", // invalid hour
		"every monday at 9zz",   // not a time
		"every blursday at 9am", // not a day
		"random garbage",        // doesn't match any form, doesn't parse as cron
		"0 9 * *",               // wrong field count for fallback
		"every 0 minutes",       // zero interval still rejected
	}
	for _, in := range bad {
		t.Run(in, func(t *testing.T) {
			_, err := ParseNatural(in)
			if err == nil {
				t.Fatalf("expected error for %q", in)
			}
		})
	}
}

// TestParseTime_AMPMBoundary pins the fix for the bug where parseTime
// would match any string ending in "am"/"pm" — including words like
// "team" or "spam" — and then surface a misleading "hour: …" atoi
// error. The boundary is now a digit or whitespace immediately before
// the suffix.
func TestParseTime_AMPMBoundary(t *testing.T) {
	// These strings end in "am"/"pm" but are NOT times. The previous
	// implementation routed them through the 12-hour parser and
	// surfaced an atoi error mentioning "hour"; the fix rejects them
	// the same way any other non-time would be rejected.
	bad := []string{
		"team",
		"spam",
		"am",
		"pm",
		"eveningpm",
	}
	for _, in := range bad {
		t.Run("bad/"+in, func(t *testing.T) {
			if _, _, err := parseTime(in); err == nil {
				t.Fatalf("parseTime(%q): expected error, got nil", in)
			}
		})
	}

	// Confirm legitimate 12-hour inputs still parse correctly so we
	// haven't tightened past the documented surface.
	good := []struct {
		in    string
		wantH int
		wantM int
	}{
		{"9am", 9, 0},
		{"12pm", 12, 0},
		{"12am", 0, 0},
		{"9:30am", 9, 30},
		{"12:00am", 0, 0},
		{"11:59pm", 23, 59},
		{"9 am", 9, 0}, // whitespace-tolerant: TrimSpace inside parseTime
	}
	for _, c := range good {
		t.Run("good/"+c.in, func(t *testing.T) {
			h, m, err := parseTime(c.in)
			if err != nil {
				t.Fatalf("parseTime(%q): unexpected error %v", c.in, err)
			}
			if h != c.wantH || m != c.wantM {
				t.Fatalf("parseTime(%q): got (%d, %d) want (%d, %d)", c.in, h, m, c.wantH, c.wantM)
			}
		})
	}
}

// TestParseCron_AllFieldForms covers the cron grammar systematically.
func TestParseCron_AllFieldForms(t *testing.T) {
	cases := []struct {
		spec  string
		valid bool
	}{
		{"* * * * *", true},
		{"0 0 * * *", true},
		{"*/15 * * * *", true},
		{"0 9-17 * * *", true},
		{"0 9-17/2 * * 1-5", true},
		{"0 9 1,15 * *", true},
		{"0 12 * * MON,WED,FRI", true},
		{"0 0 1 JAN *", true},
		{"0 0 * * sun", true},
		{"60 0 * * *", false},  // minute out of range
		{"* 24 * * *", false},  // hour out of range
		{"* * 32 * *", false},  // dom out of range
		{"* * * 13 *", false},  // month out of range
		{"* * * * 7", false},   // dow out of range
		{"5-1 * * * *", false}, // range backwards
		{"*/0 * * * *", false}, // step zero
		{"a * * * *", false},   // non-numeric
		{"* * * *", false},     // too few fields
		{"* * * * * *", false}, // too many
	}
	for _, c := range cases {
		t.Run(c.spec, func(t *testing.T) {
			_, err := ParseCron(c.spec)
			if c.valid && err != nil {
				t.Fatalf("ParseCron(%q): unexpected error %v", c.spec, err)
			}
			if !c.valid && err == nil {
				t.Fatalf("ParseCron(%q): expected error", c.spec)
			}
		})
	}
}

// TestCronExpr_Next_Boundary covers the boundary cases the brief calls
// out (DST, end-of-month, leap year).
func TestCronExpr_Next_Boundary(t *testing.T) {
	// 1) Daily at 9am — Next from 8am same day should be 9am same day.
	c, err := ParseCron("0 9 * * *")
	if err != nil {
		t.Fatal(err)
	}
	loc := time.Local
	start := time.Date(2026, 6, 5, 8, 0, 0, 0, loc)
	got := c.Next(start)
	want := time.Date(2026, 6, 5, 9, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("daily 9am: got %v want %v", got, want)
	}

	// 2) End-of-month: "0 0 31 * *" from Feb 1 → Mar 31 (Feb has no 31st).
	c, err = ParseCron("0 0 31 * *")
	if err != nil {
		t.Fatal(err)
	}
	got = c.Next(time.Date(2026, 2, 1, 0, 0, 0, 0, loc))
	want = time.Date(2026, 3, 31, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("31st: got %v want %v", got, want)
	}

	// 3) Leap year: Feb 29 only fires in leap years. 2024 is leap;
	//    2025 is not. From Mar 1 2024 the next fire is Feb 29 2028.
	c, err = ParseCron("0 0 29 2 *")
	if err != nil {
		t.Fatal(err)
	}
	got = c.Next(time.Date(2024, 3, 1, 0, 0, 0, 0, loc))
	want = time.Date(2028, 2, 29, 0, 0, 0, 0, loc)
	if !got.Equal(want) {
		t.Fatalf("leap year: got %v want %v", got, want)
	}

	// 4) DST: in US/Eastern, March 9 2025 spring-forward skips 02:00.
	//    A cron "30 2 * * *" should still fire — but on March 9 the
	//    02:30 wall time does not exist (jumps to 03:30). The iterator
	//    walks minute-by-minute; it will pass over the gap and resume
	//    on March 10. We assert that we don't infinite-loop and that
	//    the next match on/after the gap is March 10 02:30.
	eastern, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("America/New_York unavailable on this system")
	}
	c, _ = ParseCron("30 2 * * *")
	// Iterate in eastern wall time. After Mar 9 01:00 local, the next
	// 02:30 wall time is Mar 10 (Mar 9 02:30 is the DST gap).
	startE := time.Date(2025, 3, 9, 1, 0, 0, 0, eastern)
	// Override Next's "local" anchor by constructing the input in
	// eastern: Next() truncates to local time so we test what it
	// would do if the user's machine were in eastern.
	prevLoc := time.Local
	time.Local = eastern
	defer func() { time.Local = prevLoc }()
	got = c.Next(startE)
	if got.IsZero() {
		t.Fatal("dst: Next returned zero, suggests infinite loop guard tripped")
	}
	if got.Hour() != 2 || got.Minute() != 30 {
		t.Fatalf("dst: expected wall time 02:30, got %v", got)
	}
}

// TestSchedule_Due_FirstFire seeds an unfired schedule and asserts Due
// fires exactly at the cron time.
func TestSchedule_Due_FirstFire(t *testing.T) {
	s := Schedule{
		Name:   "test",
		Spec:   "0 9 * * *",
		Prompt: "x",
	}
	// At exactly 9:00 local, Due should be true.
	now := time.Date(2026, 6, 5, 9, 0, 0, 0, time.Local)
	if !s.Due(now) {
		t.Fatal("expected Due=true at 9:00")
	}
	// At 8:59 it should not yet be due.
	earlier := time.Date(2026, 6, 5, 8, 59, 0, 0, time.Local)
	if s.Due(earlier) {
		t.Fatal("expected Due=false at 8:59")
	}
}

// TestSchedule_Due_NoDoubleFire — after LastRunAt is updated, Due is
// false until the next scheduled instant.
func TestSchedule_Due_NoDoubleFire(t *testing.T) {
	s := Schedule{
		Name:      "test",
		Spec:      "0 9 * * *",
		Prompt:    "x",
		LastRunAt: time.Date(2026, 6, 5, 9, 0, 0, 0, time.Local),
	}
	// 9:30 same day — already ran at 9:00, next is tomorrow.
	now := time.Date(2026, 6, 5, 9, 30, 0, 0, time.Local)
	if s.Due(now) {
		t.Fatal("expected Due=false 30min after the last fire")
	}
}

// TestSchedule_Validate covers the three failure modes.
func TestSchedule_Validate(t *testing.T) {
	cases := []struct {
		s    Schedule
		want string
	}{
		{Schedule{Name: "", Spec: "* * * * *", Prompt: "x"}, "empty name"},
		{Schedule{Name: "n", Spec: "* * * * *", Prompt: ""}, "empty prompt"},
		{Schedule{Name: "n", Spec: "garbage", Prompt: "x"}, "5 fields"},
	}
	for _, c := range cases {
		err := c.s.Validate(nil)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Fatalf("Validate(%+v): want error containing %q, got %v", c.s, c.want, err)
		}
	}
}

// TestSchedule_Validate_FrameMembership covers the Phase F-14 hardening:
// the frame catalog gate. Empty known map skips the check (back-compat
// for callers that legitimately have no catalog, e.g. tests). Non-empty
// known map: empty Frame stays valid, in-set Frame valid, out-of-set
// Frame rejected. The error spelling pins the format so the caller can
// surface it verbatim.
func TestSchedule_Validate_FrameMembership(t *testing.T) {
	base := Schedule{Name: "n", Spec: "* * * * *", Prompt: "x"}

	// Empty known: any Frame accepted (back-compat).
	for _, f := range []string{"", "personal", "ghost"} {
		s := base
		s.Frame = f
		if err := s.Validate(nil); err != nil {
			t.Errorf("Validate(%q, nil): want nil, got %v", f, err)
		}
		if err := s.Validate(map[string]bool{}); err != nil {
			t.Errorf("Validate(%q, empty): want nil, got %v", f, err)
		}
	}

	known := map[string]bool{"personal": true, "work": true}

	// Empty Frame still accepted with a non-empty known set - falls
	// through to runtime active.
	s := base
	s.Frame = ""
	if err := s.Validate(known); err != nil {
		t.Errorf("empty Frame with known set: want nil, got %v", err)
	}

	// In-set Frame accepted.
	s.Frame = "work"
	if err := s.Validate(known); err != nil {
		t.Errorf("work in known: want nil, got %v", err)
	}

	// Out-of-set Frame rejected, error mentions both schedule name and
	// the bad frame value so the user can fix it.
	s.Frame = "ghost"
	err := s.Validate(known)
	if err == nil {
		t.Fatal("ghost frame: want error, got nil")
	}
	if !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "unknown frame") {
		t.Errorf("error spelling drifted: %v", err)
	}
}

// TestParseNatural_DayNamesAllShort spot-checks every short-form day name
// to confirm they each emit the expected weekday number.
func TestParseNatural_DayNamesAllShort(t *testing.T) {
	cases := map[string]string{
		"every sun at 9am": "0 9 * * 0",
		"every mon at 9am": "0 9 * * 1",
		"every tue at 9am": "0 9 * * 2",
		"every wed at 9am": "0 9 * * 3",
		"every thu at 9am": "0 9 * * 4",
		"every fri at 9am": "0 9 * * 5",
		"every sat at 9am": "0 9 * * 6",
	}
	for in, want := range cases {
		got, err := ParseNatural(in)
		if err != nil {
			t.Fatalf("%q: %v", in, err)
		}
		if got.Spec != want {
			t.Errorf("%q: got %q want %q", in, got.Spec, want)
		}
	}
}
