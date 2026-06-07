package manage

import (
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
)

// TestDecStr1M_FormatsLargeCounts covers the >=1M token-formatter
// branch (formatTokens delegates here when n >= 1_000_000).
func TestDecStr1M_FormatsLargeCounts(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{1_000_000, "1.0M"},
		{1_200_000, "1.2M"},
		{12_300_000, "12.3M"},
		{99_900_000, "99.9M"},
	}
	for _, c := range cases {
		if got := decStr1M(c.n); got != c.want {
			t.Errorf("decStr1M(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// TestFormatTokens_BranchCoverage walks the four ranges so every
// branch in the switch fires.
func TestFormatTokens_BranchCoverage(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{500, "500"},
		{1_500, "1.5k"},
		{12_345, "12k"},
		{2_500_000, "2.5M"},
	}
	for _, c := range cases {
		if got := formatTokens(c.n); got != c.want {
			t.Errorf("formatTokens(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

// TestFormatTokensColumn_Combines pairs in and out.
func TestFormatTokensColumn_Combines(t *testing.T) {
	got := formatTokensColumn(1_500, 2_500_000)
	if got != "1.5k/2.5M" {
		t.Errorf("formatTokensColumn = %q, want '1.5k/2.5M'", got)
	}
}

// TestFormatCost_ClampsNegative confirms negative costs render as $0.00
// rather than a confusing dollar string.
func TestFormatCost_ClampsNegative(t *testing.T) {
	cases := []struct {
		cents int64
		want  string
	}{
		{-100, "$0.00"},
		{0, "$0.00"},
		{5, "$0.05"},
		{100, "$1.00"},
		{1234, "$12.34"},
	}
	for _, c := range cases {
		if got := formatCost(c.cents); got != c.want {
			t.Errorf("formatCost(%d) = %q, want %q", c.cents, got, c.want)
		}
	}
}

// TestIntStr_NegativePath covers the negative branch which the other
// callers can't reach.
func TestIntStr_NegativePath(t *testing.T) {
	if got := intStr(-42); got != "-42" {
		t.Errorf("intStr(-42) = %q, want '-42'", got)
	}
	if got := intStr(0); got != "0" {
		t.Errorf("intStr(0) = %q", got)
	}
}

// TestZeropad2_PadsBelowTen covers the <10 padding branch + the
// negative-clamp guard.
func TestZeropad2_PadsBelowTen(t *testing.T) {
	if got := zeropad2(-3); got != "00" {
		t.Errorf("zeropad2(-3) = %q, want '00'", got)
	}
	if got := zeropad2(5); got != "05" {
		t.Errorf("zeropad2(5) = %q, want '05'", got)
	}
	if got := zeropad2(42); got != "42" {
		t.Errorf("zeropad2(42) = %q, want '42'", got)
	}
}

// TestRenderSparkline_NilRingProducesBaseline confirms a nil ring
// degrades to the baseline glyph block instead of crashing.
func TestRenderSparkline_NilRingProducesBaseline(t *testing.T) {
	out := RenderSparkline(nil, agent.StateRunning)
	if !strings.ContainsRune(out, sparkBlocks[0]) {
		t.Errorf("nil-ring spark = %q, want baseline glyph", out)
	}
}

// TestTokenRing_AddIgnoresNegative is a small invariant: Add(-N) is
// a no-op so a buggy event can't drain the ring counter.
func TestTokenRing_AddIgnoresNegative(t *testing.T) {
	r := &TokenRing{}
	r.Add(50)
	r.Add(-1000)

	var sum int64
	for _, v := range r.Snapshot() {
		sum += v
	}
	if sum != 50 {
		t.Errorf("ring sum after Add(-N) = %d, want 50", sum)
	}
}
