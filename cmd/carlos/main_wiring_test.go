package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/anthropic"
	"github.com/georgebuilds/carlos/internal/providers/gemini"
	"github.com/georgebuilds/carlos/internal/providers/ollama"
	"github.com/georgebuilds/carlos/internal/providers/openai"
	"github.com/georgebuilds/carlos/internal/providers/openrouter"
	"github.com/georgebuilds/carlos/internal/tools"
)

// mustToolsRegistry returns a fresh Registry. Helper so the
// buildResearchEngine wrong-type tests stay readable.
func mustToolsRegistry(t *testing.T) *tools.Registry {
	t.Helper()
	return tools.NewRegistry()
}

// newRegistryWithRealSearch seeds the registry with a real WebSearchTool
// (so the web_search type-assertion passes) but leaves web_fetch
// un-registered so the caller can plug in a stub.
func newRegistryWithRealSearch(t *testing.T) *tools.Registry {
	t.Helper()
	reg := tools.NewRegistry()
	// Real WebSearchTool with a non-nil Backend so the
	// Backend == nil branch doesn't short-circuit ahead of the
	// web_fetch type check.
	ws := tools.NewWebSearchTool()
	ws.Backend = stubSearchBackend{}
	reg.Register(ws)
	return reg
}

// stubSearchBackend is a no-op tools.SearchBackend so a real
// WebSearchTool's Backend field is non-nil.
type stubSearchBackend struct{}

func (stubSearchBackend) Name() string { return "stub" }
func (stubSearchBackend) Search(_ context.Context, _ string, _ int) ([]tools.SearchResult, error) {
	return nil, nil
}

// stubProvider satisfies providers.Provider so buildResearchEngine
// gets past the nil-provider check and reaches the type-assertion
// branches we want to exercise.
type stubProvider struct{}

func (stubProvider) Name() string                       { return "stub" }
func (stubProvider) Capabilities() providers.Capabilities { return providers.Capabilities{} }
func (stubProvider) Stream(_ context.Context, _ providers.Request) (<-chan providers.Event, error) {
	return nil, nil
}

// --- versionString --------------------------------------------------

func TestVersionString_NotEmpty(t *testing.T) {
	got := versionString()
	if got == "" {
		t.Error("versionString returned empty")
	}
	// Under `go test` the binary has BuildInfo but typically no semver,
	// so we expect either the fallback or a "dev (<rev>...)" string.
	if !strings.HasPrefix(got, fallbackVersion) && !strings.HasPrefix(got, "v") {
		t.Errorf("versionString = %q; expected prefix %q or 'v'", got, fallbackVersion)
	}
}

// --- applyTheme -----------------------------------------------------

func TestApplyTheme_NilCfg(t *testing.T) {
	// nil cfg must not panic; used in early onboarding paths.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("applyTheme(nil) panicked: %v", r)
		}
	}()
	applyTheme(nil)
}

func TestApplyTheme_WithCfg(t *testing.T) {
	cfg := &config.Config{
		Theme: config.ThemeConfig{Variant: "dark", Accent: "#ff8800"},
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("applyTheme panicked: %v", r)
		}
	}()
	applyTheme(cfg)
}

// --- resolveSessionFromFlag -----------------------------------------

func TestResolveSessionFromFlag_EmptyReturnsFresh(t *testing.T) {
	id, err := resolveSessionFromFlag("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "" {
		t.Errorf("empty mode should return empty id; got %q", id)
	}
}

func TestResolveSessionFromFlag_ContinueNoDB(t *testing.T) {
	// Point HOME at an empty tmp dir so state.db doesn't exist.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	id, err := resolveSessionFromFlag("continue")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "" {
		t.Errorf("missing DB should degrade to fresh; got %q", id)
	}
}

func TestResolveSessionFromFlag_ContinueEmptyDB(t *testing.T) {
	// Create an empty state.db so the path exists but has no sessions.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	dbDir := filepath.Join(tmp, ".carlos")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dbDir, "state.db")
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("OpenStateDB: %v", err)
	}
	log.Close()

	id, err := resolveSessionFromFlag("continue")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id != "" {
		t.Errorf("empty sessions should degrade to fresh; got %q", id)
	}
}

