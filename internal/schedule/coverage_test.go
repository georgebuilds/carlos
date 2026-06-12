package schedule

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFireLog_Path covers the Path accessor (both nil and live receiver).
func TestFireLog_Path(t *testing.T) {
	var nilLog *FireLog
	if nilLog.Path() != "" {
		t.Error("nil FireLog Path should be empty")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "fire.log")
	log, err := OpenFireLog(path)
	if err != nil {
		t.Fatalf("OpenFireLog: %v", err)
	}
	defer log.Close()
	if log.Path() != path {
		t.Errorf("Path = %q, want %q", log.Path(), path)
	}
}

// TestOpenFireLog_MkdirError covers the MkdirAll failure branch of
// OpenFireLog: when the parent dir cannot be created (its parent is a
// regular file), Open returns a wrapped mkdir error.
func TestOpenFireLog_MkdirError(t *testing.T) {
	dir := t.TempDir()
	fileAsParent := filepath.Join(dir, "blocker")
	if err := os.WriteFile(fileAsParent, []byte("x"), 0o644); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	// Path lives under the regular file, so MkdirAll of its parent fails.
	_, err := OpenFireLog(filepath.Join(fileAsParent, "sub", "fire.log"))
	if err == nil {
		t.Fatal("OpenFireLog under a file parent: expected mkdir error, got nil")
	}
}

// TestOpenFireLog_OpenError covers the os.OpenFile failure branch: a path
// that is itself a directory cannot be opened O_RDWR as a file.
func TestOpenFireLog_OpenError(t *testing.T) {
	dir := t.TempDir()
	asDir := filepath.Join(dir, "isdir")
	if err := os.Mkdir(asDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := OpenFireLog(asDir); err == nil {
		t.Fatal("OpenFireLog on a directory path: expected open error, got nil")
	}
}

// TestFireLog_AppendAfterCloseErrors covers the write-error branch of
// Append: appending after the underlying file is closed fails the
// WriteString call.
func TestFireLog_AppendAfterCloseErrors(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenFireLog(filepath.Join(dir, "fire.log"))
	if err != nil {
		t.Fatalf("OpenFireLog: %v", err)
	}
	// Close the underlying file out from under Append by closing the log
	// but keeping the handle; Append still holds l.f which is now closed.
	if err := log.f.Close(); err != nil {
		t.Fatalf("close underlying: %v", err)
	}
	slot := time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)
	if err := log.Append("x", slot); err == nil {
		t.Fatal("Append after underlying close: expected write error, got nil")
	}
}

// TestSchedule_DueSlot_IntervalLogSuppresses covers the log.Has() true
// branch of DueSlot for the interval path: a slot already present in the
// log forces due=false even though the schedule would otherwise fire.
func TestSchedule_DueSlot_IntervalLogSuppresses(t *testing.T) {
	dir := t.TempDir()
	log, err := OpenFireLog(filepath.Join(dir, "fire.log"))
	if err != nil {
		t.Fatalf("OpenFireLog: %v", err)
	}
	defer log.Close()
	anchor := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	s := Schedule{
		Name:      "i",
		Kind:      KindInterval,
		Interval:  7 * time.Minute,
		CreatedAt: anchor,
		Prompt:    "x",
	}
	now := anchor.Add(7 * time.Minute)
	slot, due := s.DueSlot(now, log)
	if !due {
		t.Fatal("first DueSlot at +7m: want due=true")
	}
	if err := log.Append(s.Name, slot); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// Same now, slot now recorded → suppressed.
	if _, due := s.DueSlot(now, log); due {
		t.Error("DueSlot after slot logged: want due=false (suppressed)")
	}
}

// TestSchedule_IntervalAnchor_LastRunAtFallback covers the LastRunAt
// branch of intervalAnchor: a config with no CreatedAt (pre-dates the
// field) but a set LastRunAt anchors the arithmetic on LastRunAt.
func TestSchedule_IntervalAnchor_LastRunAtFallback(t *testing.T) {
	last := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	s := Schedule{
		Name:      "i",
		Kind:      KindInterval,
		Interval:  10 * time.Minute,
		LastRunAt: last, // no CreatedAt
		Prompt:    "x",
	}
	// Next after `last` is last + 10m (anchor = LastRunAt).
	got := s.Next(last)
	want := last.Add(10 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("Next anchored on LastRunAt: got %v want %v", got, want)
	}
}

