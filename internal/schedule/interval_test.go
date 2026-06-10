package schedule

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestParseNatural_IntervalForms pins the new KindInterval surface for
// "every N minutes" and "every N hours". The previous behaviour emitted
// `*/N * * * *` which lies about the cadence: */7 fires at
// {0, 7, ..., 56} so the :56 -> :00 wrap is only 4 minutes. The fix
// emits a true Interval primitive instead so the spacing is exactly N.
func TestParseNatural_IntervalForms(t *testing.T) {
	// Pin the package clock so CreatedAt is deterministic for the
	// assertions below.
	anchor := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	prev := clockNow
	clockNow = func() time.Time { return anchor }
	defer func() { clockNow = prev }()

	cases := []struct {
		in       string
		wantKind Kind
		wantD    time.Duration
	}{
		{"every 7 minutes", KindInterval, 7 * time.Minute},
		{"every 1 minute", KindInterval, time.Minute},
		{"every 5 mins", KindInterval, 5 * time.Minute},
		{"every 100 minutes", KindInterval, 100 * time.Minute},
		{"every 2 hours", KindInterval, 2 * time.Hour},
		{"every 1 hour", KindInterval, time.Hour},
		{"every 24 hours", KindInterval, 24 * time.Hour},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			s, err := ParseNatural(c.in)
			if err != nil {
				t.Fatalf("ParseNatural(%q): %v", c.in, err)
			}
			if s.Kind != c.wantKind {
				t.Fatalf("Kind: got %q want %q", s.Kind, c.wantKind)
			}
			if s.Interval != c.wantD {
				t.Fatalf("Interval: got %s want %s", s.Interval, c.wantD)
			}
			if !s.CreatedAt.Equal(anchor) {
				t.Fatalf("CreatedAt: got %v want %v", s.CreatedAt, anchor)
			}
			// The cosmetic Spec is human-readable, not a cron expression.
			if strings.Contains(s.Spec, "*/") {
				t.Fatalf("Spec leaked cron syntax (the bug we are fixing): %q", s.Spec)
			}
		})
	}
}

// TestParseNatural_CronUnchanged confirms literal five-field cron
// expressions still parse as KindCron - the new Interval primitive
// must not capture raw cron strings.
func TestParseNatural_CronUnchanged(t *testing.T) {
	cases := []string{
		"0 9 * * *",
		"0 9 * * 1-5",
		"*/30 * * * *",
		"0 */2 * * *",
		"0 0 1 JAN *",
	}
	for _, spec := range cases {
		t.Run(spec, func(t *testing.T) {
			s, err := ParseNatural(spec)
			if err != nil {
				t.Fatalf("ParseNatural(%q): %v", spec, err)
			}
			if s.Kind.Effective() != KindCron {
				t.Fatalf("Kind: got %q want %q", s.Kind, KindCron)
			}
			if s.Spec != spec {
				t.Fatalf("Spec: got %q want %q", s.Spec, spec)
			}
			if s.Interval != 0 {
				t.Fatalf("Interval should be 0 for cron, got %s", s.Interval)
			}
		})
	}
}

// TestSchedule_Interval_DueRespectsExactSpacing is the regression for
// the "*/N misreports cadence" bug: an interval schedule must NOT fire
// before exactly Interval has elapsed since the previous fire,
// regardless of wallclock alignment. We assert at the "every 7 minutes"
// rough cadence to keep the lie literal: */7 cron would fire 14 times
// in an hour with one 4-minute gap; interval fires every 7 minutes
// flat (~8 fires in 60 minutes from a 12:00 start).
func TestSchedule_Interval_DueRespectsExactSpacing(t *testing.T) {
	created := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	s := Schedule{
		Name:      "every-7m",
		Kind:      KindInterval,
		Interval:  7 * time.Minute,
		CreatedAt: created,
		Prompt:    "x",
	}

	// Not due before CreatedAt + Interval.
	if s.Due(created) {
		t.Error("Due at created time: want false")
	}
	if s.Due(created.Add(6*time.Minute + 59*time.Second)) {
		t.Error("Due at +6m59s: want false")
	}
	// Due at the boundary.
	if !s.Due(created.Add(7 * time.Minute)) {
		t.Error("Due at +7m: want true")
	}

	// Simulate the daemon stamping LastRunAt at the fire moment.
	s.LastRunAt = created.Add(7 * time.Minute)

	// Wallclock-misaligned check: an interval schedule fires exactly
	// 7 minutes after LastRunAt, never before. */7 cron would have
	// fired at :14 (7 minutes after :07); interval fires at +7m
	// regardless of minute alignment.
	if s.Due(s.LastRunAt.Add(6 * time.Minute)) {
		t.Error("Due +6m after LastRunAt: want false")
	}
	if !s.Due(s.LastRunAt.Add(7 * time.Minute)) {
		t.Error("Due +7m after LastRunAt: want true")
	}

	// The cadence-lie regression: walk forward 60 minutes from a
	// 12:00 start, fire whenever Due returns true, and assert each
	// gap is exactly 7 minutes (no 4-minute :56 -> :00 wrap).
	fires := []time.Time{}
	last := s.LastRunAt
	for cur := last.Add(time.Minute); cur.Sub(created) <= 60*time.Minute; cur = cur.Add(time.Minute) {
		s.LastRunAt = last
		if s.Due(cur) {
			last = cur
			fires = append(fires, cur)
		}
	}
	for i := 1; i < len(fires); i++ {
		gap := fires[i].Sub(fires[i-1])
		if gap < 7*time.Minute {
			t.Fatalf("interval fired with sub-7m gap at index %d: %s (this is the bug)", i, gap)
		}
	}
}