func TestResolveSessionFromFlag_ResumeWithSessions(t *testing.T) {
	// Seed a session so runSessionPicker gets past the empty check.
	// Without a TTY the bubbletea Program returns an error, which
	// resolveSessionFromFlag surfaces via the final `return id, err`.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	dbDir := filepath.Join(tmp, ".carlos")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dbDir, "state.db")
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	if err := log.InsertAgent(context.Background(), agent.AgentRow{
		ID: "01H-r", RootID: "01H-r", State: agent.StateRunning,
		Title: "t", Model: "m",
		CreatedAt: now, UpdatedAt: now, LastHeartbeatAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	log.Close()

	// Accept any outcome; the wiring through the resume case is
	// what we're after.
	_, _ = resolveSessionFromFlag("resume")
}

func TestResolveSessionFromFlag_ResumeNoDB(t *testing.T) {
	// No DB → openStateDBForPicker returns ErrNoSessions → resume
	// degrades silently to "" + nil.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	id, err := resolveSessionFromFlag("resume")
	if err != nil {
		t.Fatalf("expected silent degrade; got error: %v", err)
	}
	if id != "" {
		t.Errorf("expected empty id; got %q", id)
	}
}

func TestResolveSessionFromFlag_ResumeEmptyDB(t *testing.T) {
	// DB exists but no sessions → ErrNoSessions surfaces from
	// runSessionPicker → degrades silently.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	dbDir := filepath.Join(tmp, ".carlos")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	log, err := agent.OpenStateDB(filepath.Join(dbDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	log.Close()
	id, err := resolveSessionFromFlag("resume")
	if err != nil {
		t.Fatalf("expected silent degrade; got error: %v", err)
	}
	if id != "" {
		t.Errorf("expected empty id; got %q", id)
	}
}

func TestResolveSessionFromFlag_UnknownMode(t *testing.T) {
	id, err := resolveSessionFromFlag("bogus")
	if err != nil {
		t.Fatalf("unknown mode error: %v", err)
	}
	if id != "" {
		t.Errorf("unknown mode should return empty; got %q", id)
	}
}

// --- parsePleaseArgs additional coverage ----------------------------

func TestParsePleaseArgs_All(t *testing.T) {
	opts, prompt, err := parsePleaseArgs([]string{
		"-y", "-p", "openai", "-m", "gpt-4o", "-w", "hello world",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.autoApprove {
		t.Error("expected autoApprove")
	}
	if opts.provider != "openai" {
		t.Errorf("provider = %q want openai", opts.provider)
	}
	if opts.model != "gpt-4o" {
		t.Errorf("model = %q want gpt-4o", opts.model)
	}
	if !opts.worktree {
		t.Error("expected worktree")
	}
	if prompt != "hello world" {
		t.Errorf("prompt = %q want 'hello world'", prompt)
	}
}

func TestParsePleaseArgs_LongFlags(t *testing.T) {
	opts, prompt, err := parsePleaseArgs([]string{
		"--yes", "--provider", "anthropic", "--model", "claude-sonnet-4-6", "--worktree", "do stuff",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.autoApprove || opts.provider != "anthropic" || opts.model != "claude-sonnet-4-6" || !opts.worktree {
		t.Errorf("opts: %+v", opts)
	}
	if prompt != "do stuff" {
		t.Errorf("prompt = %q", prompt)
	}
}

// TestParsePleaseArgs_RejectsMultiTokenPrompt pins the post-v0.7.0
// contract: unquoted multi-word prompts are an error with a "quote
// it" hint. The old behavior silently joined tokens with spaces,
// which read weirdly in combination with -y and made shell-history
// re-use brittle.
func TestParsePleaseArgs_RejectsMultiTokenPrompt(t *testing.T) {
	_, _, err := parsePleaseArgs([]string{"hello", "world"})
	if err == nil {
		t.Fatal("expected error for multi-token prompt")
	}
	if !strings.Contains(err.Error(), "single prompt") {
		t.Errorf("error message missing hint: %v", err)
	}
}

// TestParsePleaseArgs_SingleTokenPromptAccepted confirms hyphenated
// single tokens still work — the user mentioned `carlos please
// say-hello` as the canonical short form.
func TestParsePleaseArgs_SingleTokenPromptAccepted(t *testing.T) {
	_, prompt, err := parsePleaseArgs([]string{"say-hello"})
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "say-hello" {
		t.Errorf("prompt = %q want say-hello", prompt)
	}
}

func TestParsePleaseArgs_ProviderMissing(t *testing.T) {
	if _, _, err := parsePleaseArgs([]string{"-p"}); err == nil {
		t.Error("expected error for missing -p value")
	}
}

func TestParsePleaseArgs_ModelMissing(t *testing.T) {
	if _, _, err := parsePleaseArgs([]string{"-m"}); err == nil {
		t.Error("expected error for missing -m value")
	}
}

func TestParsePleaseArgs_NothingButFlags(t *testing.T) {
	opts, prompt, err := parsePleaseArgs([]string{"-y"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.autoApprove {
		t.Error("autoApprove not set")
	}
	if prompt != "" {
		t.Errorf("prompt = %q want empty", prompt)
	}
}

// --- buildDispatch + buildDispatchForFrame --------------------------

func TestBuildDispatch_NoProviderConfigured(t *testing.T) {
	cfg := &config.Config{}
	if _, err := buildDispatch(cfg, pleaseOptions{}); err == nil {
		t.Error("expected error when no provider configured")
	}
}

func TestBuildDispatch_AnthropicHappy(t *testing.T) {
	cfg := &config.Config{
		DefaultProvider: "anthropic",
		Providers: map[string]config.ProviderConfig{
			"anthropic": {APIKey: "sk-test", DefaultModel: "claude-x"},
		},
	}
	d, err := buildDispatch(cfg, pleaseOptions{})
	if err != nil {
		t.Fatalf("buildDispatch: %v", err)
	}
	if d == nil || d.name != "anthropic" || d.model != "claude-x" {
		t.Errorf("dispatch: %+v", d)
	}
	if _, ok := d.provider.(*anthropic.Client); !ok {
		t.Errorf("provider type = %T, want *anthropic.Client", d.provider)
	}
}

func TestBuildDispatch_AllProviderSwitches(t *testing.T) {
	cases := []struct {
		name  string
		cfg   config.ProviderConfig
		want  string
		check func(p providers.Provider) bool
	}{
		{"anthropic", config.ProviderConfig{APIKey: "k"}, "claude-3-5-sonnet-latest",
			func(p providers.Provider) bool { _, ok := p.(*anthropic.Client); return ok }},
		{"openai", config.ProviderConfig{APIKey: "k"}, "gpt-4o",
			func(p providers.Provider) bool { _, ok := p.(*openai.Client); return ok }},
		{"gemini", config.ProviderConfig{APIKey: "k"}, "gemini-3.5-flash",
			func(p providers.Provider) bool { _, ok := p.(*gemini.Client); return ok }},
		{"openrouter", config.ProviderConfig{APIKey: "k"}, "anthropic/claude-3.5-sonnet",
			func(p providers.Provider) bool { _, ok := p.(*openrouter.Client); return ok }},
		{"ollama", config.ProviderConfig{BaseURL: "http://x"}, "llama3.1:latest",
			func(p providers.Provider) bool { _, ok := p.(*ollama.Client); return ok }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{
				DefaultProvider: tc.name,
				Providers:       map[string]config.ProviderConfig{tc.name: tc.cfg},
			}
			d, err := buildDispatch(cfg, pleaseOptions{})
			if err != nil {
				t.Fatalf("buildDispatch: %v", err)
			}
			if d.name != tc.name {
				t.Errorf("name = %q want %q", d.name, tc.name)
			}
			if d.model != tc.want {
				t.Errorf("model = %q want %q", d.model, tc.want)
			}
			if !tc.check(d.provider) {
				t.Errorf("provider type mismatch for %s: got %T", tc.name, d.provider)
			}
		})
	}
}

func TestBuildDispatch_UnknownProvider(t *testing.T) {
	cfg := &config.Config{
		DefaultProvider: "weirdai",
		Providers: map[string]config.ProviderConfig{
			"weirdai": {APIKey: "k"},
		},
	}
	if _, err := buildDispatch(cfg, pleaseOptions{}); err == nil {
		t.Error("expected error for unknown provider")
	}
}

func TestBuildDispatch_ProviderNotConfigured(t *testing.T) {
	cfg := &config.Config{
		DefaultProvider: "anthropic",
		Providers: map[string]config.ProviderConfig{
			"anthropic": {}, // no key, no base url
		},
	}
	if _, err := buildDispatch(cfg, pleaseOptions{}); err == nil {
		t.Error("expected error when provider entry is empty")
	}
}

func TestBuildDispatch_FlagOverridesDefaultProvider(t *testing.T) {
	cfg := &config.Config{
		DefaultProvider: "anthropic",
		Providers: map[string]config.ProviderConfig{
			"anthropic": {APIKey: "a"},
			"openai":    {APIKey: "o"},
		},
	}
	d, err := buildDispatch(cfg, pleaseOptions{provider: "openai", model: "gpt-x"})
	if err != nil {
		t.Fatalf("buildDispatch: %v", err)
	}
	if d.name != "openai" {
		t.Errorf("name = %q want openai", d.name)
	}
	if d.model != "gpt-x" {
		t.Errorf("model = %q want gpt-x", d.model)
	}
}

func TestBuildDispatch_FallbackToFirstConfigured(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"anthropic": {APIKey: "a"},
		},
	}
	d, err := buildDispatch(cfg, pleaseOptions{})
	if err != nil {
		t.Fatalf("buildDispatch: %v", err)
	}
	if d.name != "anthropic" {
		t.Errorf("name = %q want anthropic", d.name)
	}
}

func TestBuildDispatchForFrame_UsesFrameProvider(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"openai": {APIKey: "k", DefaultModel: "gpt-4"},
		},
	}
	f := &frame.Frame{Name: "work", Provider: "openai", Model: "gpt-mini"}
	d, err := buildDispatchForFrame(cfg, pleaseOptions{}, f)
	if err != nil {
		t.Fatalf("buildDispatchForFrame: %v", err)
	}
	if d.name != "openai" {
		t.Errorf("name = %q", d.name)
	}
	if d.model != "gpt-mini" {
		t.Errorf("model = %q want gpt-mini (from frame)", d.model)
	}
}

func TestBuildDispatchForFrame_FrameProviderWithFrameDefaultModel(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"openai": {APIKey: "k"},
		},
	}
	f := &frame.Frame{
		Name:     "work",
		Provider: "openai",
		ProviderOverride: map[string]frame.ProviderOverride{
			"openai": {DefaultModel: "gpt-from-frame"},
		},
	}
	d, err := buildDispatchForFrame(cfg, pleaseOptions{}, f)
	if err != nil {
		t.Fatalf("buildDispatchForFrame: %v", err)
	}
	if d.model != "gpt-from-frame" {
		t.Errorf("expected frame-override default model; got %q", d.model)
	}
}

// --- activeFrameForDispatch -----------------------------------------

func TestActiveFrameForDispatch_NoFramesReturnsNil(t *testing.T) {
	cfg := &config.Config{}
	if f := activeFrameForDispatch(cfg, ""); f != nil {
		t.Errorf("expected nil; got %+v", f)
	}
}

func TestActiveFrameForDispatch_FlagWins(t *testing.T) {
	cfg := &config.Config{
		Frames: frame.Config{
			Default: "personal",
			Active:  "personal",
			List: []frame.Frame{
				{Name: "personal"},
				{Name: "work"},
			},
		},
	}
	// Clear any inherited env that could win precedence.
	t.Setenv("CARLOS_FRAME", "")
	f := activeFrameForDispatch(cfg, "work")
	if f == nil || f.Name != "work" {
		t.Errorf("expected work frame; got %+v", f)
	}
}

func TestActiveFrameForDispatch_EnvWins(t *testing.T) {
	cfg := &config.Config{
		Frames: frame.Config{
			Default: "personal",
			List: []frame.Frame{
				{Name: "personal"},
				{Name: "work"},
			},
		},
	}
	t.Setenv("CARLOS_FRAME", "personal")
	f := activeFrameForDispatch(cfg, "")
	if f == nil || f.Name != "personal" {
		t.Errorf("expected personal; got %+v", f)
	}
}

func TestActiveFrameForDispatch_UnknownNameReturnsNil(t *testing.T) {
	cfg := &config.Config{
		Frames: frame.Config{
			Default: "personal",
			List: []frame.Frame{
				{Name: "personal"},
			},
		},
	}
	t.Setenv("CARLOS_FRAME", "")
	// Flag picks a frame that doesn't exist in List; Find returns nil.
	f := activeFrameForDispatch(cfg, "ghost")
	if f != nil {
		t.Errorf("expected nil for unknown frame; got %+v", f)
	}
}

// --- extractCapabilityBackends --------------------------------------

func TestExtractCapabilityBackends_Empty(t *testing.T) {
	if got := extractCapabilityBackends(frame.Frame{}); got != nil {
		t.Errorf("expected nil; got %v", got)
	}
}

func TestExtractCapabilityBackends_NilSettingsSkipped(t *testing.T) {
	f := frame.Frame{
		Capabilities: map[string]map[string]any{
			"calendar": nil,
		},
	}
	if got := extractCapabilityBackends(f); got != nil {
		t.Errorf("nil settings should be skipped; got %v", got)
	}
}

func TestExtractCapabilityBackends_NoBackendKey(t *testing.T) {
	f := frame.Frame{
		Capabilities: map[string]map[string]any{
			"calendar": {"other": "value"},
		},
	}
	if got := extractCapabilityBackends(f); got != nil {
		t.Errorf("missing backend should be skipped; got %v", got)
	}
}

func TestExtractCapabilityBackends_HappyPath(t *testing.T) {
	f := frame.Frame{
		Capabilities: map[string]map[string]any{
			"calendar": {"backend": "google"},
			"email":    {"backend": "gmail"},
			"chat":     {"backend": ""}, // empty string skipped
			"weather":  {"other": 1},    // no backend key skipped
		},
	}
	got := extractCapabilityBackends(f)
	if got["calendar"] != "google" {
		t.Errorf("calendar = %q want google", got["calendar"])
	}
	if got["email"] != "gmail" {
		t.Errorf("email = %q want gmail", got["email"])
	}
	if _, ok := got["chat"]; ok {
		t.Error("chat (empty backend) should be omitted")
	}
	if _, ok := got["weather"]; ok {
		t.Error("weather (no backend) should be omitted")
	}
}

// --- providerDefaultModel -------------------------------------------

func TestProviderDefaultModel(t *testing.T) {
	cases := map[string]string{
		"anthropic":  "claude-3-5-sonnet-latest",
		"openai":     "gpt-4o",
		"gemini":     "gemini-3.5-flash",
		"openrouter": "anthropic/claude-3.5-sonnet",
		"ollama":     "llama3.1:latest",
		"unknown":    "",
		"":           "",
	}
	for name, want := range cases {
		if got := providerDefaultModel(name); got != want {
			t.Errorf("providerDefaultModel(%q) = %q want %q", name, got, want)
		}
	}
}

// --- migrateFrameLayout ---------------------------------------------

func TestMigrateFrameLayout_EmptyHome(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("empty home should be a no-op; panicked: %v", r)
		}
	}()
	migrateFrameLayout("")
}