// TestSchedule_IntervalAnchor_NowFallback covers the default (`now`)
// branch of intervalAnchor: with neither CreatedAt nor LastRunAt set,
// the anchor is the caller's `now`, so the first slot is now+Interval.
func TestSchedule_IntervalAnchor_NowFallback(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	s := Schedule{
		Name:     "i",
		Kind:     KindInterval,
		Interval: 10 * time.Minute,
		Prompt:   "x",
	}
	got := s.Next(now)
	want := now.Add(10 * time.Minute)
	if !got.Equal(want) {
		t.Fatalf("Next anchored on now: got %v want %v", got, want)
	}
}

// TestSchedule_DueInterval_CreatedAtGate covers the CreatedAt branch of
// dueInterval: with LastRunAt zero but CreatedAt set, the gate is
// CreatedAt + Interval (never fires immediately at registration time).
func TestSchedule_DueInterval_CreatedAtGate(t *testing.T) {
	created := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	s := Schedule{
		Name:      "i",
		Kind:      KindInterval,
		Interval:  10 * time.Minute,
		CreatedAt: created, // LastRunAt zero
		Prompt:    "x",
	}
	if s.Due(created.Add(9 * time.Minute)) {
		t.Error("Due before CreatedAt+Interval: want false")
	}
	if !s.Due(created.Add(10 * time.Minute)) {
		t.Error("Due at CreatedAt+Interval: want true")
	}
}

// TestSchedule_DueInterval_BothZeroNeverFires covers the both-zero
// branch of dueInterval: with neither anchor set the call observes the
// first `now` but does not fire (the daemon stamps LastRunAt forward).
func TestSchedule_DueInterval_BothZeroNeverFires(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	s := Schedule{
		Name:     "i",
		Kind:     KindInterval,
		Interval: 10 * time.Minute,
		Prompt:   "x",
	}
	if s.Due(now) {
		t.Error("Due with both anchors zero: want false on first observation")
	}
}

// TestSchedule_DueInterval_ZeroIntervalNeverFires covers the
// non-positive-interval guard in dueInterval.
func TestSchedule_DueInterval_ZeroIntervalNeverFires(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	s := Schedule{Name: "i", Kind: KindInterval, Interval: 0, Prompt: "x"}
	if s.Due(now) {
		t.Error("Due with zero interval: want false")
	}
}

// TestSchedule_SlotFor_IntervalBeforeAnchor covers the diff<0 branch of
// SlotFor: when `now` precedes the anchor (synthetic test clock), the
// slot is the anchor itself.
func TestSchedule_SlotFor_IntervalBeforeAnchor(t *testing.T) {
	anchor := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	s := Schedule{
		Name:      "i",
		Kind:      KindInterval,
		Interval:  10 * time.Minute,
		CreatedAt: anchor,
		Prompt:    "x",
	}
	got := s.SlotFor(anchor.Add(-5 * time.Minute))
	if !got.Equal(anchor) {
		t.Fatalf("SlotFor before anchor: got %v want anchor %v", got, anchor)
	}
}

// TestSchedule_SlotFor_ZeroIntervalReturnsZero covers the zero-interval
// guard in SlotFor's interval branch.
func TestSchedule_SlotFor_ZeroIntervalReturnsZero(t *testing.T) {
	s := Schedule{Name: "i", Kind: KindInterval, Interval: 0, Prompt: "x"}
	if got := s.SlotFor(time.Now()); !got.IsZero() {
		t.Fatalf("SlotFor zero interval: got %v want zero", got)
	}
}

// TestSchedule_SlotFor_BadCronReturnsZero covers the ParseCron-error
// branch of SlotFor (cron path).
func TestSchedule_SlotFor_BadCronReturnsZero(t *testing.T) {
	s := Schedule{Name: "bad", Spec: "not a cron", Prompt: "x"}
	if got := s.SlotFor(time.Now()); !got.IsZero() {
		t.Fatalf("SlotFor bad cron: got %v want zero", got)
	}
}

// TestSchedule_Next_BadCronReturnsZero covers the ParseCron-error branch
// of Schedule.Next (cron path).
func TestSchedule_Next_BadCronReturnsZero(t *testing.T) {
	s := Schedule{Name: "bad", Spec: "garbage spec here", Prompt: "x"}
	if got := s.Next(time.Now()); !got.IsZero() {
		t.Fatalf("Next bad cron: got %v want zero", got)
	}
}

