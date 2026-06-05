package gateway_test

import (
	"testing"

	"github.com/georgebuilds/carlos/internal/gateway"
)

func TestSource_Valid(t *testing.T) {
	cases := []struct {
		s    gateway.Source
		want bool
	}{
		{gateway.SourceNtfy, true},
		{gateway.SourceTelegram, true},
		{gateway.SourceSignal, true},
		{gateway.SourceCustom, true},
		{gateway.SourceFake, true},
		{gateway.Source(""), false},
		{gateway.Source("unknown"), false},
	}
	for _, tc := range cases {
		if got := tc.s.Valid(); got != tc.want {
			t.Errorf("Source(%q).Valid() = %v want %v", tc.s, got, tc.want)
		}
	}
}

func TestUrgency_String_RoundTrip(t *testing.T) {
	for _, u := range []gateway.Urgency{gateway.UrgencyLow, gateway.UrgencyDefault, gateway.UrgencyHigh} {
		got := gateway.ParseUrgency(u.String())
		if got != u {
			t.Errorf("Urgency round trip: %v -> %q -> %v", u, u.String(), got)
		}
	}
}

func TestParseUrgency_UnknownFallsBackToDefault(t *testing.T) {
	for _, in := range []string{"", "lol", "URGENT", "0"} {
		if got := gateway.ParseUrgency(in); got != gateway.UrgencyDefault {
			t.Errorf("ParseUrgency(%q) = %v want UrgencyDefault", in, got)
		}
	}
}

func TestDecisionKind_Valid(t *testing.T) {
	for _, d := range []gateway.DecisionKind{gateway.DecisionApprove, gateway.DecisionRevise, gateway.DecisionReject} {
		if !d.Valid() {
			t.Errorf("DecisionKind(%q).Valid() = false", d)
		}
	}
	for _, d := range []gateway.DecisionKind{"", "yes", "no", "maybe"} {
		if d.Valid() {
			t.Errorf("DecisionKind(%q).Valid() = true want false", d)
		}
	}
}