func TestMigrateFrameLayout_NoLegacyDirs(t *testing.T) {
	tmp := t.TempDir()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("no-legacy should be a no-op; panicked: %v", r)
		}
	}()
	migrateFrameLayout(tmp)
}

func TestMigrateFrameLayout_BlockedDestination(t *testing.T) {
	tmp := t.TempDir()
	// Pre-create a legacy file.
	legacyResearch := filepath.Join(tmp, ".carlos", "research")
	if err := os.MkdirAll(legacyResearch, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyResearch, "x.md"), []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Pre-create the destination DIRECTORY (frames/personal/research) as
	// a regular file so MkdirAll for the destination fails.
	target := frame.PathsFor(tmp, frame.DefaultPersonalName).ResearchDir
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Capture stderr so the error line doesn't pollute test output.
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	migrateFrameLayout(tmp)
	w.Close()
	os.Stderr = origStderr
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	// Expect either a frame migration error line OR a per-file error line.
	if !strings.Contains(buf.String(), "carlos:") {
		t.Errorf("expected error output; got %q", buf.String())
	}
}

func TestMigrateFrameLayout_MovesLegacyFiles(t *testing.T) {
	tmp := t.TempDir()
	// Seed legacy ~/.carlos/research/ with one file.
	legacyResearch := filepath.Join(tmp, ".carlos", "research")
	if err := os.MkdirAll(legacyResearch, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyResearch, "report.md"), []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Capture stderr so the migration line doesn't pollute test output.
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	migrateFrameLayout(tmp)
	w.Close()
	os.Stderr = origStderr
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)

	// File should be at frames/personal/research/.
	personalPath := frame.PathsFor(tmp, frame.DefaultPersonalName).ResearchDir
	if _, err := os.Stat(filepath.Join(personalPath, "report.md")); err != nil {
		t.Errorf("expected file at frame path: %v", err)
	}
	if !strings.Contains(buf.String(), "migrated to per-frame layout") {
		t.Errorf("stderr should mention migration; got %q", buf.String())
	}
}

