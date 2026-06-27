package chat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
)

func keyRune(r rune) tea.KeyMsg        { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }
func keyType(t tea.KeyType) tea.KeyMsg { return tea.KeyMsg{Type: t} }

// openedConfigModel seeds a config file and opens the overlay against it,
// returning a Model ready to drive through handleConfigKey.
func openedConfigModel(t *testing.T, cfg *config.Config) *Model {
	t.Helper()
	seedConfigFile(t, cfg)
	m := configModel(cfg, cfg.Frames.Active)
	if cmd := m.openConfig(); cmd != nil {
		// openConfig only returns a cmd on load failure.
		if msg, ok := cmd().(statusMsg); ok {
			t.Fatalf("openConfig failed: %s", msg.text)
		}
	}
	return m
}

func cfgWithFrames() *config.Config {
	return &config.Config{
		UserName:        "Boss",
		DefaultProvider: "anthropic",
		Providers: map[string]config.ProviderConfig{
			"anthropic":  {APIKey: "sk-anthropicSECRET", DefaultModel: "claude-opus-4-8"},
			"openrouter": {APIKey: "sk-orSECRET", DefaultModel: "google/gemini-3.5-flash"},
		},
		Frames: frame.Config{
			Active:  "work",
			Default: "personal",
			List: []frame.Frame{
				{Name: "personal", Provider: "anthropic"},
				{Name: "work", Provider: "openrouter", Model: "anthropic/claude-opus-4-8"},
			},
		},
	}
}

func configModel(cfg *config.Config, activeFrame string) *Model {
	return &Model{
		configCfg:   cfg,
		configFrame: activeFrame,
		frame: FrameUI{
			Active:    activeFrame,
			Available: frameNamesFromCfg(cfg),
		},
	}
}

func seedConfigFile(t *testing.T, cfg *config.Config) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Setenv("CARLOS_CONFIG", path)
	return path
}

func rowByID(rows []cfgRow, id string) (cfgRow, bool) {
	for _, r := range rows {
		if r.id == id {
			return r, true
		}
	}
	return cfgRow{}, false
}

func TestBuildConfigRows_Sections(t *testing.T) {
	m := configModel(cfgWithFrames(), "work")
	rows := m.buildConfigRows()

	wantHeaders := []string{"General", "Appearance", "Vault", "Providers", "Shared keys (pantry)"}
	got := map[string]bool{}
	for _, r := range rows {
		if r.kind == cfgHeader {
			got[r.label] = true
		}
	}
	for _, h := range wantHeaders {
		if !got[h] {
			t.Errorf("missing section header %q", h)
		}
	}
	// frame-first rows present
	for _, id := range []string{"name", "skills", "theme", "accent", "vault", "frame_select", "frame_provider", "frame_model", "frame_override_key", "add_provider"} {
		if _, ok := rowByID(rows, id); !ok {
			t.Errorf("missing row %q", id)
		}
	}
	// pantry rows for both providers
	for _, id := range []string{"pantry:anthropic:key", "pantry:openrouter:key", "pantry:anthropic:model"} {
		if _, ok := rowByID(rows, id); !ok {
			t.Errorf("missing pantry row %q", id)
		}
	}
}

func TestBuildConfigRows_NoFramesDegrades(t *testing.T) {
	cfg := cfgWithFrames()
	cfg.Frames = frame.Config{} // no frames
	m := configModel(cfg, "")
	rows := m.buildConfigRows()
	if _, ok := rowByID(rows, "frame_select"); ok {
		t.Error("no-frame config should not render the frame selector")
	}
	if _, ok := rowByID(rows, "pantry:anthropic:key"); !ok {
		t.Error("pantry should still render without frames")
	}
}

