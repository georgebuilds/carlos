package theme

import "testing"

func envWith(vals map[string]string) func(string) string {
	return func(k string) string { return vals[k] }
}

// TestReducedMotion_BooleanSemantics: like NO_COLOR, any non-empty
// value (even "0") means on; empty/unset means off.
func TestReducedMotion_BooleanSemantics(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false},
		{"1", true},
		{"true", true},
		{"0", true}, // boolean-by-presence, per the NO_COLOR convention
	}
	for _, tc := range cases {
		got := ReducedMotion(envWith(map[string]string{"PREFERS_REDUCED_MOTION": tc.val}))
		if got != tc.want {
			t.Errorf("PREFERS_REDUCED_MOTION=%q: got %v, want %v", tc.val, got, tc.want)
		}
	}
}

// TestReducedMotion_NilEnvReadsProcess: nil hook falls back to
// os.Getenv.
func TestReducedMotion_NilEnvReadsProcess(t *testing.T) {
	t.Setenv("PREFERS_REDUCED_MOTION", "yes")
	if !ReducedMotion(nil) {
		t.Error("nil env hook should read the process environment")
	}
	t.Setenv("PREFERS_REDUCED_MOTION", "")
	if ReducedMotion(nil) {
		t.Error("empty value must read as off")
	}
}

// TestReducedMotion_IndependentOfNoColor: motion and color are
// separate accessibility axes - neither variable implies the other.
func TestReducedMotion_IndependentOfNoColor(t *testing.T) {
	// NO_COLOR alone does not reduce motion.
	if ReducedMotion(envWith(map[string]string{"NO_COLOR": "1"})) {
		t.Error("NO_COLOR must not imply reduced motion")
	}
	// PREFERS_REDUCED_MOTION alone does not strip color.
	p := Load(Options{Env: envWith(map[string]string{"PREFERS_REDUCED_MOTION": "1"})})
	if p.NoColor {
		t.Error("PREFERS_REDUCED_MOTION must not imply NO_COLOR")
	}
	if p.Accent == "" {
		t.Error("palette should stay colored under reduced motion")
	}
}
