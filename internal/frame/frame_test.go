package frame

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNewPersonal(t *testing.T) {
	f := NewPersonal("anthropic", "claude-sonnet-4-6")
	if f.Name != DefaultPersonalName {
		t.Errorf("Name = %q, want %q", f.Name, DefaultPersonalName)
	}
	if f.Glyph != DefaultPersonalGlyph {
		t.Errorf("Glyph = %q, want %q", f.Glyph, DefaultPersonalGlyph)
	}
	if f.Accent != DefaultPersonalAccent {
		t.Errorf("Accent = %q, want %q", f.Accent, DefaultPersonalAccent)
	}
	if f.Provider != "anthropic" || f.Model != "claude-sonnet-4-6" {
		t.Errorf("provider/model not threaded: %+v", f)
	}
}

func TestDefaultGlyphFor(t *testing.T) {
	cases := map[string]string{
		"personal": "◉",
		"work":     "▣",
		"research": "◈",
		"writing":  "✦",
		"client":   "⛰",
		"side":     "⛰",
		"ludus":    "⛰",
		"":         "+",
		"foobar":   "+",
	}
	for name, want := range cases {
		if got := DefaultGlyphFor(name); got != want {
			t.Errorf("DefaultGlyphFor(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestIsValidMode(t *testing.T) {
	for _, ok := range []string{ModeSolo, ModeTight, ModeOrchestrator} {
		if !IsValidMode(ok) {
			t.Errorf("IsValidMode(%q) = false, want true", ok)
		}
	}
	for _, no := range []string{"", "auto", "FAST", "ORCHESTRATOR"} {
		if IsValidMode(no) {
			t.Errorf("IsValidMode(%q) = true, want false", no)
		}
	}
}

func TestEffectiveMode(t *testing.T) {
	// Pre-modes / partial / blank-shell frames must surface as
	// orchestrator so the user-visible default matches what onboarding
	// (NewPersonal) writes for a fresh install. Without this flip a
	// fresh-install config that happened to leave Mode empty would
	// report solo via /whoami while the supervisor was actually
	// running with the orchestrator cap from a separate code path —
	// an internally-inconsistent surface we hit in field testing.
	if m := EffectiveMode(Frame{}); m != ModeOrchestrator {
		t.Errorf("zero-frame default = %q, want %q (orchestrator)", m, ModeOrchestrator)
	}
	if m := EffectiveMode(Frame{Mode: "garbage"}); m != ModeOrchestrator {
		t.Errorf("invalid mode should fall back to orchestrator; got %q", m)
	}
	if m := EffectiveMode(Frame{Mode: ModeOrchestrator}); m != ModeOrchestrator {
		t.Errorf("valid mode = %q, want %q", m, ModeOrchestrator)
	}
	// Explicit solo is honoured — the flip is on the FALLBACK, not on
	// the "user explicitly chose solo" path. Guards against accidentally
	// regressing user intent.
	if m := EffectiveMode(Frame{Mode: ModeSolo}); m != ModeSolo {
		t.Errorf("explicit solo should be honoured, not overridden; got %q", m)
	}
}

func TestNewPersonal_DefaultsToOrchestratorMode(t *testing.T) {
	// v0.7.6 flip: the personal frame defaults to orchestrator so the
	// chat-side /agents view and the `agent` delegation tool work out
	// of the box. The previous solo default left a fresh install
	// unable to spawn sub-agents — sub-tasks reported "delegation is
	// off for this frame" with no obvious path to fix it. Solo is
	// still available via `/mode solo` or the frames editor.
	if f := NewPersonal("a", "b"); f.Mode != ModeOrchestrator {
		t.Errorf("NewPersonal Mode = %q, want %q (orchestrator default)", f.Mode, ModeOrchestrator)
	}
	// Cross-check: the spawn cap that the supervisor reads from this
	// mode matches the legacy pre-modes cap of 5 — proves the flip
	// restores the historical behaviour, not just the label.
	if got := SpawnCapFor(NewPersonal("a", "b").Mode); got != SpawnCapOrchestrator {
		t.Errorf("personal SpawnCap = %d, want %d", got, SpawnCapOrchestrator)
	}
}

func TestSpawnCapFor(t *testing.T) {
	cases := map[string]int{
		ModeSolo:         SpawnCapSolo,
		ModeTight:        SpawnCapTight,
		ModeOrchestrator: SpawnCapOrchestrator,
		// Unknown / empty modes fall back to the safest stance.
		"":        SpawnCapSolo,
		"garbage": SpawnCapSolo,
		"AUTO":    SpawnCapSolo,
	}
	for mode, want := range cases {
		if got := SpawnCapFor(mode); got != want {
			t.Errorf("SpawnCapFor(%q) = %d, want %d", mode, got, want)
		}
	}
}

func TestIsValidName(t *testing.T) {
	// Happy path: lowercase identifiers fit for filesystem paths
	// (~/.carlos/frames/<name>/...) and YAML keys.
	for _, ok := range []string{
		"personal", "work", "work-a", "work_b", "client1", "p",
		// 31-char boundary - exactly at the cap.
		"a" + strings.Repeat("b", 30),
	} {
		if !IsValidName(ok) {
			t.Errorf("IsValidName(%q) = false, want true", ok)
		}
	}
	// Rejected: empty, capitals, digit-leading, path escape, separators,
	// whitespace, and the 32-char over-the-cap case. Each row is the
	// silent-failure mode we're closing.
	for _, no := range []string{
		"",                            // empty
		"Personal",                    // capital
		"PERSONAL",                    // upper
		"123foo",                      // digit start
		"-foo",                        // hyphen start
		"_foo",                        // underscore start
		"../escape",                   // path escape
		"work/x",                      // separator
		"foo bar",                     // space
		"foo.bar",                     // dot
		"foo:bar",                     // colon
		"foo@bar",                     // at
		strings.Repeat("a", 32),       // 32-char over the cap
	} {
		if IsValidName(no) {
			t.Errorf("IsValidName(%q) = true, want false", no)
		}
	}
}

func TestIsValidAccent(t *testing.T) {
	for _, ok := range []string{"rust", "slate", "olive", "teal", "plum", "cream", "sand", "navy"} {
		if !IsValidAccent(ok) {
			t.Errorf("IsValidAccent(%q) = false, want true", ok)
		}
	}
	for _, no := range []string{"", "magenta", "RED", "Rust"} {
		if IsValidAccent(no) {
			t.Errorf("IsValidAccent(%q) = true, want false", no)
		}
	}
}

func TestMigrateFromLegacy_synthesizesPersonal(t *testing.T) {
	in := Config{}
	out := MigrateFromLegacy(in, "anthropic", "claude-sonnet-4-6")
	if out.Default != DefaultPersonalName {
		t.Errorf("Default = %q, want %q", out.Default, DefaultPersonalName)
	}
	if out.Active != DefaultPersonalName {
		t.Errorf("Active = %q, want %q", out.Active, DefaultPersonalName)
	}
	if len(out.List) != 1 || out.List[0].Name != DefaultPersonalName {
		t.Fatalf("List wrong: %+v", out.List)
	}
	if out.List[0].Provider != "anthropic" || out.List[0].Model != "claude-sonnet-4-6" {
		t.Errorf("provider/model not threaded: %+v", out.List[0])
	}
}

func TestMigrateFromLegacy_idempotent(t *testing.T) {
	existing := Config{
		Default: "work",
		Active:  "work",
		List:    []Frame{{Name: "work", Provider: "anthropic"}},
	}
	out := MigrateFromLegacy(existing, "openrouter", "ignored")
	if !reflect.DeepEqual(out, existing) {
		t.Errorf("Migrate mutated an already-populated config:\n got %+v\nwant %+v", out, existing)
	}
}

func TestConfigFindAndNames(t *testing.T) {
	c := &Config{
		List: []Frame{
			{Name: "personal"},
			{Name: "work"},
		},
	}
	if c.Find("work") == nil {
		t.Errorf("Find(\"work\") returned nil")
	}
	if c.Find("missing") != nil {
		t.Errorf("Find(\"missing\") returned non-nil")
	}
	if got := c.Names(); !reflect.DeepEqual(got, []string{"personal", "work"}) {
		t.Errorf("Names = %v, want [personal work]", got)
	}

	// Nil receiver is safe.
	var nilCfg *Config
	if nilCfg.Find("x") != nil {
		t.Errorf("nil receiver Find returned non-nil")
	}
	if nilCfg.Names() != nil {
		t.Errorf("nil receiver Names returned non-nil")
	}
}

func TestResolveProvider_pantryFallback(t *testing.T) {
	pantry := map[string]SharedProvider{
		"anthropic": {APIKey: "sk-shared", DefaultModel: "claude-sonnet-4-6"},
	}
	f := Frame{Provider: "anthropic"}
	got, ok := ResolveProvider(f, "", pantry)
	if !ok {
		t.Fatal("ok=false; want true")
	}
	if got.APIKey != "sk-shared" || got.Model != "claude-sonnet-4-6" {
		t.Errorf("fallback wrong: %+v", got)
	}
}

func TestResolveProvider_perFrameOverride(t *testing.T) {
	pantry := map[string]SharedProvider{
		"anthropic": {APIKey: "sk-personal", DefaultModel: "claude-sonnet-4-6"},
	}
	f := Frame{
		Provider: "anthropic",
		Model:    "claude-opus-4-7",
		ProviderOverride: map[string]ProviderOverride{
			"anthropic": {APIKey: "sk-work-billing"},
		},
	}
	got, _ := ResolveProvider(f, "", pantry)
	if got.APIKey != "sk-work-billing" {
		t.Errorf("APIKey override not applied: %+v", got)
	}
	if got.Model != "claude-opus-4-7" {
		t.Errorf("frame Model not preferred over default: %+v", got)
	}
}

func TestResolveProvider_inheritsDefaultProvider(t *testing.T) {
	pantry := map[string]SharedProvider{
		"openrouter": {APIKey: "sk-or"},
	}
	f := Frame{} // no Provider set
	got, ok := ResolveProvider(f, "openrouter", pantry)
	if !ok {
		t.Fatal("ok=false")
	}
	if got.Provider != "openrouter" {
		t.Errorf("Provider = %q, want %q", got.Provider, "openrouter")
	}
}

func TestResolveProvider_emptyEverything(t *testing.T) {
	_, ok := ResolveProvider(Frame{}, "", nil)
	if ok {
		t.Error("ok=true; want false when neither frame nor default name a provider")
	}
}

func TestResolveActive_precedence(t *testing.T) {
	cfg := &Config{
		Default: "personal",
		Active:  "work",
		List: []Frame{
			{Name: "personal"},
			{Name: "work", CwdHints: []string{"/Users/george/Code/ludus"}},
			{Name: "research", CwdHints: []string{"/Users/george/Code/anneal"}},
		},
	}
	type tc struct {
		name   string
		in     Input
		want   string
		reason string
	}
	cases := []tc{
		{"env wins over flag", Input{Env: "research", Flag: "personal"}, "research", ReasonEnv},
		{"flag wins over cwd", Input{Flag: "personal", Cwd: "/Users/george/Code/ludus/web"}, "personal", ReasonFlag},
		{"cwd exact match", Input{Cwd: "/Users/george/Code/ludus/api"}, "work", ReasonCwdHintExact},
		{"cwd no match falls to persisted", Input{Cwd: "/tmp/other"}, "work", ReasonPersistedActive},
		{"no signals at all uses persisted", Input{}, "work", ReasonPersistedActive},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ResolveActive(cfg, c.in)
			if !ok {
				t.Fatal("ok=false")
			}
			if got.Frame != c.want || got.Reason != c.reason {
				t.Errorf("got {Frame:%q Reason:%q}, want {Frame:%q Reason:%q}",
					got.Frame, got.Reason, c.want, c.reason)
			}
		})
	}
}

// TestResolveActive_unknownEnvFallsThrough confirms that CARLOS_FRAME
// naming a frame that doesn't exist drops to the next signal AND
// records a Warning. Without this an unknown env value would boot a
// phantom frame and explode at the first tool call.
func TestResolveActive_unknownEnvFallsThrough(t *testing.T) {
	cfg := &Config{
		Default: "personal",
		Active:  "work",
		List: []Frame{
			{Name: "personal"},
			{Name: "work", CwdHints: []string{"/Users/george/Code/ludus"}},
		},
	}
	cases := []struct {
		name      string
		in        Input
		wantFrame string
		wantReason string
		warnSub   string
	}{
		{
			name:      "unknown env falls to flag",
			in:        Input{Env: "ghost", Flag: "personal"},
			wantFrame: "personal",
			wantReason: ReasonFlag,
			warnSub:   "CARLOS_FRAME=ghost",
		},
		{
			name:      "unknown env falls to cwd hint",
			in:        Input{Env: "ghost", Cwd: "/Users/george/Code/ludus/api"},
			wantFrame: "work",
			wantReason: ReasonCwdHintExact,
			warnSub:   "CARLOS_FRAME=ghost",
		},
		{
			name:      "unknown env falls to persisted active",
			in:        Input{Env: "ghost"},
			wantFrame: "work",
			wantReason: ReasonPersistedActive,
			warnSub:   "CARLOS_FRAME=ghost",
		},
		{
			name:      "unknown flag falls to persisted",
			in:        Input{Flag: "phantom"},
			wantFrame: "work",
			wantReason: ReasonPersistedActive,
			warnSub:   "-f phantom",
		},
		{
			name:      "unknown env AND unknown flag both fall through",
			in:        Input{Env: "ghost", Flag: "phantom"},
			wantFrame: "work",
			wantReason: ReasonPersistedActive,
			warnSub:   "phantom",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, ok := ResolveActive(cfg, c.in)
			if !ok {
				t.Fatal("ok=false")
			}
			if res.Frame != c.wantFrame {
				t.Errorf("Frame = %q, want %q", res.Frame, c.wantFrame)
			}
			if res.Reason != c.wantReason {
				t.Errorf("Reason = %q, want %q", res.Reason, c.wantReason)
			}
			if !strings.Contains(res.Warning, c.warnSub) {
				t.Errorf("Warning = %q, want substring %q", res.Warning, c.warnSub)
			}
		})
	}
}