// --- gitRepoRoot -----------------------------------------------------

func TestGitRepoRoot_InsideRepo(t *testing.T) {
	// The test process runs inside the carlos repo, so this should
	// succeed and return an absolute path under HOME.
	got, err := gitRepoRoot()
	if err != nil {
		t.Skipf("git unavailable or not in repo: %v", err)
	}
	if got == "" || !filepath.IsAbs(got) {
		t.Errorf("gitRepoRoot returned non-abs path: %q", got)
	}
}

func TestGitRepoRoot_NotARepo(t *testing.T) {
	// chdir to /tmp (typically not a git repo) and ensure the error path fires.
	tmp := t.TempDir()
	wd, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	_, err := gitRepoRoot()
	if err == nil {
		t.Skip("git happened to find a parent repo; skipping")
	}
	if !strings.Contains(err.Error(), "not inside a git repo") {
		t.Errorf("error should mention non-repo: %v", err)
	}
}

// --- printUsage -----------------------------------------------------

func TestPrintUsage_ContainsKeyCommands(t *testing.T) {
	r, w, _ := os.Pipe()
	orig := os.Stdout
	os.Stdout = w
	printUsage()
	w.Close()
	os.Stdout = orig
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	out := buf.String()
	for _, kw := range []string{"carlos", "please", "onboard", "version", "Usage:"} {
		if !strings.Contains(out, kw) {
			t.Errorf("usage missing %q:\n%s", kw, out)
		}
	}
}

