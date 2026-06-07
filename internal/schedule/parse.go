package schedule

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ParseNatural converts an English schedule phrase into a Schedule.
// The supported grammar (case-insensitive, whitespace-tolerant) is:
//
//   - "every weekday at 9am"          → 0 9 * * 1-5
//   - "every weekday morning"         → 0 9 * * 1-5  (morning := 9am)
//   - "every monday at noon"          → 0 12 * * 1
//   - "every <day> at <time>"         → 0 H * * D
//   - "every 30 minutes"              → */30 * * * *
//   - "every N minutes"               → */N * * * *
//   - "every hour"                    → 0 * * * *
//   - "every N hours"                 → 0 */N * * *
//   - "daily at <time>"               → M H * * *
//   - "every day at <time>"           → M H * * *
//   - "every <day> at <time>"         → M H * * D
//   - "tomorrow at <time>"            → M H D Mo *  + Once=true
//
// `<time>` accepts:
//   - "9am" / "9pm"
//   - "9:30am"
//   - "noon" (12:00), "midnight" (00:00)
//   - "17:00" (24-hour H:M)
//
// `<day>` accepts the full English names of weekdays (monday..sunday)
// and "weekday" (= mon-fri) / "weekend" (= sat,sun).
//
// Anything that doesn't match the natural-language forms is fed to
// ParseCron as a fallback so a power user can paste "0 9 * * 1-5"
// directly. The fallback's error is surfaced as the final error.
//
// The Schedule returned has Name unset - caller supplies it (the
// /schedule add handler defaults to a slugified prefix of the prompt
// + a timestamp suffix; tests pass an explicit name).
func ParseNatural(s string) (Schedule, error) {
	raw := strings.TrimSpace(s)
	if raw == "" {
		return Schedule{}, errors.New("schedule: empty natural-language input")
	}
	lower := strings.ToLower(raw)

	// Try each form in order. The first one that matches wins.
	if sch, ok, err := tryEveryNMinutes(lower); ok {
		return sch, err
	}
	if sch, ok, err := tryEveryNHours(lower); ok {
		return sch, err
	}
	if sch, ok, err := tryEveryHour(lower); ok {
		return sch, err
	}
	if sch, ok, err := tryDaily(lower); ok {
		return sch, err
	}
	if sch, ok, err := tryEveryDay(lower); ok {
		return sch, err
	}
	if sch, ok, err := tryEveryWeekdayMorning(lower); ok {
		return sch, err
	}
	if sch, ok, err := tryEveryDayName(lower); ok {
		return sch, err
	}
	if sch, ok, err := tryTomorrow(lower, time.Now()); ok {
		return sch, err
	}

	// Fallback: treat as a raw cron expression.
	if _, err := ParseCron(raw); err == nil {
		return Schedule{Spec: raw}, nil
	}
	return Schedule{}, fmt.Errorf("schedule: cannot parse %q (expected an English phrase like \"every weekday at 9am\" or a 5-field cron expression)", raw)
}

// --- Pattern matchers --------------------------------------------------

var reEveryNMinutes = regexp.MustCompile(`^every\s+(\d+)\s*(minute|minutes|min|mins)$`)

func tryEveryNMinutes(s string) (Schedule, bool, error) {
	m := reEveryNMinutes.FindStringSubmatch(s)
	if m == nil {
		return Schedule{}, false, nil
	}
	n, err := atoi(m[1])
	if err != nil || n <= 0 || n > 59 {
		return Schedule{}, true, fmt.Errorf("every N minutes: N must be 1..59, got %q", m[1])
	}
	return Schedule{Spec: fmt.Sprintf("*/%d * * * *", n)}, true, nil
}

var reEveryNHours = regexp.MustCompile(`^every\s+(\d+)\s*(hour|hours|hr|hrs)$`)

func tryEveryNHours(s string) (Schedule, bool, error) {
	m := reEveryNHours.FindStringSubmatch(s)
	if m == nil {
		return Schedule{}, false, nil
	}
	n, err := atoi(m[1])
	if err != nil || n <= 0 || n > 23 {
		return Schedule{}, true, fmt.Errorf("every N hours: N must be 1..23, got %q", m[1])
	}
	return Schedule{Spec: fmt.Sprintf("0 */%d * * *", n)}, true, nil
}

func tryEveryHour(s string) (Schedule, bool, error) {
	if s == "every hour" || s == "hourly" {
		return Schedule{Spec: "0 * * * *"}, true, nil
	}
	return Schedule{}, false, nil
}

var reDaily = regexp.MustCompile(`^daily\s+at\s+(.+)$`)

func tryDaily(s string) (Schedule, bool, error) {
	m := reDaily.FindStringSubmatch(s)
	if m == nil {
		return Schedule{}, false, nil
	}
	h, min, err := parseTime(m[1])
	if err != nil {
		return Schedule{}, true, fmt.Errorf("daily at: %w", err)
	}
	return Schedule{Spec: fmt.Sprintf("%d %d * * *", min, h)}, true, nil
}

var reEveryDay = regexp.MustCompile(`^every\s+day\s+at\s+(.+)$`)

func tryEveryDay(s string) (Schedule, bool, error) {
	m := reEveryDay.FindStringSubmatch(s)
	if m == nil {
		return Schedule{}, false, nil
	}
	h, min, err := parseTime(m[1])
	if err != nil {
		return Schedule{}, true, fmt.Errorf("every day at: %w", err)
	}
	return Schedule{Spec: fmt.Sprintf("%d %d * * *", min, h)}, true, nil
}