// TestResolveActive_knownEnvNoWarning is the back-compat assertion: an
// env value that DOES name a real frame still wins with no warning.
func TestResolveActive_knownEnvNoWarning(t *testing.T) {
	cfg := &Config{
		Default: "personal",
		Active:  "personal",
		List:    []Frame{{Name: "personal"}, {Name: "work"}},
	}
	res, ok := ResolveActive(cfg, Input{Env: "work"})
	if !ok {
		t.Fatal("ok=false")
	}
	if res.Frame != "work" || res.Reason != ReasonEnv {
		t.Errorf("got %+v, want work/env", res)
	}
	if res.Warning != "" {
		t.Errorf("Warning = %q, want empty", res.Warning)
	}
}

func TestResolveActive_cwdMultipleCandidates(t *testing.T) {
	cfg := &Config{
		Default: "personal",
		Active:  "personal",
		List: []Frame{
			{Name: "personal", CwdHints: []string{"/Users/george"}},
			{Name: "work", CwdHints: []string{"/Users/george/Code"}},
		},
	}
	res, ok := ResolveActive(cfg, Input{Cwd: "/Users/george/Code/ludus"})
	if !ok {
		t.Fatal("ok=false")
	}
	if res.Reason != ReasonCwdHintMultiple {
		t.Errorf("Reason = %q, want %q", res.Reason, ReasonCwdHintMultiple)
	}
	if res.Frame != "personal" {
		t.Errorf("Frame = %q, want fallback to persisted-active %q", res.Frame, "personal")
	}
	if !reflect.DeepEqual(res.Candidates, []string{"personal", "work"}) {
		t.Errorf("Candidates = %v, want [personal work]", res.Candidates)
	}
}