// --- confirmOverwrite -----------------------------------------------

// withStdin temporarily replaces os.Stdin with the contents of s.
func withStdin(t *testing.T, s string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdin
	os.Stdin = r
	if _, err := w.WriteString(s); err != nil {
		t.Fatal(err)
	}
	w.Close()
	defer func() {
		os.Stdin = orig
		r.Close()
	}()
	// Also drain stdout so the prompt doesn't pollute test output.
	origOut := os.Stdout
	rOut, wOut, _ := os.Pipe()
	os.Stdout = wOut
	fn()
	wOut.Close()
	os.Stdout = origOut
	_, _ = io.Copy(io.Discard, rOut)
	rOut.Close()
}

func TestConfirmOverwrite_Yes(t *testing.T) {
	var got bool
	withStdin(t, "y\n", func() { got = confirmOverwrite("/tmp/cfg") })
	if !got {
		t.Error("expected true for 'y'")
	}
}

func TestConfirmOverwrite_FullYes(t *testing.T) {
	var got bool
	withStdin(t, "yes\n", func() { got = confirmOverwrite("/tmp/cfg") })
	if !got {
		t.Error("expected true for 'yes'")
	}
}

func TestConfirmOverwrite_No(t *testing.T) {
	var got bool
	withStdin(t, "n\n", func() { got = confirmOverwrite("/tmp/cfg") })
	if got {
		t.Error("expected false for 'n'")
	}
}

func TestConfirmOverwrite_EOF(t *testing.T) {
	var got bool
	withStdin(t, "", func() { got = confirmOverwrite("/tmp/cfg") })
	if got {
		t.Error("expected false on EOF")
	}
}

// --- runMemory ------------------------------------------------------

