package onboarding

import "testing"

// clearProtocolEnv zeroes every env var DetectProtocol consults so each
// sub-case starts from a known-empty terminal and only the variables it
// sets influence the outcome. t.Setenv restores originals at cleanup.
func clearProtocolEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"CARLOS_PORTRAIT_PROTOCOL",
		"KITTY_WINDOW_ID",
		"TERM",
		"TERM_PROGRAM",
		"GHOSTTY_RESOURCES_DIR",
		"WEZTERM_PANE",
		"COLORTERM",
	} {
		t.Setenv(k, "")
	}
}

// TestDetectProtocol_ForcedOverrides walks every value the
// CARLOS_PORTRAIT_PROTOCOL escape hatch accepts, including the alias
// spellings for half-block.
func TestDetectProtocol_ForcedOverrides(t *testing.T) {
	cases := map[string]RenderProtocol{
		"kitty":              ProtoKitty,
		"iterm2":             ProtoITerm2,
		"wezterm":            ProtoWezTerm,
		"sixel":              ProtoSixel,
		"half-block":         ProtoUnicodeHalfBlock,
		"unicode-half-block": ProtoUnicodeHalfBlock,
		"halfblock":          ProtoUnicodeHalfBlock,
		"ascii":              ProtoASCII,
	}
	for val, want := range cases {
		t.Run(val, func(t *testing.T) {
			clearProtocolEnv(t)
			t.Setenv("CARLOS_PORTRAIT_PROTOCOL", val)
			if got := DetectProtocol(); got != want {
				t.Errorf("forced %q: got %s want %s", val, got, want)
			}
		})
	}
}

// TestDetectProtocol_ForcedUnknownFallsThrough verifies an unrecognized
// forced value doesn't short-circuit; detection continues to the env
// cascade (here landing on ASCII because no other signal is set).
func TestDetectProtocol_ForcedUnknownFallsThrough(t *testing.T) {
	clearProtocolEnv(t)
	t.Setenv("CARLOS_PORTRAIT_PROTOCOL", "bogus-protocol")
	if got := DetectProtocol(); got != ProtoASCII {
		t.Errorf("bogus forced value should fall through to the cascade (ascii here); got %s", got)
	}
}

// TestDetectProtocol_KittyWindowID exercises the KITTY_WINDOW_ID signal.
func TestDetectProtocol_KittyWindowID(t *testing.T) {
	clearProtocolEnv(t)
	t.Setenv("KITTY_WINDOW_ID", "1")
	if got := DetectProtocol(); got != ProtoKitty {
		t.Errorf("KITTY_WINDOW_ID set: got %s want kitty", got)
	}
}

// TestDetectProtocol_KittyTERM exercises the TERM=xterm-kitty fallback
// path (multiplexer stripped the env var).
func TestDetectProtocol_KittyTERM(t *testing.T) {
	clearProtocolEnv(t)
	t.Setenv("TERM", "xterm-kitty")
	if got := DetectProtocol(); got != ProtoKitty {
		t.Errorf("TERM=xterm-kitty: got %s want kitty", got)
	}
}

// TestDetectProtocol_WezTerm verifies the WEZTERM_PANE signal returns
// the distinct ProtoWezTerm value (telemetry attribution) rather than
// collapsing to kitty.
func TestDetectProtocol_WezTerm(t *testing.T) {
	clearProtocolEnv(t)
	t.Setenv("WEZTERM_PANE", "0")
	if got := DetectProtocol(); got != ProtoWezTerm {
		t.Errorf("WEZTERM_PANE: got %s want wezterm", got)
	}
}

// TestDetectProtocol_ITerm2 covers the canonical iTerm2 signal.
func TestDetectProtocol_ITerm2(t *testing.T) {
	clearProtocolEnv(t)
	t.Setenv("TERM_PROGRAM", "iTerm.app")
	if got := DetectProtocol(); got != ProtoITerm2 {
		t.Errorf("TERM_PROGRAM=iTerm.app: got %s want iterm2", got)
	}
}

// TestDetectProtocol_SixelTerms covers the sixel TERM allow-list.
func TestDetectProtocol_SixelTerms(t *testing.T) {
	for _, term := range []string{"mlterm", "foot", "foot-extra", "contour", "yaft-256color"} {
		t.Run(term, func(t *testing.T) {
			clearProtocolEnv(t)
			t.Setenv("TERM", term)
			if got := DetectProtocol(); got != ProtoSixel {
				t.Errorf("TERM=%q: got %s want sixel", term, got)
			}
		})
	}
}

// TestDetectProtocol_ColortermHalfBlock covers the COLORTERM truecolor
// advertisements that route to half-block.
func TestDetectProtocol_ColortermHalfBlock(t *testing.T) {
	for _, ct := range []string{"truecolor", "24bit"} {
		t.Run(ct, func(t *testing.T) {
			clearProtocolEnv(t)
			t.Setenv("COLORTERM", ct)
			if got := DetectProtocol(); got != ProtoUnicodeHalfBlock {
				t.Errorf("COLORTERM=%q: got %s want half-block", ct, got)
			}
		})
	}
}

// TestDetectProtocol_GenericTermHalfBlock verifies an ordinary 256-color
// TERM (no graphics signal, no COLORTERM) degrades to half-block, not
// straight to ASCII.
func TestDetectProtocol_GenericTermHalfBlock(t *testing.T) {
	clearProtocolEnv(t)
	t.Setenv("TERM", "xterm-256color")
	if got := DetectProtocol(); got != ProtoUnicodeHalfBlock {
		t.Errorf("TERM=xterm-256color: got %s want half-block", got)
	}
}

// TestDetectProtocol_DumbTermFallsToASCII verifies TERM=dumb (and the
// fully-empty terminal) bottom out at ASCII.
func TestDetectProtocol_DumbTermFallsToASCII(t *testing.T) {
	clearProtocolEnv(t)
	t.Setenv("TERM", "dumb")
	if got := DetectProtocol(); got != ProtoASCII {
		t.Errorf("TERM=dumb: got %s want ascii", got)
	}
}

// TestDetectProtocol_EmptyEnvFallsToASCII covers the all-empty terminal.
func TestDetectProtocol_EmptyEnvFallsToASCII(t *testing.T) {
	clearProtocolEnv(t)
	if got := DetectProtocol(); got != ProtoASCII {
		t.Errorf("empty env: got %s want ascii", got)
	}
}

// TestDetectProtocol_KittyBeatsColorterm pins the cascade priority: a
// kitty signal wins even when COLORTERM would otherwise pick half-block.
func TestDetectProtocol_KittyBeatsColorterm(t *testing.T) {
	clearProtocolEnv(t)
	t.Setenv("KITTY_WINDOW_ID", "7")
	t.Setenv("COLORTERM", "truecolor")
	if got := DetectProtocol(); got != ProtoKitty {
		t.Errorf("kitty should outrank colorterm: got %s", got)
	}
}

// TestRenderProtocolString_Unknown covers the default arm of String for
// an out-of-range protocol value.
func TestRenderProtocolString_Unknown(t *testing.T) {
	if got := RenderProtocol(99).String(); got != "unknown" {
		t.Errorf("out-of-range protocol String(): got %q want %q", got, "unknown")
	}
}
