package tools

import (
	"testing"
	"time"
)

// TestParseDuration_DayShorthand — the Obsidian-style `Nd` form translates
// to N*24h; standard Go forms still parse; a malformed `d`-suffixed value
// falls through to the standard parser (and errors).
func TestParseDuration_DayShorthand(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"7d", 7 * 24 * time.Hour, false},
		{"1d", 24 * time.Hour, false},
		{"24h", 24 * time.Hour, false},
		{"90m", 90 * time.Minute, false},
		{"xd", 0, true}, // non-numeric prefix -> standard parser -> error
		{"5x", 0, true}, // not a valid duration
	}
	for _, c := range cases {
		got, err := parseDuration(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("parseDuration(%q) want error, got %v", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseDuration(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseDuration(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