func TestRunMemory_NoArgs(t *testing.T) {
	if err := runMemory(nil); err == nil {
		t.Error("expected error with no args")
	}
}

func TestRunMemory_UnknownSubcommand(t *testing.T) {
	if err := runMemory([]string{"bogus"}); err == nil {
		t.Error("expected error for unknown subcommand")
	}
}

func TestRunMemory_SearchEmptyQuery(t *testing.T) {
	if err := runMemory([]string{"search"}); err == nil {
		t.Error("expected error for empty query")
	}
	if err := runMemory([]string{"search", "   "}); err == nil {
		t.Error("expected error for whitespace-only query")
	}
}

func TestRunMemory_SearchHappyPath(t *testing.T) {
	// Point HOME at a tmp dir so RunSearchInFrame opens a fresh DB.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Capture stdout so the "no matches." line doesn't pollute output.
	r, w, _ := os.Pipe()
	orig := os.Stdout
	os.Stdout = w
	err := runMemory([]string{"search", "anything"})
	w.Close()
	os.Stdout = orig
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if err != nil {
		t.Fatalf("runMemory: %v", err)
	}
	if !strings.Contains(buf.String(), "no matches") {
		t.Errorf("expected no-matches output; got %q", buf.String())
	}
}

func TestRunMemory_SearchWithFrameFlag(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	r, w, _ := os.Pipe()
	orig := os.Stdout
	os.Stdout = w
	err := runMemory([]string{"search", "-f", "work", "test"})
	w.Close()
	os.Stdout = orig
	_, _ = io.Copy(io.Discard, r)
	if err != nil {
		t.Fatalf("runMemory with frame flag: %v", err)
	}
}

func TestRunMemory_SearchFrameFlagMissingValue(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	err := runMemory([]string{"search", "-f"})
	if err == nil {
		t.Error("expected error for missing frame value")
	}
}

// --- runApprovals ---------------------------------------------------

func TestRunApprovals_NoArgs(t *testing.T) {
	if err := runApprovals(nil); err == nil {
		t.Error("expected error with no args")
	}
}

func TestRunApprovals_UnknownSubcommand(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	if err := runApprovals([]string{"bogus"}); err == nil {
		t.Error("expected error for unknown subcommand")
	}
}

func TestRunApprovals_ListEmpty(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	r, w, _ := os.Pipe()
	orig := os.Stdout
	os.Stdout = w
	err := runApprovals([]string{"list"})
	w.Close()
	os.Stdout = orig
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if err != nil {
		t.Fatalf("runApprovals list: %v", err)
	}
	if !strings.Contains(buf.String(), "no pending approvals") {
		t.Errorf("expected 'no pending approvals' message; got %q", buf.String())
	}
}

func TestRunApprovals_AcceptRequiresID(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	err := runApprovals([]string{"accept"})
	if err == nil {
		t.Error("expected error for missing artifact id")
	}
}

func TestRunApprovals_RejectRequiresReason(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	err := runApprovals([]string{"reject", "abc"})
	if err == nil {
		t.Error("expected error for missing reason")
	}
}

func TestRunApprovals_AcceptRejectWithSeedData(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	dbDir := filepath.Join(tmp, ".carlos")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dbDir, "state.db")
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("OpenStateDB: %v", err)
	}
	// Seed a pending approval via the agent API.
	ctx := context.Background()
	ref := agent.ArtifactRef{ID: "art-1", Kind: "file", Path: "/tmp/x", Size: 10}
	if _, err := agent.ProposeApproval(ctx, log, "agent-1", "test", ref); err != nil {
		t.Fatalf("ProposeApproval: %v", err)
	}
	log.Close()

	// List should now report the entry. Capture stdout.
	r, w, _ := os.Pipe()
	orig := os.Stdout
	os.Stdout = w
	err = runApprovals([]string{"list"})
	w.Close()
	os.Stdout = orig
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(buf.String(), "art-1") {
		t.Errorf("expected art-1 in list output; got %q", buf.String())
	}

	// Accept the pending approval.
	r, w, _ = os.Pipe()
	os.Stdout = w
	err = runApprovals([]string{"accept", "art-1", "looks", "good"})
	w.Close()
	os.Stdout = orig
	var bufA bytes.Buffer
	_, _ = io.Copy(&bufA, r)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	if !strings.Contains(bufA.String(), "accepted") {
		t.Errorf("expected accepted output; got %q", bufA.String())
	}

	// Seed another for reject.
	log, err = agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	ref2 := agent.ArtifactRef{ID: "art-2", Kind: "file", Path: "/tmp/y", Size: 5}
	if _, err := agent.ProposeApproval(ctx, log, "agent-2", "second", ref2); err != nil {
		t.Fatal(err)
	}
	log.Close()

	r, w, _ = os.Pipe()
	os.Stdout = w
	err = runApprovals([]string{"reject", "art-2", "no", "thanks"})
	w.Close()
	os.Stdout = orig
	var bufR bytes.Buffer
	_, _ = io.Copy(&bufR, r)
	if err != nil {
		t.Fatalf("reject: %v", err)
	}
	if !strings.Contains(bufR.String(), "rejected") {
		t.Errorf("expected rejected output; got %q", bufR.String())
	}
}

