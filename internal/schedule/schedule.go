// Package schedule owns carlos's scheduled-run vocabulary:
//
//   - The on-disk Schedule record (lives in ~/.carlos/config.yaml under
//     the `schedules:` key).
//   - A 5-field cron expression parser (minute hour day-of-month month
//     day-of-week) - enough grammar to cover everything ParseNatural
//     emits and the manual cron strings the user can paste in.
//   - Schedule.Next(after) - the daemon's tick loop calls this per
//     schedule each pass to decide if it's due.
//   - parse.go: ParseNatural - small natural-language frontend covering
//     the ~7 forms that account for ~90% of personal use; falls back to
//     "parse as cron" so power users can paste a 5-field expression.
//
// Design notes:
//
//   - The cron parser is intentionally hand-rolled (no third-party
//     dependency). The grammar is the standard POSIX 5-field form with
//     '*', step values ('*/N'), ranges ('1-5'), and lists ('MON,WED,FRI').
//     Day names (SUN..SAT) and month names (JAN..DEC) are accepted in
//     the corresponding fields, case-insensitive.
//   - Time zone is the local zone (time.Local). DST-aware via the
//     standard library: time.AddDate / time.Date naturally walk through
//     the spring-forward gap by snapping to the next valid wall time;
//     Next() iterates minute-by-minute so a missed minute during the
//     gap doesn't fire (matches the cron-spec behavior in vixie / Mac
//     launchd).
//   - Once-only schedules: parse_test confirms ParseNatural("tomorrow
//     at 3pm") returns Schedule{Once: true}. The daemon deletes the
//     entry from config after it fires.
//   - Storage shape: see Schedule struct doc; the YAML round-trips
//     cleanly through internal/miniyaml (JSON-tagged structs decoded
//     via UnmarshalStruct).
package schedule

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Schedule is one scheduled run. The daemon iterates Schedules at every
// tick and fires the ones whose Next() <= now.
//
// On-disk shape (under ~/.carlos/config.yaml `schedules:`):
//
//	schedules:
//	  - name: morning-slack
//	    spec: "0 9 * * 1-5"
//	    prompt: "Summarize my unread Slack DMs"
//	    budget_tokens: 8000
//	    budget_cents: 50
//	    once: false
//	    last_run_at: 2026-06-04T09:00:00Z
//	    last_run_ok: true
//
// Optional fields (BudgetTokens, BudgetCents) cap the per-run cost the
// daemon hands the supervisor's SpawnContract so a misfiring cron can't
// burn the user's API balance unsupervised.
// Kind names the firing model behind a Schedule. The empty string is
// treated as KindCron so older YAML configs (which have no `kind:` key)
// keep loading without a migration step.
type Kind string

const (
	// KindCron evaluates a 5-field cron expression against wall clock.
	// Empty Kind is equivalent to KindCron on load.
	KindCron Kind = "cron"
	// KindInterval fires every Interval starting from CreatedAt + Interval,
	// then exactly Interval apart regardless of wall-clock alignment.
	// Introduced to fix the "every N minutes" cron-step lie: */7 fires at
	// {0,7,14,...,56} so the :56 -> :00 wrap is only 4 minutes, not 7.
	KindInterval Kind = "interval"
)

// Effective returns k or KindCron if k is the empty Kind. Centralised so
// every dispatch site agrees on the default.
func (k Kind) Effective() Kind {
	if k == "" {
		return KindCron
	}
	return k
}

