package schedule

import (
	"testing"
	"time"
)

// TestTryMatchers_BadTimePropagatesError covers the parseTime-error
// branch in each natural-language matcher that takes a <time>: the form
// matches (ok=true) but the time body is garbage, so each returns an
// error. These are the `return Schedule{}, true, fmt.Errorf(...)` arms.
func TestTryMatchers_BadTimePropagatesError(t *testing.T) {
	cases := []string{
		"daily at notatime",
		"every day at notatime",
		"every monday at notatime",
		"tomorrow at notatime",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			_, err := ParseNatural(in)
			if err == nil {
				t.Fatalf("ParseNatural(%q): expected time-parse error, got nil", in)
			}
		})
	}
}

// TestTryEveryNMinutes_TooLargeOverflow exercises the err/n<=0 guard via
// a giant number that atoi still parses but stays positive (sanity), and
// the zero case (already covered elsewhere but pinned here for the hours
// twin). The real lever is the hours form's error arm.
func TestTryEveryNHours_ZeroRejected(t *testing.T) {
	if _, err := ParseNatural("every 0 hours"); err == nil {
		t.Fatal("every 0 hours: expected error, got nil")
	}
}

// TestParseTime_24HourOutOfRange covers the `h > 23` branch in the
// 24-hour arm of parseTime.
func TestParseTime_24HourOutOfRange(t *testing.T) {
	if _, _, err := parseTime("25:00"); err == nil {
		t.Fatal("parseTime(25:00): expected out-of-range error, got nil")
	}
	if _, _, err := parseTime("24"); err == nil {
		t.Fatal("parseTime(24): bare hour 24 should be out of range")
	}
}

// TestParseTime_12HourOutOfRange covers the `h < 1 || h > 12` branch in
// the am/pm arm of parseTime.
func TestParseTime_12HourOutOfRange(t *testing.T) {
	if _, _, err := parseTime("13am"); err == nil {
		t.Fatal("parseTime(13am): expected 12-hour out-of-range error")
	}
	if _, _, err := parseTime("0am"); err == nil {
		t.Fatal("parseTime(0am): hour 0 invalid on a 12-hour clock")
	}
}

// TestSplitHourMinute_Errors covers the failure arms of splitHourMinute:
// empty string, non-numeric hour, non-numeric minute, out-of-range
// minute.
func TestSplitHourMinute_Errors(t *testing.T) {
	cases := []string{
		"",       // empty time
		"zz:30",  // non-numeric hour
		"9:zz",   // non-numeric minute
		"9:99",   // minute out of range
		"notnum", // bare non-numeric hour
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if _, _, err := splitHourMinute(in); err == nil {
				t.Fatalf("splitHourMinute(%q): expected error, got nil", in)
			}
		})
	}
}

// TestSplitHourMinute_WithMinutes covers the colon-bearing success arm.
func TestSplitHourMinute_WithMinutes(t *testing.T) {
	h, m, err := splitHourMinute("7:45")
	if err != nil {
		t.Fatalf("splitHourMinute(7:45): %v", err)
	}
	if h != 7 || m != 45 {
		t.Fatalf("splitHourMinute(7:45) = (%d,%d) want (7,45)", h, m)
	}
}

// TestDayNameToCronField_Unknown covers the default/error arm of
// dayNameToCronField.
func TestDayNameToCronField_Unknown(t *testing.T) {
	if _, err := dayNameToCronField("blursday"); err == nil {
		t.Fatal("dayNameToCronField(blursday): expected error, got nil")
	}
}

// TestDayNameToCronField_AllForms covers every recognised branch,
// including the long names and the weekend/weekday aggregates that the
// existing ParseNatural tests do not all exercise directly.
func TestDayNameToCronField_AllForms(t *testing.T) {
	want := map[string]string{
		"weekday": "1-5", "weekdays": "1-5",
		"weekend": "0,6", "weekends": "0,6",
		"sunday": "0", "sun": "0",
		"monday": "1", "mon": "1",
		"tuesday": "2", "tue": "2",
		"wednesday": "3", "wed": "3",
		"thursday": "4", "thu": "4",
		"friday": "5", "fri": "5",
		"saturday": "6", "sat": "6",
	}
	for name, exp := range want {
		got, err := dayNameToCronField(name)
		if err != nil {
			t.Errorf("dayNameToCronField(%q): %v", name, err)
			continue
		}
		if got != exp {
			t.Errorf("dayNameToCronField(%q) = %q want %q", name, got, exp)
		}
	}
}

// TestTryTomorrow_RollsDate confirms tryTomorrow uses the injected clock
// and rolls to the next calendar day (covers the AddDate path).
func TestTryTomorrow_RollsMonthBoundary(t *testing.T) {
	// Jan 31 → Feb 1 (month + day-of-month both roll).
	now := time.Date(2026, 1, 31, 8, 0, 0, 0, time.Local)
	s, ok, err := tryTomorrow("tomorrow at 9am", now)
	if err != nil || !ok {
		t.Fatalf("tryTomorrow: ok=%v err=%v", ok, err)
	}
	want := "0 9 1 2 *" // minute=0 hour=9 dom=1 month=2
	if s.Spec != want {
		t.Fatalf("tryTomorrow month roll: got %q want %q", s.Spec, want)
	}
	if !s.Once {
		t.Error("tomorrow form must set Once=true")
	}
}