// "every weekday morning" / "every weekend morning" - uses 9am as the
// fixture for "morning". A future slice can let the user override the
// morning anchor via config.
func tryEveryWeekdayMorning(s string) (Schedule, bool, error) {
	switch s {
	case "every weekday morning":
		return Schedule{Spec: "0 9 * * 1-5"}, true, nil
	case "every weekend morning":
		return Schedule{Spec: "0 9 * * 0,6"}, true, nil
	}
	return Schedule{}, false, nil
}

// "every weekday at 9am" / "every monday at noon" / "every weekend at 5pm".
var reEveryDayName = regexp.MustCompile(`^every\s+(monday|tuesday|wednesday|thursday|friday|saturday|sunday|weekday|weekdays|weekend|weekends|mon|tue|wed|thu|fri|sat|sun)\s+at\s+(.+)$`)

func tryEveryDayName(s string) (Schedule, bool, error) {
	m := reEveryDayName.FindStringSubmatch(s)
	if m == nil {
		return Schedule{}, false, nil
	}
	h, min, err := parseTime(m[2])
	if err != nil {
		return Schedule{}, true, fmt.Errorf("every %s at: %w", m[1], err)
	}
	dow, err := dayNameToCronField(m[1])
	if err != nil {
		return Schedule{}, true, err
	}
	return Schedule{Spec: fmt.Sprintf("%d %d * * %s", min, h, dow)}, true, nil
}

// "tomorrow at 3pm" - one-shot. Resolved against `now` so tests can
// inject a fixed clock; production callers pass time.Now().
var reTomorrow = regexp.MustCompile(`^tomorrow\s+at\s+(.+)$`)

func tryTomorrow(s string, now time.Time) (Schedule, bool, error) {
	m := reTomorrow.FindStringSubmatch(s)
	if m == nil {
		return Schedule{}, false, nil
	}
	h, min, err := parseTime(m[1])
	if err != nil {
		return Schedule{}, true, fmt.Errorf("tomorrow at: %w", err)
	}
	t := now.Local().AddDate(0, 0, 1)
	return Schedule{
		Spec: fmt.Sprintf("%d %d %d %d *", min, h, t.Day(), int(t.Month())),
		Once: true,
	}, true, nil
}

// --- Time + day helpers ------------------------------------------------

// parseTime parses one of:
//
//	"9am" / "9pm" / "12am" / "12pm"
//	"9:30am" / "9:30pm"
//	"noon" / "midnight"
//	"17:00" / "07:30"
//	"7" (bare hour; treated as 24h e.g. "7" = 7am)
//
// Returns (hour 0..23, minute 0..59).
func parseTime(raw string) (int, int, error) {
	t := strings.TrimSpace(strings.ToLower(raw))
	switch t {
	case "noon":
		return 12, 0, nil
	case "midnight":
		return 0, 0, nil
	}
	// am/pm form: "9am" / "9:30pm" / "12pm".
	if strings.HasSuffix(t, "am") || strings.HasSuffix(t, "pm") {
		pm := strings.HasSuffix(t, "pm")
		body := strings.TrimSuffix(strings.TrimSuffix(t, "am"), "pm")
		body = strings.TrimSpace(body)
		h, m, err := splitHourMinute(body)
		if err != nil {
			return 0, 0, err
		}
		if h < 1 || h > 12 {
			return 0, 0, fmt.Errorf("12-hour clock: hour must be 1..12, got %d", h)
		}
		if h == 12 {
			h = 0 // 12am = 00, 12pm = 12 (handled by the +12 below)
		}
		if pm {
			h += 12
		}
		return h, m, nil
	}
	// 24-hour form: "17:00", "7:30", or bare hour "7".
	h, m, err := splitHourMinute(t)
	if err != nil {
		return 0, 0, err
	}
	if h > 23 {
		return 0, 0, fmt.Errorf("24-hour clock: hour must be 0..23, got %d", h)
	}
	return h, m, nil
}

func splitHourMinute(s string) (int, int, error) {
	if s == "" {
		return 0, 0, errors.New("empty time")
	}
	if i := strings.Index(s, ":"); i >= 0 {
		h, err := atoi(s[:i])
		if err != nil {
			return 0, 0, fmt.Errorf("hour: %w", err)
		}
		m, err := atoi(s[i+1:])
		if err != nil {
			return 0, 0, fmt.Errorf("minute: %w", err)
		}
		if m < 0 || m > 59 {
			return 0, 0, fmt.Errorf("minute %d out of range 0..59", m)
		}
		return h, m, nil
	}
	h, err := atoi(s)
	if err != nil {
		return 0, 0, fmt.Errorf("hour: %w", err)
	}
	return h, 0, nil
}

// dayNameToCronField translates a day-name token into its cron field
// representation:
//
//	monday..sunday → 1..0
//	mon..sun       → 1..0
//	weekday(s)     → 1-5
//	weekend(s)     → 0,6
func dayNameToCronField(name string) (string, error) {
	switch strings.ToLower(name) {
	case "weekday", "weekdays":
		return "1-5", nil
	case "weekend", "weekends":
		return "0,6", nil
	case "sunday", "sun":
		return "0", nil
	case "monday", "mon":
		return "1", nil
	case "tuesday", "tue":
		return "2", nil
	case "wednesday", "wed":
		return "3", nil
	case "thursday", "thu":
		return "4", nil
	case "friday", "fri":
		return "5", nil
	case "saturday", "sat":
		return "6", nil
	}
	return "", fmt.Errorf("unrecognized day name %q", name)
}