type Schedule struct {
	// Name is the user-supplied handle ("morning-slack"). Required and
	// unique within a config. /schedule rm uses this as the key.
	Name string `json:"name"`

	// Kind selects the firing model: KindCron (default; empty == cron) or
	// KindInterval. ParseNatural emits KindInterval for "every N
	// minutes/hours"; literal five-field cron strings stay KindCron.
	Kind Kind `json:"kind,omitempty"`

	// Interval is the firing period for KindInterval schedules. Ignored
	// for KindCron. Wire format mirrors the other time.Duration fields
	// in the project (internal/agent/eventlog.go, internal/usershell/
	// events.go): the JSON encoder emits an int64 of nanoseconds, so
	// the YAML on disk reads `interval: 420000000000` rather than `7m`.
	// Cosmetic only; the daemon never displays Interval directly.
	Interval time.Duration `json:"interval,omitempty"`

	// CreatedAt anchors KindInterval schedules: the first fire happens
	// at CreatedAt + Interval, the Nth fire at CreatedAt + N*Interval.
	// ParseNatural stamps this from the package clock for new interval
	// schedules; pre-existing configs (no CreatedAt on disk) fall back
	// to LastRunAt as the anchor and, when both are zero, to the first
	// Due() call's `now` as a one-time anchor (the schedule will then
	// fire one Interval later).
	CreatedAt time.Time `json:"created_at,omitempty"`

	// Spec is the 5-field cron expression. ParseNatural emits this from
	// English phrases; users can also paste cron directly. For KindInterval
	// schedules Spec carries a human-readable label ("every 7 minutes")
	// rather than a cron expression and is not parsed.
	Spec string `json:"spec"`

	// Prompt is the user-bound text fed to the supervisor as the
	// SpawnContract.Objective when this schedule fires.
	Prompt string `json:"prompt"`

	// BudgetTokens caps total tokens for one fire (0 = no cap). The
	// daemon wires this into SpawnContract.MaxTokens.
	BudgetTokens int `json:"budget_tokens,omitempty"`

	// BudgetCents caps total spend in integer cents for one fire.
	// The daemon wires this into the supervisor's run-wide Tracker
	// (Budget.MaxCostCents).
	BudgetCents int `json:"budget_cents,omitempty"`

	// Once flags a one-shot schedule. ParseNatural("tomorrow at 3pm")
	// sets this; the daemon removes the entry from config after it
	// fires successfully.
	Once bool `json:"once,omitempty"`

	// LastRunAt is the most recent fire time (UTC). Used by the daemon
	// to avoid double-firing when the tick loop restarts mid-minute.
	LastRunAt time.Time `json:"last_run_at,omitempty"`

	// LastRunOK records whether the most recent run terminated cleanly
	// (state=done) or not. Daemon status surfaces this; it does NOT
	// gate the next fire (a failing schedule keeps firing).
	LastRunOK bool `json:"last_run_ok,omitempty"`

	// Frame names the carlos frame this schedule should run in
	// (Phase F-14). Empty falls back to the user's persisted active
	// frame at fire time; explicit value sets the frame even when the
	// user has switched in the meantime. The daemon honours this when
	// constructing the per-run sysprompt + tool registry.
	Frame string `json:"frame,omitempty"`
}

// Validate returns nil iff the schedule is well-formed: a non-empty
// name, a non-empty prompt, and a parseable Spec.
//
// known is the optional set of frame names recognised by the current
// config; a non-empty Frame field must appear in this set. An empty
// known (nil or len 0) skips the membership check so tests and any
// path that legitimately runs without a frame catalog stay valid.
// An empty Frame field is always accepted - that falls through to the
// runtime's active frame at fire time.
func (s Schedule) Validate(known map[string]bool) error {
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("schedule: empty name")
	}
	if strings.TrimSpace(s.Prompt) == "" {
		return errors.New("schedule: empty prompt")
	}
	switch s.Kind.Effective() {
	case KindInterval:
		if s.Interval <= 0 {
			return fmt.Errorf("schedule %q: interval kind requires positive interval, got %s", s.Name, s.Interval)
		}
	default:
		if _, err := ParseCron(s.Spec); err != nil {
			return fmt.Errorf("schedule %q: %w", s.Name, err)
		}
	}
	if s.Frame != "" && len(known) > 0 && !known[s.Frame] {
		return fmt.Errorf("schedule %q: unknown frame %q", s.Name, s.Frame)
	}
	return nil
}

