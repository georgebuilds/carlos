package theme

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// fakeEnv builds an Options.Env closure from a plain map. Missing keys
// return the empty string — same shape as os.Getenv.
func fakeEnv(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoad_NoColor_ZeroesEverySlot(t *testing.T) {
	p := Load(Options{Env: fakeEnv(map[string]string{"NO_COLOR": "1"})})
	if !p.NoColor {
		t.Fatal("Load with NO_COLOR=1: want NoColor=true")
	}
	checks := []struct {
		name string
		c    lipgloss.Color
	}{
		{"Accent", p.Accent},
		{"Muted", p.Muted},
		{"User", p.User},
		{"Agent", p.Agent},
		{"Tool", p.Tool},
		{"Warn", p.Warn},
		{"OK", p.OK},
		{"Subtle", p.Subtle},
		{"Brand", p.Brand},
		{"Cyan", p.Cyan},
		{"ErrHi", p.ErrHi},
	}
	for _, ch := range checks {
		if string(ch.c) != "" {
			t.Errorf("NO_COLOR: slot %s = %q, want empty", ch.name, string(ch.c))
		}
	}
}

func TestLoad_NoColor_AnyValueTriggers(t *testing.T) {
	// no-color.org spec: any non-empty value means monochrome. "0"
	// included — surprising, but that's the standard.
	for _, v := range []string{"1", "true", "0", "yes", "anything"} {
		p := Load(Options{Env: fakeEnv(map[string]string{"NO_COLOR": v})})
		if !p.NoColor {
			t.Errorf("NO_COLOR=%q: want NoColor=true", v)
		}
	}
}

func TestLoad_NoColor_PreservesVariant(t *testing.T) {
	// Even in monochrome mode, the variant is still detected so
	// renderers can pick layout differences for light vs dark.
	p := Load(Options{
		ForcedVariant: "light",
		Env:           fakeEnv(map[string]string{"NO_COLOR": "1"}),
	})
	if p.Variant != Light {
		t.Errorf("NO_COLOR + forced light: variant = %v, want Light", p.Variant)
	}
}

func TestLoad_COLORFGBG_DarkBackground(t *testing.T) {
	// "15;0" — white-on-black. BG=0 is dark.
	p := Load(Options{Env: fakeEnv(map[string]string{"COLORFGBG": "15;0"})})
	if p.Variant != Dark {
		t.Errorf("COLORFGBG=15;0: variant = %v, want Dark", p.Variant)
	}
}

func TestLoad_COLORFGBG_LightBackground(t *testing.T) {
	// "0;15" — black-on-white. BG=15 is light.
	p := Load(Options{Env: fakeEnv(map[string]string{"COLORFGBG": "0;15"})})
	if p.Variant != Light {
		t.Errorf("COLORFGBG=0;15: variant = %v, want Light", p.Variant)
	}
}

func TestLoad_COLORFGBG_ThreeFieldForm(t *testing.T) {
	// Some terminals (rxvt) emit "FG;_;BG". The BG is the last field.
	p := Load(Options{Env: fakeEnv(map[string]string{"COLORFGBG": "0;default;15"})})
	if p.Variant != Light {
		t.Errorf("COLORFGBG=0;default;15: variant = %v, want Light", p.Variant)
	}
}

func TestLoad_COLORFGBG_Unknown_DefaultsDark(t *testing.T) {
	for _, raw := range []string{"", "garbage", "99;42", "x;y"} {
		p := Load(Options{Env: fakeEnv(map[string]string{"COLORFGBG": raw})})
		if p.Variant != Dark {
			t.Errorf("COLORFGBG=%q: variant = %v, want Dark", raw, p.Variant)
		}
	}
}

func TestLoad_ForcedVariant_OverridesEnv(t *testing.T) {
	// ForcedVariant must beat COLORFGBG.
	p := Load(Options{
		ForcedVariant: "light",
		Env:           fakeEnv(map[string]string{"COLORFGBG": "15;0"}),
	})
	if p.Variant != Light {
		t.Errorf("ForcedVariant=light, COLORFGBG=15;0: variant = %v, want Light", p.Variant)
	}

	p = Load(Options{
		ForcedVariant: "dark",
		Env:           fakeEnv(map[string]string{"COLORFGBG": "0;15"}),
	})
	if p.Variant != Dark {
		t.Errorf("ForcedVariant=dark, COLORFGBG=0;15: variant = %v, want Dark", p.Variant)
	}
}

func TestLoad_ForcedVariant_CaseInsensitive(t *testing.T) {
	p := Load(Options{ForcedVariant: "  LIGHT  ", Env: fakeEnv(nil)})
	if p.Variant != Light {
		t.Errorf("ForcedVariant=' LIGHT ': variant = %v, want Light", p.Variant)
	}
}

func TestLoad_ForcedVariant_UnknownFallsThrough(t *testing.T) {
	// Unknown forced value should not block COLORFGBG detection.
	p := Load(Options{
		ForcedVariant: "neon",
		Env:           fakeEnv(map[string]string{"COLORFGBG": "0;15"}),
	})
	if p.Variant != Light {
		t.Errorf("unknown ForcedVariant + light COLORFGBG: variant = %v, want Light", p.Variant)
	}
}

func TestLoad_AccentOverride_HexSix(t *testing.T) {
	p := Load(Options{AccentOverride: "#ff0000", Env: fakeEnv(nil)})
	if p.Accent != lipgloss.Color("#ff0000") {
		t.Errorf("AccentOverride=#ff0000: Accent = %q, want #ff0000", string(p.Accent))
	}
	// Other slots untouched.
	if p.Muted != darkPalette().Muted {
		t.Errorf("AccentOverride must not change Muted; got %q", string(p.Muted))
	}
}

func TestLoad_AccentOverride_HexThree(t *testing.T) {
	p := Load(Options{AccentOverride: "#f0a", Env: fakeEnv(nil)})
	if p.Accent != lipgloss.Color("#f0a") {
		t.Errorf("AccentOverride=#f0a: Accent = %q, want #f0a", string(p.Accent))
	}
}

func TestLoad_AccentOverride_AnsiIndex(t *testing.T) {
	p := Load(Options{AccentOverride: "129", Env: fakeEnv(nil)})
	if p.Accent != lipgloss.Color("129") {
		t.Errorf("AccentOverride=129: Accent = %q, want 129", string(p.Accent))
	}
}

func TestLoad_AccentOverride_Malformed_KeepsDefault(t *testing.T) {
	def := darkPalette().Accent
	for _, raw := range []string{"#zz", "#1234", "#abcd", "999", "blue", "-3"} {
		p := Load(Options{AccentOverride: raw, Env: fakeEnv(nil)})
		if p.Accent != def {
			t.Errorf("AccentOverride=%q: Accent = %q, want default %q", raw, string(p.Accent), string(def))
		}
	}
}

func TestLoad_DarkDefault_MatchesLegacyLiterals(t *testing.T) {
	// Visual regression guard. The legacy chat/onboarding/manage
	// palettes used these exact literals; the dark default must keep
	// matching so centralization is a no-op for existing users.
	p := Load(Options{Env: fakeEnv(nil)})
	want := map[string]lipgloss.Color{
		"Accent": "#4a6bd6",
		"Muted":  "240",
		"User":   "#7fb3ff",
		"Agent":  "252",
		"Tool":   "214",
		"Warn":   "203",
		"OK":     "34",
		"Subtle": "244",
		"Brand":  "#1d2a4d",
	}
	got := map[string]lipgloss.Color{
		"Accent": p.Accent, "Muted": p.Muted, "User": p.User,
		"Agent": p.Agent, "Tool": p.Tool, "Warn": p.Warn,
		"OK": p.OK, "Subtle": p.Subtle, "Brand": p.Brand,
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("dark default %s = %q, want %q", k, string(got[k]), string(w))
		}
	}
}

func TestLoad_LightDefault_DiffersFromDark(t *testing.T) {
	// Sanity — the light palette is not silently equal to dark.
	d := darkPalette()
	l := lightPalette()
	if d.Accent == l.Accent {
		t.Error("light vs dark Accent should differ")
	}
	if d.Agent == l.Agent {
		t.Error("light vs dark Agent should differ")
	}
}

func TestVariant_String(t *testing.T) {
	if Dark.String() != "dark" {
		t.Errorf("Dark.String() = %q, want dark", Dark.String())
	}
	if Light.String() != "light" {
		t.Errorf("Light.String() = %q, want light", Light.String())
	}
}

func TestLoad_NilEnv_UsesOSGetenv(t *testing.T) {
	// Smoke — Options with nil Env must not panic. Result depends on
	// the real process env so we only assert the call completed.
	_ = Load(Options{})
}