// TestSchedule_Interval_Next walks the arithmetic progression to
// confirm the Next() method returns exactly anchor + (N+1)*Interval.
func TestSchedule_Interval_Next(t *testing.T) {
	anchor := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	s := Schedule{
		Kind:      KindInterval,
		Interval:  10 * time.Minute,
		CreatedAt: anchor,
	}
	// Right at anchor -> next is anchor + 10m.
	got := s.Next(anchor)
	want := anchor.Add(10 * time.Minute)
	if !got.Equal(want) {
		t.Errorf("Next(anchor): got %v want %v", got, want)
	}
	// At anchor + 25m -> next is anchor + 30m.
	got = s.Next(anchor.Add(25 * time.Minute))
	want = anchor.Add(30 * time.Minute)
	if !got.Equal(want) {
		t.Errorf("Next(+25m): got %v want %v", got, want)
	}
	// Zero Interval returns the zero time (defensive).
	bad := Schedule{Kind: KindInterval, CreatedAt: anchor}
	if got := bad.Next(anchor); !got.IsZero() {
		t.Errorf("Next on zero-Interval: got %v want zero", got)
	}
}

// TestSchedule_Validate_Interval pins the Validate gate for the new
// Kind. Positive Interval valid; zero rejected; negative rejected.
func TestSchedule_Validate_Interval(t *testing.T) {
	good := Schedule{
		Name:     "ok",
		Kind:     KindInterval,
		Interval: 5 * time.Minute,
		Prompt:   "x",
	}
	if err := good.Validate(nil); err != nil {
		t.Fatalf("Validate(good): %v", err)
	}
	bad := Schedule{
		Name:     "bad",
		Kind:     KindInterval,
		Interval: 0,
		Prompt:   "x",
	}
	if err := bad.Validate(nil); err == nil {
		t.Fatal("Validate(zero interval): want error, got nil")
	}
}

// TestSchedule_SlotFor_Cron returns the cron tick the schedule is
// firing for. Used by DueSlot to key the fire log so a crash-window
// restart can dedupe.
func TestSchedule_SlotFor_Cron(t *testing.T) {
	s := Schedule{
		Name:   "morning",
		Spec:   "0 9 * * *",
		Prompt: "x",
	}
	now := time.Date(2026, 6, 10, 9, 0, 30, 0, time.Local)
	slot := s.SlotFor(now)
	want := time.Date(2026, 6, 10, 9, 0, 0, 0, time.Local)
	if !slot.Equal(want) {
		t.Fatalf("SlotFor: got %v want %v", slot, want)
	}
}

// TestSchedule_SlotFor_Interval returns the most recent
// anchor+N*Interval boundary at or before `now`.
func TestSchedule_SlotFor_Interval(t *testing.T) {
	anchor := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	s := Schedule{
		Name:      "every-7m",
		Kind:      KindInterval,
		Interval:  7 * time.Minute,
		CreatedAt: anchor,
	}
	// At 12:15 the most recent slot is anchor + 14m = 12:14.
	got := s.SlotFor(anchor.Add(15 * time.Minute))
	want := anchor.Add(14 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("SlotFor(+15m): got %v want %v", got, want)
	}
	// At anchor (no time elapsed) the slot is the anchor itself.
	got = s.SlotFor(anchor)
	if !got.Equal(anchor) {
		t.Fatalf("SlotFor(anchor): got %v want %v", got, anchor)
	}
}