// Next returns the next firing time strictly after `after`, in local
// time. If no future time matches within 4 years (effectively never -
// a malformed cron) it returns the zero time.
//
// For KindInterval the anchor is intervalAnchor(s): Next walks the
// arithmetic progression anchor + N*Interval and returns the first
// instant strictly after `after`. A zero Interval returns the zero time.
func (s Schedule) Next(after time.Time) time.Time {
	if s.Kind.Effective() == KindInterval {
		if s.Interval <= 0 {
			return time.Time{}
		}
		anchor := s.intervalAnchor(after)
		// First firing slot is anchor + Interval; subsequent slots step by Interval.
		// Walk forward until we land strictly after `after`.
		diff := after.Sub(anchor)
		// Number of full intervals already elapsed since the anchor.
		n := int64(diff / s.Interval)
		next := anchor.Add(time.Duration(n+1) * s.Interval)
		return next
	}
	c, err := ParseCron(s.Spec)
	if err != nil {
		return time.Time{}
	}
	return c.Next(after)
}

// intervalAnchor picks the reference time for KindInterval arithmetic.
// CreatedAt wins when set (the canonical anchor the parser stamps);
// LastRunAt covers configs that pre-date the CreatedAt field; falling
// back to `now` lets a stale-zero schedule still fire one Interval
// later instead of misbehaving. Caller-provided `now` is only used
// for that last-ditch fallback so the function stays deterministic
// when at least one anchor is set on disk.
func (s Schedule) intervalAnchor(now time.Time) time.Time {
	switch {
	case !s.CreatedAt.IsZero():
		return s.CreatedAt
	case !s.LastRunAt.IsZero():
		return s.LastRunAt
	default:
		return now
	}
}

// Due reports whether this schedule should fire at `now`.
//
// For KindCron the check is minute-granular (so the tick loop's 30s
// cadence catches every minute without double-firing): Next(LastRunAt)
// <= now. On first ever run (LastRunAt zero), uses now.Add(-time.Minute)
// as the "after" anchor so a schedule that should have fired exactly at
// startup time still gets picked up.
//
// For KindInterval, due iff `now.Sub(LastRunAt) >= Interval`. When
// LastRunAt is zero, CreatedAt + Interval is the gate, so an interval
// schedule never fires immediately at registration time.
func (s Schedule) Due(now time.Time) bool {
	if s.Kind.Effective() == KindInterval {
		return s.dueInterval(now)
	}
	anchor := s.LastRunAt
	if anchor.IsZero() {
		anchor = now.Add(-time.Minute)
	}
	next := s.Next(anchor)
	if next.IsZero() {
		return false
	}
	return !next.After(now)
}

// dueInterval handles the KindInterval branch of Due. Centralised so
// DueSlot can share the gate expression without duplicating the
// anchor-selection rules.
func (s Schedule) dueInterval(now time.Time) bool {
	if s.Interval <= 0 {
		return false
	}
	if !s.LastRunAt.IsZero() {
		return now.Sub(s.LastRunAt) >= s.Interval
	}
	if !s.CreatedAt.IsZero() {
		return now.Sub(s.CreatedAt) >= s.Interval
	}
	// Both zero: treat the first observation as the anchor (caller's
	// `now` is the only reference we have). Never fires on this call;
	// the daemon's next persistSchedules will stamp LastRunAt forward.
	return false
}

// SlotFor returns the canonical timestamp identifying the firing slot
// the schedule would land on at `now`. The slot is the (schedule_name,
// slot_time) key the fire log uses to suppress duplicates after a
// crash-window restart.
//
// For KindCron the slot is the cron tick the schedule is about to fire
// for (the matching minute, truncated to whole minutes). For
// KindInterval the slot is the most recent anchor+N*Interval boundary
// at or before `now` (truncated to interval).
//
// SlotFor does NOT gate on whether the schedule is currently due; pair
// it with Due (or use DueSlot) to get both signals together.
func (s Schedule) SlotFor(now time.Time) time.Time {
	if s.Kind.Effective() == KindInterval {
		if s.Interval <= 0 {
			return time.Time{}
		}
		anchor := s.intervalAnchor(now)
		diff := now.Sub(anchor)
		if diff < 0 {
			// `now` precedes the anchor (synthetic clock in a test): the
			// schedule hasn't reached its first slot yet.
			return anchor
		}
		n := int64(diff / s.Interval)
		return anchor.Add(time.Duration(n) * s.Interval)
	}
	c, err := ParseCron(s.Spec)
	if err != nil {
		return time.Time{}
	}
	// For cron, the slot is the most recent matching minute at or
	// before `now`. Walk backward from `now` minute-by-minute up to
	// the configured deadline so a sparse schedule still resolves.
	t := now.In(time.Local).Truncate(time.Minute)
	// 4 years backwards mirrors CronExpr.Next's forward search bound;
	// guards against a malformed expression making this loop unbounded.
	floor := t.Add(-4 * 365 * 24 * time.Hour)
	for t.After(floor) || t.Equal(floor) {
		if c.match(t) {
			return t
		}
		t = t.Add(-time.Minute)
	}
	return time.Time{}
}