// TestSchedule_Due_BadCronReturnsFalse covers the next.IsZero() branch
// of Due: a malformed cron makes Next return the zero time, so Due must
// report false rather than firing.
func TestSchedule_Due_BadCronReturnsFalse(t *testing.T) {
	s := Schedule{Name: "bad", Spec: "nonsense", Prompt: "x"}
	if s.Due(time.Now()) {
		t.Error("Due with malformed cron: want false")
	}
}

// TestSchedule_DueSlot_NotDueReturnsZero covers the early-return branch
// of DueSlot when the schedule is not due.
func TestSchedule_DueSlot_NotDueReturnsZero(t *testing.T) {
	s := Schedule{
		Name:      "morning",
		Spec:      "0 9 * * *",
		Prompt:    "x",
		LastRunAt: time.Date(2026, 6, 10, 9, 0, 0, 0, time.Local),
	}
	// 9:30, already ran at 9:00 → not due.
	now := time.Date(2026, 6, 10, 9, 30, 0, 0, time.Local)
	slot, due := s.DueSlot(now, nil)
	if due {
		t.Error("DueSlot when not due: want due=false")
	}
	if !slot.IsZero() {
		t.Errorf("DueSlot not-due slot: got %v want zero", slot)
	}
}

// TestSchedule_DueSlot_IntervalHappy pins the interval DueSlot happy
// path: a due interval schedule returns due=true and a non-zero slot.
func TestSchedule_DueSlot_IntervalHappy(t *testing.T) {
	anchor := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	s := Schedule{
		Name:      "i",
		Kind:      KindInterval,
		Interval:  7 * time.Minute,
		CreatedAt: anchor,
		Prompt:    "x",
	}
	slot, due := s.DueSlot(anchor.Add(7*time.Minute), nil)
	if !due {
		t.Fatal("interval DueSlot at +7m: want due=true")
	}
	if slot.IsZero() {
		t.Fatal("interval DueSlot returned zero slot when due")
	}
}

// TestCronExpr_Next_NoMatchReturnsZero covers the deadline-exceeded
// branch of CronExpr.Next: Feb 30 never exists, so the 4-year search
// exhausts and returns the zero time.
func TestCronExpr_Next_NoMatchReturnsZero(t *testing.T) {
	// "0 0 30 2 *" - the 30th of February, which never occurs.
	c, err := ParseCron("0 0 30 2 *")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	got := c.Next(time.Date(2026, 1, 1, 0, 0, 0, 0, time.Local))
	if !got.IsZero() {
		t.Fatalf("Next for impossible date: got %v want zero", got)
	}
}

// TestCronExpr_Match_DomDowOrSemantics pins the POSIX OR-semantics of
// match when both day-of-month and day-of-week are constrained: a tick
// fires if EITHER matches. We use "0 0 13 * 5" (the 13th OR any Friday).
func TestCronExpr_Match_DomDowOrSemantics(t *testing.T) {
	c, err := ParseCron("0 0 13 * 5")
	if err != nil {
		t.Fatalf("ParseCron: %v", err)
	}
	// 2026-02-13 is a Friday → both match.
	fri13 := time.Date(2026, 2, 13, 0, 0, 0, 0, time.Local)
	if !c.match(fri13) {
		t.Error("Friday the 13th must match (both DoM and DoW)")
	}
	// 2026-03-13 is a Friday → DoW matches, DoM matches too (it's the 13th).
	// Use a plain Friday that is NOT the 13th: 2026-02-06 is a Friday.
	friNot13 := time.Date(2026, 2, 6, 0, 0, 0, 0, time.Local)
	if !c.match(friNot13) {
		t.Error("a Friday that is not the 13th must still match via DoW (OR)")
	}
	// The 13th of a month that is not a Friday: 2026-01-13 is a Tuesday.
	the13Tue := time.Date(2026, 1, 13, 0, 0, 0, 0, time.Local)
	if !c.match(the13Tue) {
		t.Error("the 13th on a non-Friday must match via DoM (OR)")
	}
	// A day that is neither the 13th nor a Friday must NOT match.
	neither := time.Date(2026, 1, 14, 0, 0, 0, 0, time.Local) // Wed the 14th
	if c.match(neither) {
		t.Error("neither-13th-nor-Friday must NOT match")
	}
}