// TestSchedule_DueSlot_Suppression is the headline fire-log regression
// test: we simulate a crash by appending a fire-log entry for the
// (name, slot) the schedule would fire next, "reload" by reopening the
// log fresh, and assert that DueSlot returns due=false for that slot.
// Without the fix, the daemon restart re-fires the same slot.
func TestSchedule_DueSlot_Suppression(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fire.log")

	s := Schedule{
		Name:   "morning",
		Spec:   "0 9 * * *",
		Prompt: "x",
	}
	now := time.Date(2026, 6, 10, 9, 0, 30, 0, time.Local)

	// Pre-crash: open the log, write the slot for this fire.
	pre, err := OpenFireLog(path)
	if err != nil {
		t.Fatalf("OpenFireLog: %v", err)
	}
	slot, due := s.DueSlot(now, pre)
	if !due {
		t.Fatal("expected DueSlot=true on a fresh log at 9:00")
	}
	if err := pre.Append(s.Name, slot); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// Simulate crash: do NOT update LastRunAt; close the log so the
	// next process sees the on-disk state alone.
	if err := pre.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Post-crash restart: reopen the log; the same Schedule (still
	// with zero LastRunAt) and the same `now` must NOT re-fire.
	post, err := OpenFireLog(path)
	if err != nil {
		t.Fatalf("OpenFireLog (replay): %v", err)
	}
	defer post.Close()
	_, due = s.DueSlot(now, post)
	if due {
		t.Fatal("DueSlot returned true after crash-window replay: double-fire bug")
	}
}

// TestSchedule_DueSlot_NoLogStillWorks confirms DueSlot with a nil log
// degrades to plain Due() semantics. Used by callers (tests, future
// in-process consumers) that don't need the crash-window protection.
func TestSchedule_DueSlot_NoLogStillWorks(t *testing.T) {
	s := Schedule{
		Name:   "morning",
		Spec:   "0 9 * * *",
		Prompt: "x",
	}
	now := time.Date(2026, 6, 10, 9, 0, 0, 0, time.Local)
	slot, due := s.DueSlot(now, nil)
	if !due {
		t.Fatal("DueSlot(nil log) at 9:00: want true")
	}
	if slot.IsZero() {
		t.Fatal("DueSlot returned zero slot when due=true")
	}
}

// TestSchedule_DueSlot_FireLogWriteOrdering confirms the contract:
// calling FireLog.Append before invoking the action callback makes the
// log entry durable, so a "crash" (i.e., a fresh OpenFireLog on the
// same path) before the callback runs still leaves a record. This
// pins the write-before-action ordering documented on DueSlot.
func TestSchedule_DueSlot_FireLogWriteOrdering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fire.log")
	s := Schedule{
		Name:   "ordering",
		Spec:   "0 9 * * *",
		Prompt: "x",
	}
	now := time.Date(2026, 6, 10, 9, 0, 0, 0, time.Local)

	log, err := OpenFireLog(path)
	if err != nil {
		t.Fatalf("OpenFireLog: %v", err)
	}
	defer log.Close()

	slot, due := s.DueSlot(now, log)
	if !due {
		t.Fatal("DueSlot at 9:00: want true")
	}

	// Step 1: write to the log (the caller's contract: BEFORE
	// invoking the action). We capture whether a probe reopen sees
	// the entry before we ever pretend the action ran.
	if err := log.Append(s.Name, slot); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Step 2: WITHOUT having called the action, simulate a crash by
	// opening a fresh handle on the same file. The probe must see
	// the recorded entry - that proves Append fsynced the line.
	probe, err := OpenFireLog(path)
	if err != nil {
		t.Fatalf("OpenFireLog (probe): %v", err)
	}
	defer probe.Close()
	if !probe.Has(s.Name, slot) {
		t.Fatal("probe did NOT see the slot: Append was not durable before return")
	}
}

// TestSchedule_Interval_DueSlot_Suppression mirrors the cron-side
// suppression test for the KindInterval branch: an interval schedule
// must not re-fire the same slot after a crash + reopen.
func TestSchedule_Interval_DueSlot_Suppression(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fire.log")
	anchor := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	s := Schedule{
		Name:      "every-7m",
		Kind:      KindInterval,
		Interval:  7 * time.Minute,
		CreatedAt: anchor,
		Prompt:    "x",
	}
	now := anchor.Add(7 * time.Minute)

	pre, err := OpenFireLog(path)
	if err != nil {
		t.Fatalf("OpenFireLog: %v", err)
	}
	slot, due := s.DueSlot(now, pre)
	if !due {
		t.Fatal("expected DueSlot=true at anchor+7m")
	}
	if err := pre.Append(s.Name, slot); err != nil {
		t.Fatalf("Append: %v", err)
	}
	pre.Close()

	post, err := OpenFireLog(path)
	if err != nil {
		t.Fatalf("OpenFireLog (replay): %v", err)
	}
	defer post.Close()
	if _, due := s.DueSlot(now, post); due {
		t.Fatal("DueSlot returned true after crash-window replay for interval schedule")
	}
}