// DueSlot reports whether the schedule should fire at `now` and returns
// the slot timestamp the firing maps to. When log is non-nil, a slot
// already present in the log forces due=false (this is the crash-window
// suppression path: the daemon writes the slot to the log BEFORE
// invoking the action, so a restart mid-action sees the recorded slot
// and skips re-firing).
//
// Callers should:
//  1. call DueSlot(now, log)
//  2. if due, log.Append(name, slot) BEFORE invoking the action
//  3. invoke the action
//
// That ordering preserves "fire at most once per slot" across crashes.
// The returned slot is the zero time when due=false.
func (s Schedule) DueSlot(now time.Time, log *FireLog) (time.Time, bool) {
	if !s.Due(now) {
		return time.Time{}, false
	}
	slot := s.SlotFor(now)
	if slot.IsZero() {
		return time.Time{}, false
	}
	if log != nil && log.Has(s.Name, slot) {
		return time.Time{}, false
	}
	return slot, true
}

// CronExpr is the parsed form of a 5-field cron expression. The parser
// expands each field into a sorted bitmap of integers (the valid set);
// Next() iterates minute-by-minute matching all five.
type CronExpr struct {
	Minute     []int // 0..59
	Hour       []int // 0..23
	DayOfMonth []int // 1..31
	Month      []int // 1..12
	DayOfWeek  []int // 0..6 (0=Sun)

	// domStar / dowStar capture "this field was *". When BOTH dom and
	// dow are constrained (non-*) the cron spec says either match
	// fires (OR semantics). When one is * the other is the gate.
	domStar bool
	dowStar bool
}