func TestResolveActive_emptyCfg(t *testing.T) {
	if _, ok := ResolveActive(nil, Input{}); ok {
		t.Error("ok=true on nil cfg")
	}
	if _, ok := ResolveActive(&Config{}, Input{}); ok {
		t.Error("ok=true on empty List")
	}
}

func TestResolveActive_defaultFallback(t *testing.T) {
	cfg := &Config{
		Default: "personal",
		List:    []Frame{{Name: "personal"}},
	}
	res, ok := ResolveActive(cfg, Input{})
	if !ok {
		t.Fatal("ok=false")
	}
	if res.Reason != ReasonDefault {
		t.Errorf("Reason = %q, want %q", res.Reason, ReasonDefault)
	}
	if res.Frame != "personal" {
		t.Errorf("Frame = %q, want personal", res.Frame)
	}
}

func TestResolveActive_defaultFallbackEverythingEmpty(t *testing.T) {
	cfg := &Config{
		List: []Frame{{Name: "alpha"}, {Name: "beta"}},
	}
	res, _ := ResolveActive(cfg, Input{})
	if res.Frame != "alpha" {
		t.Errorf("Frame = %q, want first-listed %q", res.Frame, "alpha")
	}
}

func TestHintMatches_prefix(t *testing.T) {
	sep := string(filepath.Separator)
	cases := []struct {
		hint, cwd string
		want      bool
	}{
		{"/Users/george/Code/anneal", "/Users/george/Code/anneal", true},
		{"/Users/george/Code/anneal", "/Users/george/Code/anneal/uop", true},
		{"/Users/george/Code/ann", "/Users/george/Code/anneal", false},
		{"/Users/george/Code", "/Users/george/Code/ludus", true},
		{"/Users/george/Code" + sep, "/Users/george/Code/ludus", true},
		{"", "/anything", false},
		{"/x", "", false},
	}
	for _, c := range cases {
		got := hintMatches(c.hint, c.cwd)
		if got != c.want {
			t.Errorf("hintMatches(%q, %q) = %v, want %v", c.hint, c.cwd, got, c.want)
		}
	}
}

func TestHintMatches_glob(t *testing.T) {
	cases := []struct {
		hint, cwd string
		want      bool
	}{
		{"/Users/george/Code/ludus*", "/Users/george/Code/ludus", true},
		{"/Users/george/Code/ludus*", "/Users/george/Code/ludus-web", true},
		{"/Users/george/Code/ludus*", "/Users/george/Code/ludus/web/src", true},
		{"/Users/george/Code/ludus*", "/Users/george/Code/anneal", false},
		{"/var/log/*.log", "/var/log/foo.log", true},
	}
	for _, c := range cases {
		got := hintMatches(c.hint, c.cwd)
		if got != c.want {
			t.Errorf("hintMatches(%q, %q) = %v, want %v", c.hint, c.cwd, got, c.want)
		}
	}
}

func TestHasGlob(t *testing.T) {
	if !hasGlob("foo*") || !hasGlob("a?") || !hasGlob("a[b]") {
		t.Errorf("hasGlob missed a meta char")
	}
	if hasGlob("plain/path") {
		t.Errorf("hasGlob false-positive on plain path")
	}
}