// --- exit (covers scrubProviderName via exit's path) ----------------

// We can't actually invoke os.Exit, but we can verify scrubProviderName's
// behaviour. Existing tests already cover it; add a smoke for the
// errPickerCancelled branch detection (without actually exiting).

func TestErrors_FramePickerCancelledIsDifferent(t *testing.T) {
	// Sanity: the two cancel sentinels in cmd/carlos shouldn't be the
	// same value (one comes from session picker, the other from frame
	// picker).
	if errors.Is(errPickerCancelled, errFramePickerCancelled) {
		t.Error("session picker cancel and frame picker cancel must differ")
	}
}

// --- Misc smoke tests for completeness -------------------------------

// TestSlugifyQuestionLength is a small extra to ensure the length cap
// holds across a wide variety of inputs; existing tests focus on edge
// cases.
func TestSlugifyQuestion_LengthMonotonic(t *testing.T) {
	for n := 0; n < 200; n += 17 {
		in := strings.Repeat("word ", n)
		got := slugifyQuestion(in)
		if len(got) > 60 {
			t.Errorf("slug too long (n=%d): %d", n, len(got))
		}
	}
}

// TestResolveSessionFromFlag_DBExistsNoSessions is a more focused
// regression for the "resume with non-empty DB but no sessions" path
// being exercised under HOME pointing to a fresh dir.
func TestResolveSessionFromFlag_DBExistsNoSessions(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	// Don't seed any sessions. The "continue" branch opens the DB and
	// returns ErrNoSessions, which becomes "" + nil to the caller.
	dbDir := filepath.Join(tmp, ".carlos")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	log, err := agent.OpenStateDB(filepath.Join(dbDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	log.Close()
	id, err := resolveSessionFromFlag("continue")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if id != "" {
		t.Errorf("expected empty id; got %q", id)
	}
}

// TestResolveSessionFromFlag_ContinueHappy seeds a session row and
// checks that resolveSessionFromFlag("continue") returns its ID.
// stubTool implements tools.Tool with a fixed name so we can register
// a tool under "web_search" / "web_fetch" that is NOT the expected
// concrete type, exercising buildResearchEngine's type-assertion
// failure paths.
type stubTool struct{ name string }

func (s *stubTool) Name() string        { return s.name }
func (s *stubTool) Description() string { return "stub" }
func (s *stubTool) Schema() []byte      { return []byte(`{}`) }
func (s *stubTool) Execute(_ context.Context, _ []byte) ([]byte, error) {
	return nil, nil
}

func TestBuildResearchEngine_WrongTypeForWebSearch(t *testing.T) {
	reg := mustToolsRegistry(t)
	reg.Register(&stubTool{name: "web_search"})
	if e := buildResearchEngine(stubProvider{}, "m", reg); e != nil {
		t.Errorf("expected nil engine when web_search is wrong type; got %+v", e)
	}
}

func TestBuildResearchEngine_WrongTypeForWebFetch(t *testing.T) {
	// First create a registry with a real web_search but a wrong-type
	// web_fetch. Use the default registry as the starting point.
	reg := newRegistryWithRealSearch(t)
	reg.Register(&stubTool{name: "web_fetch"})
	if e := buildResearchEngine(stubProvider{}, "m", reg); e != nil {
		t.Errorf("expected nil engine when web_fetch is wrong type; got %+v", e)
	}
}

// TestRunGatewayAdd_NoConfig surfaces the early-exit when no config
// exists yet. Covers the fs.ErrNotExist branch of runGatewayAdd.
func TestRunGatewayAdd_NoConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	err := runGatewayAdd(nil)
	if err == nil {
		t.Error("expected error when no config exists")
	}
	if err != nil && !strings.Contains(err.Error(), "carlos onboard") {
		t.Errorf("error should mention onboarding: %v", err)
	}
}

func TestRunGatewayAdd_MalformedConfig(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cfgDir := filepath.Join(tmp, ".carlos")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	// Bad yaml.
	if err := os.WriteFile(filepath.Join(cfgDir, "config.yaml"), []byte("not: ['valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runGatewayAdd(nil)
	if err == nil {
		t.Error("expected load error on malformed config")
	}
}

func TestRunGatewayAdd_DaemonDisabledWarnsButRunsFlow(t *testing.T) {
	// Config exists but daemon.enabled is false. runGatewayAdd warns
	// then constructs the flow and calls Run. Without a TTY the Run
	// call fails; accept any non-nil result as evidence the wiring
	// past the daemon-disabled check ran.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	cfgDir := filepath.Join(tmp, ".carlos")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		UserName:        "tester",
		DefaultProvider: "anthropic",
		Providers: map[string]config.ProviderConfig{
			"anthropic": {APIKey: "k", DefaultModel: "claude"},
		},
	}
	if err := config.Save(filepath.Join(cfgDir, "config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	// Capture stderr so the warning doesn't pollute test output.
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	// Defensively recover from any flow panic.
	func() {
		defer func() { _ = recover() }()
		_ = runGatewayAdd(nil)
	}()
	w.Close()
	os.Stderr = origStderr
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	// The daemon-disabled warning should appear in stderr.
	if !strings.Contains(buf.String(), "daemon is disabled") {
		t.Errorf("expected daemon-disabled warning; got %q", buf.String())
	}
}

// TestBuildOnboardOnlyFlow_ConfigLoadError covers the branch where
// config.Load returns an error that's NOT os.ErrNotExist (e.g. bad YAML).
func TestBuildOnboardOnlyFlow_ConfigLoadError(t *testing.T) {
	tmp := t.TempDir()
	bad := filepath.Join(tmp, "bad.yaml")
	// Malformed YAML.
	if err := os.WriteFile(bad, []byte("not: ['valid"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := buildOnboardOnlyFlow("name", bad)
	if err == nil {
		t.Error("expected error for malformed config")
	}
	if err != nil && !strings.Contains(err.Error(), "load config") {
		t.Errorf("error should mention load config: %v", err)
	}
}

// TestSaveResearchReport_WriteFailure exercises the os.WriteFile
// error path inside saveResearchReport by pre-creating the target
// filename as a directory (a directory can't be overwritten with
// WriteFile and writes return EISDIR/equivalent).
func TestSaveResearchReport_WriteFailure(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	ts := time.Date(2026, 6, 5, 10, 30, 0, 0, time.UTC)
	// Determine the path saveResearchReport will write to, then make it
	// a directory so WriteFile fails.
	dir := filepath.Join(tmp, ".carlos", "research")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	// slugifyQuestion("X") = "x", suffix is unix.
	target := filepath.Join(dir, "x-"+fmt.Sprintf("%d", ts.Unix())+".md")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := saveResearchReport("X", "body", "", ts)
	if err == nil {
		t.Error("expected write error when target is a directory")
	}
	if err != nil && !strings.Contains(err.Error(), "write") {
		t.Errorf("error should mention write: %v", err)
	}
}

func TestResolveSessionFromFlag_ContinueHappy(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	dbDir := filepath.Join(tmp, ".carlos")
	if err := os.MkdirAll(dbDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dbDir, "state.db")
	log, err := agent.OpenStateDB(dbPath)
	if err != nil {
		t.Fatalf("OpenStateDB: %v", err)
	}
	// Seed one top-level agent (a "session").
	now := time.Now().UTC().Truncate(time.Millisecond)
	row := agent.AgentRow{
		ID:              "01H-test-session",
		RootID:          "01H-test-session",
		State:           agent.StateRunning,
		Title:           "test",
		Model:           "claude",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastHeartbeatAt: now,
	}
	if err := log.InsertAgent(context.Background(), row); err != nil {
		t.Fatalf("InsertAgent: %v", err)
	}
	log.Close()

	id, err := resolveSessionFromFlag("continue")
	if err != nil {
		t.Fatalf("continue: %v", err)
	}
	if id != "01H-test-session" {
		t.Errorf("expected '01H-test-session'; got %q", id)
	}
}

// TestRunApprovals_AcceptUnknownIDFails proves the accept path surfaces
// errors from the agent layer.
func TestRunApprovals_AcceptEmptyIDFails(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	// Provide an empty artifact ID by passing only the subcommand and
	// an empty arg slot. Accept requires len(args) >= 2 so we need
	// args = ["accept", ""]. The agent layer returns an error.
	err := runApprovals([]string{"accept", ""})
	if err == nil {
		t.Error("expected error for empty artifact id")
	}
}

func TestRunApprovals_RejectEmptyIDFails(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	err := runApprovals([]string{"reject", "", "reason"})
	if err == nil {
		t.Error("expected error for empty artifact id")
	}
}

// TestRunApprovals_OpenStateDBFails forces an OpenStateDB failure by
// pointing HOME at a path that cannot be opened. Covers the error
// branch after OpenStateDB.
func TestRunApprovals_OpenStateDBFails(t *testing.T) {
	tmp := t.TempDir()
	// Pre-create a file at the state.db path so OpenStateDB hits an
	// error opening it as a directory.
	dbParentAsFile := filepath.Join(tmp, ".carlos")
	if err := os.WriteFile(dbParentAsFile, []byte("blocker"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", tmp)
	err := runApprovals([]string{"list"})
	if err == nil {
		t.Error("expected error when state.db path is blocked")
	}
}

// TestRunApprovals_ContextRespectedSmoke is a minimal smoke that the
// list path doesn't hang. Time-bounded to surface deadlocks early.
func TestRunApprovals_ListWithinDeadline(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	done := make(chan error, 1)
	go func() {
		r, w, _ := os.Pipe()
		orig := os.Stdout
		os.Stdout = w
		defer func() { os.Stdout = orig }()
		err := runApprovals([]string{"list"})
		w.Close()
		_, _ = io.Copy(io.Discard, r)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("list: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runApprovals list hung past deadline")
	}
}