func TestResolveEffective_Precedence(t *testing.T) {
	cfg := cfgWithFrames()
	// work frame: Model set explicitly -> wins, source "frame"
	eff := resolveEffective(cfg, cfg.Frames.Find("work"))
	if eff.provider != "openrouter" || eff.model != "anthropic/claude-opus-4-8" || eff.modelSource != "frame" {
		t.Errorf("work resolve = %+v", eff)
	}
	// personal: no Model -> falls to pantry default, source "shared pantry"
	eff = resolveEffective(cfg, cfg.Frames.Find("personal"))
	if eff.provider != "anthropic" || eff.model != "claude-opus-4-8" || eff.modelSource != "shared pantry" {
		t.Errorf("personal resolve = %+v", eff)
	}
	// frame override default model wins over pantry
	p := cfg.Frames.Find("personal")
	p.ProviderOverride = map[string]frame.ProviderOverride{"anthropic": {DefaultModel: "claude-haiku-4-5"}}
	eff = resolveEffective(cfg, p)
	if eff.model != "claude-haiku-4-5" || eff.modelSource != "frame override" {
		t.Errorf("override resolve = %+v", eff)
	}
}

func TestMaskSecret(t *testing.T) {
	cases := map[string]string{
		"":                  "(not set)",
		"short":             "••••",
		"sk-supersecretkey": "sk-••••ey",
		"env:OPENAI_KEY":    "env:OPENAI_KEY", // env ref shown verbatim
	}
	for in, want := range cases {
		if got := maskSecret(in); got != want {
			t.Errorf("maskSecret(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderConfigOverlay_MasksSecrets(t *testing.T) {
	cfg := cfgWithFrames()
	m := configModel(cfg, "work")
	out := renderConfigOverlay(m, 100, 40)
	if !strings.Contains(out, "settings") || !strings.Contains(out, "Providers") || !strings.Contains(out, "Shared keys") {
		t.Errorf("overlay missing structure:\n%s", out)
	}
	// The raw secret values must NEVER appear in the rendered output.
	for _, secret := range []string{"anthropicSECRET", "orSECRET"} {
		if strings.Contains(out, secret) {
			t.Errorf("overlay LEAKED a secret %q:\n%s", secret, out)
		}
	}
}

func TestApplyText_NamePersistsAndLive(t *testing.T) {
	cfg := cfgWithFrames()
	path := seedConfigFile(t, cfg)
	m := configModel(cfg, "work")
	_ = m.applyText("name", "Alice")
	if m.userName != "Alice" {
		t.Errorf("live userName = %q, want Alice", m.userName)
	}
	got, _ := config.Load(path)
	if got.UserName != "Alice" {
		t.Errorf("persisted UserName = %q", got.UserName)
	}
}

func TestApplyEnum_SkillsPersists(t *testing.T) {
	cfg := cfgWithFrames()
	path := seedConfigFile(t, cfg)
	m := configModel(cfg, "work")
	_ = m.applyEnum("skills", config.SkillsConventionClaude)
	got, _ := config.Load(path)
	if got.Skills.Convention != config.SkillsConventionClaude {
		t.Errorf("skills convention = %q", got.Skills.Convention)
	}
}

func TestApplyPantryKeyPersists(t *testing.T) {
	cfg := cfgWithFrames()
	path := seedConfigFile(t, cfg)
	m := configModel(cfg, "work")
	_ = m.applyText("pantry:anthropic:key", "sk-newkey")
	got, _ := config.Load(path)
	if got.Providers["anthropic"].APIKey != "sk-newkey" {
		t.Errorf("pantry key not persisted: %q", got.Providers["anthropic"].APIKey)
	}
}

func TestFrameModelLiveSwap(t *testing.T) {
	cfg := cfgWithFrames()
	seedConfigFile(t, cfg)
	var swapped [2]string
	m := configModel(cfg, "work")
	m.frame.SwitchModel = func(p, model string) (string, string, error) {
		swapped = [2]string{p, model}
		return p, model, nil
	}
	// edit the active frame's model -> persists + live swaps
	_ = m.applyText("frame_model", "openrouter/some-model")
	if cfg.Frames.Find("work").Model != "openrouter/some-model" {
		t.Errorf("frame model not set: %q", cfg.Frames.Find("work").Model)
	}
	if swapped[1] != "openrouter/some-model" {
		t.Errorf("live swap not invoked with new model: %+v", swapped)
	}
}

func TestFrameModel_NonActiveNoLiveSwap(t *testing.T) {
	cfg := cfgWithFrames()
	seedConfigFile(t, cfg)
	called := false
	m := configModel(cfg, "work")
	m.configFrame = "personal" // editing a non-active frame
	m.frame.SwitchModel = func(p, model string) (string, string, error) {
		called = true
		return p, model, nil
	}
	_ = m.applyText("frame_model", "x/y")
	if called {
		t.Error("editing a non-active frame must not live-swap the running session")
	}
	if cfg.Frames.Find("personal").Model != "x/y" {
		t.Error("non-active frame edit should still persist")
	}
}

func TestConfigNavSkipsNonFocusable(t *testing.T) {
	m := configModel(cfgWithFrames(), "work")
	rows := m.buildConfigRows()
	// start at first focusable
	m.configCursor = 0
	for i, r := range rows {
		if r.focusable() {
			m.configCursor = i
			break
		}
	}
	start := m.configCursor
	m.configMove(rows, 1)
	if m.configCursor == start {
		t.Fatal("cursor did not move")
	}
	if !rows[m.configCursor].focusable() {
		t.Errorf("cursor landed on a non-focusable row (kind %d)", rows[m.configCursor].kind)
	}
}

func TestOpenConfig_LoadFailure(t *testing.T) {
	t.Setenv("CARLOS_CONFIG", filepath.Join(t.TempDir(), "absent.yaml"))
	m := &Model{frame: FrameUI{}}
	cmd := m.openConfig()
	if cmd == nil {
		t.Fatal("expected a status cmd on load failure")
	}
	if m.showConfig {
		t.Error("overlay should not open when the config fails to load")
	}
}

func TestOpenCloseConfig(t *testing.T) {
	m := openedConfigModel(t, cfgWithFrames())
	if !m.showConfig || m.configCfg == nil {
		t.Fatal("openConfig did not open")
	}
	rows := m.buildConfigRows()
	if !rows[m.configCursor].focusable() {
		t.Error("cursor should land on a focusable row")
	}
	m.closeConfig()
	if m.showConfig || m.configCfg != nil {
		t.Error("closeConfig did not reset state")
	}
}

func TestHandleConfigKey_NavAndClose(t *testing.T) {
	m := openedConfigModel(t, cfgWithFrames())
	start := m.configCursor
	if _, _, handled := m.handleConfigKey(keyRune('j')); !handled {
		t.Fatal("j should be handled")
	}
	if m.configCursor == start {
		t.Error("j did not move the cursor")
	}
	m.handleConfigKey(keyRune('k'))
	if m.configCursor != start {
		t.Error("k did not move the cursor back")
	}
	// ctrl+c must fall through (handled=false)
	if _, _, handled := m.handleConfigKey(tea.KeyMsg{Type: tea.KeyCtrlC}); handled {
		t.Error("ctrl+c should not be consumed by the overlay")
	}
	// esc closes
	m.handleConfigKey(keyType(tea.KeyEsc))
	if m.showConfig {
		t.Error("esc should close the overlay")
	}
}

func focusID(m *Model, id string) {
	rows := m.buildConfigRows()
	for i, r := range rows {
		if r.id == id {
			m.configCursor = i
			return
		}
	}
}

func TestHandleConfigKey_EnumCyclePersists(t *testing.T) {
	cfg := cfgWithFrames()
	path := seedConfigFile(t, cfg)
	m := configModel(cfg, "work")
	_ = m.openConfig()
	focusID(m, "skills")
	// right cycles agents -> claude
	m.handleConfigKey(keyType(tea.KeyRight))
	got, _ := config.Load(path)
	if got.Skills.Convention != config.SkillsConventionClaude {
		t.Errorf("skills not cycled+persisted: %q", got.Skills.Convention)
	}
}

func TestHandleConfigKey_TextEditFlow(t *testing.T) {
	cfg := cfgWithFrames()
	path := seedConfigFile(t, cfg)
	m := configModel(cfg, "work")
	_ = m.openConfig()
	focusID(m, "name")
	// enter -> edit mode, buffer seeded with current value
	m.handleConfigKey(keyType(tea.KeyEnter))
	if !m.configEditing {
		t.Fatal("enter on a text row should start editing")
	}
	// clear with backspaces then type a new name
	for i := 0; i < 20; i++ {
		m.handleConfigKey(keyType(tea.KeyBackspace))
	}
	for _, r := range "Alice" {
		m.handleConfigKey(keyRune(r))
	}
	m.handleConfigKey(keyType(tea.KeyEnter)) // commit
	if m.configEditing {
		t.Error("enter should commit + leave edit mode")
	}
	got, _ := config.Load(path)
	if got.UserName != "Alice" {
		t.Errorf("edited name not persisted: %q", got.UserName)
	}
	// edit again then cancel with esc -> no change
	focusID(m, "name")
	m.handleConfigKey(keyType(tea.KeyEnter))
	m.handleConfigKey(keyRune('Z'))
	m.handleConfigKey(keyType(tea.KeyEsc))
	got, _ = config.Load(path)
	if got.UserName != "Alice" {
		t.Errorf("esc should cancel the edit, got %q", got.UserName)
	}
}

func TestHandleConfigKey_AddProviderFlow(t *testing.T) {
	cfg := cfgWithFrames()
	path := seedConfigFile(t, cfg)
	m := configModel(cfg, "work")
	_ = m.openConfig()
	focusID(m, "add_provider")
	m.handleConfigKey(keyType(tea.KeyEnter)) // start adding
	if !m.configEditing || !m.configAddingProvider {
		t.Fatal("add provider should enter the naming edit")
	}
	for _, r := range "ollama" {
		m.handleConfigKey(keyRune(r))
	}
	m.handleConfigKey(keyType(tea.KeyEnter)) // commit the name
	// In memory the new provider exists immediately and focus jumps to its
	// key row; a bare empty entry is not yet persisted (nothing to write),
	// so fill the key to make it durable - the realistic flow.
	if _, ok := m.configCfg.Provider("ollama"); !ok {
		t.Fatal("new provider should exist in memory right after add")
	}
	if rows := m.buildConfigRows(); rows[m.configCursor].id != "pantry:ollama:key" {
		t.Errorf("focus should jump to the new provider's key row, got %q", rows[m.configCursor].id)
	}
	m.handleConfigKey(keyType(tea.KeyEnter)) // edit the key
	for _, r := range "sk-ollamakey" {
		m.handleConfigKey(keyRune(r))
	}
	m.handleConfigKey(keyType(tea.KeyEnter)) // commit the key
	got, _ := config.Load(path)
	if got.Providers["ollama"].APIKey != "sk-ollamakey" {
		t.Errorf("new provider not persisted: %+v", got.Providers["ollama"])
	}
}

func TestApplyText_AppearanceAndVault(t *testing.T) {
	cfg := cfgWithFrames()
	path := seedConfigFile(t, cfg)
	m := configModel(cfg, "work")
	_ = m.applyText("accent", "#ff8800")
	_ = m.applyText("vault", "/tmp/vault")
	_ = m.applyEnum("theme", "dark")
	_ = m.applyEnum("default_provider", "openrouter")
	got, _ := config.Load(path)
	if got.Theme.Accent != "#ff8800" || got.Vault.Path != "/tmp/vault" || got.Theme.Variant != "dark" || got.DefaultProvider != "openrouter" {
		t.Errorf("appearance/vault not persisted: %+v %+v", got.Theme, got.Vault)
	}
	// theme "auto" clears the variant
	_ = m.applyEnum("theme", "auto")
	got, _ = config.Load(path)
	if got.Theme.Variant != "" {
		t.Errorf("auto theme should clear variant, got %q", got.Theme.Variant)
	}
}

func TestApplyEnum_FrameProviderLive(t *testing.T) {
	cfg := cfgWithFrames()
	path := seedConfigFile(t, cfg)
	m := configModel(cfg, "work")
	var swapped bool
	m.frame.SwitchModel = func(p, model string) (string, string, error) { swapped = true; return p, model, nil }
	_ = m.applyEnum("frame_provider", "anthropic")
	got, _ := config.Load(path)
	if got.Frames.Find("work").Provider != "anthropic" {
		t.Errorf("frame provider not persisted: %q", got.Frames.Find("work").Provider)
	}
	if !swapped {
		t.Error("active-frame provider change should live-swap")
	}
}

func TestApplyText_FrameOverrideKey(t *testing.T) {
	cfg := cfgWithFrames()
	path := seedConfigFile(t, cfg)
	m := configModel(cfg, "work")
	_ = m.applyText("frame_override_key", "sk-workonly")
	got, _ := config.Load(path)
	// work frame resolves to openrouter; the override should land there.
	if got.Frames.Find("work").ProviderOverride["openrouter"].APIKey != "sk-workonly" {
		t.Errorf("frame override key not persisted: %+v", got.Frames.Find("work").ProviderOverride)
	}
}

func TestConfigHelperNilBranches(t *testing.T) {
	if frameProvider(nil) != "" || frameModel(nil) != "" || frameOverrideKey(nil, "p") != "" {
		t.Error("nil-frame helpers should return empty")
	}
	empty := &config.Config{}
	if frameProviderRaw(empty, "x") != "" || frameModelRaw(empty, "x") != "" {
		t.Error("raw helpers on a missing frame should return empty")
	}
	if skillsConventionOrDefault("claude") != "claude" {
		t.Error("explicit skills convention should pass through")
	}
	if themeVariantOrAuto("dark") != "dark" {
		t.Error("explicit theme variant should pass through")
	}
	if dashRule(0) == "" {
		t.Error("dashRule(0) should still produce a rule")
	}
}

func TestResolveEffective_Fallbacks(t *testing.T) {
	// no providers at all -> (none)
	eff := resolveEffective(&config.Config{}, nil)
	if eff.provider != "(none)" {
		t.Errorf("empty config resolve = %+v", eff)
	}
	// no DefaultProvider, frame pins none -> first pantry entry with creds,
	// no default model -> built-in source.
	c := &config.Config{Providers: map[string]config.ProviderConfig{"ollama": {BaseURL: "http://x"}}}
	eff = resolveEffective(c, &frame.Frame{Name: "f"})
	if eff.provider != "ollama" || eff.model != "(provider default)" || eff.modelSource != "built-in" {
		t.Errorf("fallback resolve = %+v", eff)
	}
}

func TestApplyPantry_ModelAndBaseURL(t *testing.T) {
	cfg := cfgWithFrames()
	path := seedConfigFile(t, cfg)
	m := configModel(cfg, "work")
	_ = m.applyText("pantry:anthropic:model", "claude-x")
	_ = m.applyText("pantry:anthropic:baseurl", "http://local")
	got, _ := config.Load(path)
	if got.Providers["anthropic"].DefaultModel != "claude-x" || got.Providers["anthropic"].BaseURL != "http://local" {
		t.Errorf("pantry model/baseurl not persisted: %+v", got.Providers["anthropic"])
	}
}

func TestApplyEnum_FrameProviderInherit(t *testing.T) {
	cfg := cfgWithFrames()
	path := seedConfigFile(t, cfg)
	m := configModel(cfg, "work")
	var swappedProvider string
	m.frame.SwitchModel = func(p, model string) (string, string, error) {
		swappedProvider = p
		return p, model, nil
	}
	_ = m.applyEnum("frame_provider", inheritLabel)
	got, _ := config.Load(path)
	if got.Frames.Find("work").Provider != "" {
		t.Errorf("inherit should clear the frame provider, got %q", got.Frames.Find("work").Provider)
	}
	// MED fix: the live swap must use the RESOLVED provider (DefaultProvider
	// = anthropic), NOT the now-blank frame provider (SwitchModel reads "" as
	// "keep the current provider", which would be the wrong one).
	if swappedProvider != "anthropic" {
		t.Errorf("inherit live-swap provider = %q, want resolved default anthropic", swappedProvider)
	}
}

func TestMaybeLiveSwap_NoConcreteModelSkips(t *testing.T) {
	// active frame inherits; default provider has no DefaultModel and the
	// frame has no Model -> no concrete model, so the live swap must be
	// skipped (never forward the "(provider default)" placeholder).
	cfg := &config.Config{
		DefaultProvider: "ollama",
		Providers:       map[string]config.ProviderConfig{"ollama": {BaseURL: "http://x"}},
		Frames:          frame.Config{Active: "work", List: []frame.Frame{{Name: "work"}}},
	}
	seedConfigFile(t, cfg)
	called := false
	m := configModel(cfg, "work")
	m.frame.SwitchModel = func(p, model string) (string, string, error) { called = true; return p, model, nil }
	_ = m.applyEnum("frame_provider", inheritLabel)
	if called {
		t.Error("with no concrete model the live swap must be skipped, not sent a placeholder")
	}
	if !strings.Contains(m.configNotice, "set a model") {
		t.Errorf("expected a 'set a model' notice, got %q", m.configNotice)
	}
}

func TestRenderConfigFooter_Editing(t *testing.T) {
	m := &Model{configEditing: true}
	if !strings.Contains(renderConfigFooter(m), "save") {
		t.Error("editing footer should offer save")
	}
	m.configEditing = false
	if !strings.Contains(renderConfigFooter(m), "move") {
		t.Error("nav footer should offer move")
	}
}

func TestConfigApply_ErrorPaths(t *testing.T) {
	// unknown ids are harmless no-ops
	m := configModel(cfgWithFrames(), "work")
	if cmd := m.applyText("nope_id", "x"); cmd != nil {
		t.Error("unknown text id should be a no-op")
	}
	if cmd := m.applyEnum("nope_id", "x"); cmd != nil {
		t.Error("unknown enum id should be a no-op")
	}

	// empty new-provider name is rejected with an error, no save
	seedConfigFile(t, cfgWithFrames())
	m2 := configModel(cfgWithFrames(), "work")
	_ = m2.applyText("new_provider_name", "")
	if m2.configErr == "" {
		t.Error("empty provider name should set configErr")
	}

	// a save failure surfaces in configErr rather than panicking: point
	// CARLOS_CONFIG under a regular file so MkdirAll of the parent fails.
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CARLOS_CONFIG", filepath.Join(blocker, "config.yaml"))
	m3 := configModel(cfgWithFrames(), "work")
	_ = m3.applyText("name", "X")
	if m3.configErr == "" {
		t.Error("a save failure should surface in configErr")
	}
}

func TestApplyEnum_FrameProviderErrors(t *testing.T) {
	// unknown frame -> SetFrameProvider errors into configErr
	m := configModel(cfgWithFrames(), "work")
	m.configFrame = "ghost"
	_ = m.applyEnum("frame_provider", "anthropic")
	if m.configErr == "" {
		t.Error("frame_provider on an unknown frame should set configErr")
	}
	// save failure path
	dir := t.TempDir()
	blocker := filepath.Join(dir, "b")
	_ = os.WriteFile(blocker, []byte("x"), 0o600)
	t.Setenv("CARLOS_CONFIG", filepath.Join(blocker, "config.yaml"))
	m2 := configModel(cfgWithFrames(), "work")
	_ = m2.applyEnum("frame_provider", "anthropic")
	if m2.configErr == "" {
		t.Error("a save failure on frame_provider should surface in configErr")
	}
}