// Next walks minutes forward from `after` and returns the first instant
// that matches the expression. Stops after 4 years of wall time to
// avoid pathological loops on a malformed parse.
func (c CronExpr) Next(after time.Time) time.Time {
	// Round up to the next whole minute strictly after `after`. We use
	// local time because cron expressions are wall-clock by convention.
	t := after.In(time.Local).Add(time.Minute).Truncate(time.Minute)
	deadline := t.Add(4 * 365 * 24 * time.Hour)
	for t.Before(deadline) {
		if c.match(t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

// match reports whether wall-time `t` satisfies every field of the
// expression. DoM/DoW OR-semantic per POSIX cron when both are
// constrained.
func (c CronExpr) match(t time.Time) bool {
	if !contains(c.Minute, t.Minute()) {
		return false
	}
	if !contains(c.Hour, t.Hour()) {
		return false
	}
	if !contains(c.Month, int(t.Month())) {
		return false
	}
	domOK := contains(c.DayOfMonth, t.Day())
	dowOK := contains(c.DayOfWeek, int(t.Weekday()))
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return dowOK
	case c.dowStar:
		return domOK
	default:
		// Both constrained → OR.
		return domOK || dowOK
	}
}

func contains(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

// ParseCron parses a 5-field cron expression. Accepts:
//
//   - `*`           - every value in the field's range
//   - `N`           - exact value (0..59 for minute, etc)
//   - `N-M`         - inclusive range
//   - `*/S`         - every S values in the full range (0, S, 2S, ...)
//   - `N-M/S`       - every S values within range [N, M]
//   - `A,B,C`       - list (each element follows the rules above)
//   - day names     - SUN..SAT in the day-of-week field (case-insensitive)
//   - month names   - JAN..DEC in the month field (case-insensitive)
//
// Returns a descriptive error on the first malformed field so the user
// can see which one they need to fix.
func ParseCron(spec string) (CronExpr, error) {
	parts := strings.Fields(strings.TrimSpace(spec))
	if len(parts) != 5 {
		return CronExpr{}, fmt.Errorf("cron: expected 5 fields, got %d (%q)", len(parts), spec)
	}
	c := CronExpr{}
	var err error
	c.Minute, _, err = parseField(parts[0], 0, 59, nil)
	if err != nil {
		return CronExpr{}, fmt.Errorf("cron field 1 (minute): %w", err)
	}
	c.Hour, _, err = parseField(parts[1], 0, 23, nil)
	if err != nil {
		return CronExpr{}, fmt.Errorf("cron field 2 (hour): %w", err)
	}
	c.DayOfMonth, c.domStar, err = parseField(parts[2], 1, 31, nil)
	if err != nil {
		return CronExpr{}, fmt.Errorf("cron field 3 (day-of-month): %w", err)
	}
	c.Month, _, err = parseField(parts[3], 1, 12, monthNames)
	if err != nil {
		return CronExpr{}, fmt.Errorf("cron field 4 (month): %w", err)
	}
	c.DayOfWeek, c.dowStar, err = parseField(parts[4], 0, 6, dayNames)
	if err != nil {
		return CronExpr{}, fmt.Errorf("cron field 5 (day-of-week): %w", err)
	}
	return c, nil
}

// monthNames + dayNames: cron-standard 3-letter abbreviations.
var monthNames = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}
var dayNames = map[string]int{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

// parseField expands one comma-separated cron field into a sorted slice
// of valid values within [lo, hi]. The second return reports whether
// the field was a single '*' (relevant only for day-of-month and
// day-of-week to capture the OR semantic).
func parseField(field string, lo, hi int, aliases map[string]int) ([]int, bool, error) {
	field = strings.TrimSpace(field)
	if field == "" {
		return nil, false, errors.New("empty field")
	}
	star := field == "*" || strings.HasPrefix(field, "*/")
	set := map[int]bool{}
	for _, part := range strings.Split(field, ",") {
		if err := parsePart(part, lo, hi, aliases, set); err != nil {
			return nil, false, err
		}
	}
	out := make([]int, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	// Sort: simple insertion sort since the slices are at most 60 wide.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, star, nil
}

func parsePart(part string, lo, hi int, aliases map[string]int, set map[int]bool) error {
	part = strings.TrimSpace(part)
	if part == "" {
		return errors.New("empty list element")
	}
	step := 1
	if i := strings.Index(part, "/"); i >= 0 {
		s, err := atoi(part[i+1:])
		if err != nil || s <= 0 {
			return fmt.Errorf("invalid step %q", part[i+1:])
		}
		step = s
		part = part[:i]
	}
	startVal, endVal := lo, hi
	switch {
	case part == "*":
		// keep range = full
	case strings.Contains(part, "-"):
		bounds := strings.SplitN(part, "-", 2)
		s, err := resolveValue(bounds[0], lo, hi, aliases)
		if err != nil {
			return err
		}
		e, err := resolveValue(bounds[1], lo, hi, aliases)
		if err != nil {
			return err
		}
		if s > e {
			return fmt.Errorf("range start %d > end %d", s, e)
		}
		startVal, endVal = s, e
	default:
		v, err := resolveValue(part, lo, hi, aliases)
		if err != nil {
			return err
		}
		startVal, endVal = v, v
	}
	for v := startVal; v <= endVal; v += step {
		set[v] = true
	}
	return nil
}

func resolveValue(s string, lo, hi int, aliases map[string]int) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty value")
	}
	if aliases != nil {
		if v, ok := aliases[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	v, err := atoi(s)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", s)
	}
	if v < lo || v > hi {
		return 0, fmt.Errorf("value %d out of range [%d,%d]", v, lo, hi)
	}
	return v, nil
}

// atoi is a tiny non-allocating decimal parser. Standard library would
// work too; this keeps the cron parser dependency-free at the package
// level (it's already used by parse.go which has its own small lex).
func atoi(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty integer")
	}
	n := 0
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch < '0' || ch > '9' {
			return 0, fmt.Errorf("non-digit %q", string(ch))
		}
		n = n*10 + int(ch-'0')
	}
	return n, nil
}
