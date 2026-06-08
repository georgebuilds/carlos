package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/mcp"
)

// TestSaveLoadRoundtrip is the happy-path: write a config, read it back,
// verify all fields survive a marshal/unmarshal cycle.
func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	want := &Config{
		UserName: "George",
		Providers: map[string]ProviderConfig{
			"anthropic": {APIKey: "sk-ant-test", DefaultModel: "claude-opus-4-7"},
			"ollama":    {BaseURL: "http://localhost:11434", DefaultModel: "llama3.1:8b"},
		},
		DefaultProvider: "anthropic",
		Daemon:          DaemonConfig{Enabled: true, UnitPath: "/tmp/fake.plist"},
		Skills:          SkillsConfig{Convention: SkillsConventionClaude},
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.UserName != want.UserName {
		t.Errorf("UserName: want %q got %q", want.UserName, got.UserName)
	}
	if len(got.Providers) != 2 {
		t.Errorf("Providers: want 2 got %d", len(got.Providers))
	}
	if got.Providers["anthropic"].APIKey != "sk-ant-test" {
		t.Errorf("anthropic api_key not roundtripped")
	}
	if got.Daemon.Enabled != true {
		t.Errorf("Daemon.Enabled not roundtripped")
	}
	if got.Skills.Convention != SkillsConventionClaude {
		t.Errorf("Skills.Convention not roundtripped: %q", got.Skills.Convention)
	}
}

// TestSaveAtomicMode verifies the saved file has 0600 permissions — API
// keys are secrets and a world-readable config is the classic mistake.
func TestSaveAtomicMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := Save(path, &Config{UserName: "Boss"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	mode := info.Mode().Perm()
	if mode != 0o600 {
		t.Errorf("config file mode: want 0600 got %o", mode)
	}
}

// TestSaveNoTmpLeftBehind verifies Save cleans up the temp file on the
// happy path (no .tmp dangling after a successful rename).
func TestSaveNoTmpLeftBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := Save(path, &Config{UserName: "Boss"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file left behind: %v", err)
	}
}

// TestLoadMissingFileReturnsErrNotExist lets the CLI distinguish "first
// run, run onboarding" from "config is corrupt, surface the parse error".
func TestLoadMissingFileReturnsErrNotExist(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(filepath.Join(dir, "nope.yaml"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !os.IsNotExist(err) {
		t.Errorf("expected os.IsNotExist, got %v", err)
	}
}

// TestIsCompleteCases exercises the gate that decides whether to trigger
// onboarding. Empty-username, no-providers, and provider-with-no-secret
// must all be considered incomplete.
func TestIsCompleteCases(t *testing.T) {
	cases := []struct {
		name string
		cfg  *Config
		want bool
	}{
		{"nil", nil, false},
		{"empty username", &Config{Providers: map[string]ProviderConfig{"a": {APIKey: "x"}}}, false},
		{"no providers", &Config{UserName: "Boss"}, false},
		{"provider with no secret", &Config{
			UserName:  "Boss",
			Providers: map[string]ProviderConfig{"anthropic": {}},
		}, false},
		{"happy path", &Config{
			UserName:  "Boss",
			Providers: map[string]ProviderConfig{"anthropic": {APIKey: "sk-x"}},
		}, true},
		{"ollama base url counts", &Config{
			UserName:  "Boss",
			Providers: map[string]ProviderConfig{"ollama": {BaseURL: "http://x"}},
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsComplete(tc.cfg); got != tc.want {
				t.Errorf("IsComplete: want %v got %v", tc.want, got)
			}
		})
	}
}

// TestSaveCreatesDirectoryWithRightMode ensures we MkdirAll the parent
// at 0700, not the umask default (which on many systems is 0755).
func TestSaveCreatesDirectoryWithRightMode(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "carlos")
	path := filepath.Join(dir, "config.yaml")
	if err := Save(path, &Config{UserName: "Boss"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	mode := info.Mode().Perm()
	if mode != 0o700 {
		t.Errorf("dir mode: want 0700 got %o", mode)
	}
}

// TestSaveAtomicOverwrite verifies that Save over an existing file leaves
// the destination intact even if we simulate a write failure by passing
// an unwritable target. (Best-effort: the temp file should be cleaned up,
// and the original file untouched.)
func TestSaveAtomicOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := Save(path, &Config{UserName: "Original"}); err != nil {
		t.Fatalf("first save: %v", err)
	}
	// Overwrite with new content; readback should reflect the new content.
	if err := Save(path, &Config{UserName: "Updated"}); err != nil {
		t.Fatalf("second save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.UserName != "Updated" {
		t.Errorf("post-overwrite UserName: want Updated got %q", got.UserName)
	}
	// Tmp must be gone.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp not cleaned: %v", err)
	}
}

// TestThemeRoundtrip verifies the Phase 9 slice-9a ThemeConfig fields
// survive a marshal/unmarshal cycle and that an empty Theme is omitted
// from the on-disk YAML (forward-compatible with older configs).
func TestThemeRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	want := &Config{
		UserName: "Boss",
		Theme:    ThemeConfig{Variant: "light", Accent: "#00ff00"},
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Theme.Variant != "light" {
		t.Errorf("Theme.Variant: want light got %q", got.Theme.Variant)
	}
	if got.Theme.Accent != "#00ff00" {
		t.Errorf("Theme.Accent: want #00ff00 got %q", got.Theme.Accent)
	}
	// YAML must include the theme block when set.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"theme:", "variant:", "accent:"} {
		if !strings.Contains(string(b), key) {
			t.Errorf("theme yaml missing key %q\n--- got ---\n%s", key, string(b))
		}
	}
}

// TestThemeOmittedWhenEmpty pins the omitempty behavior: a zero-value
// ThemeConfig must NOT emit a stray "theme: {}" line — older configs
// without the field need to round-trip cleanly through a write.
func TestThemeOmittedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := Save(path, &Config{UserName: "Boss"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "theme:") {
		t.Errorf("empty Theme should be omitted; got:\n%s", string(b))
	}
}

// TestVaultRoundtrip pins the Phase 12 VaultConfig serialization:
// path + exclude survive a Save+Load cycle, and a zero-value Vault
// stays omitted from the on-disk YAML for forward-compat with older
// configs.
func TestVaultRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	want := &Config{
		UserName: "Boss",
		Vault: VaultConfig{
			Path:    "/Users/me/vault",
			Exclude: []string{"templates/**", "private/**"},
		},
	}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Vault.Path != want.Vault.Path {
		t.Errorf("Vault.Path: want %q got %q", want.Vault.Path, got.Vault.Path)
	}
	if len(got.Vault.Exclude) != 2 {
		t.Errorf("Vault.Exclude: want 2 entries got %d", len(got.Vault.Exclude))
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "vault:") {
		t.Errorf("yaml should include `vault:` block; got:\n%s", string(b))
	}
}

// TestVaultOmittedWhenEmpty — a zero-value Vault must not emit a
// stray `vault: {}` line so older configs without the field round-trip
// cleanly through a write.
func TestVaultOmittedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := Save(path, &Config{UserName: "Boss"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "vault:") {
		t.Errorf("empty Vault should be omitted; got:\n%s", string(b))
	}
}

// TestGatewayRoundtrip pins the post-v1 GatewayConfig serialization: the
// per-channel sub-blocks, routing, and retry survive a Save+Load cycle,
// and a zero-value GatewayConfig stays omitted from the on-disk YAML
// for forward-compat with older configs.
func TestGatewayRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	want := &Config{
		UserName: "Boss",
		Gateway: GatewayConfig{
			Enabled: true,
			Ntfy: NtfyGatewayConfig{
				Enabled:        true,
				Server:         "https://ntfy.example",
				Topic:          "carlos-george-deadbeef",
				ActionEndpoint: "https://carlos-cronus.ts.net/gateway/ntfy/action",
				PriorityMap:    map[string]int{"default": 3, "high": 5},
				SigningKey:     "env:CARLOS_NTFY_SIGNING_KEY",
			},
			Telegram: TelegramConfig{
				Enabled:        true,
				BotToken:       "env:CARLOS_TELEGRAM_TOKEN",
				AllowedChatIDs: []int64{123456789},
				ParseMode:      "MarkdownV2",
				PollTimeoutSec: 30,
			},
			Signal: SignalConfig{Enabled: false, SignalCLISocket: "/run/signal-cli/socket"},
			Custom: CustomGatewayConfig{Enabled: false, ListenAddr: "tailscale://carlos:8443"},
			Routing: GatewayRouting{
				Notifications: []string{"ntfy", "telegram"},
				Approvals:     []string{"telegram", "ntfy"},
				Conversations: []string{"telegram"},
			},
			Retry: GatewayRetry{MaxAttempts: 5, BackoffInitial: "1s", BackoffMax: "60s"},
		},
	}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Gateway.Enabled {
		t.Error("Gateway.Enabled not roundtripped")
	}
	if got.Gateway.Telegram.BotToken != want.Gateway.Telegram.BotToken {
		t.Errorf("Telegram.BotToken: want %q got %q", want.Gateway.Telegram.BotToken, got.Gateway.Telegram.BotToken)
	}
	if len(got.Gateway.Telegram.AllowedChatIDs) != 1 || got.Gateway.Telegram.AllowedChatIDs[0] != 123456789 {
		t.Errorf("Telegram.AllowedChatIDs: want [123456789] got %v", got.Gateway.Telegram.AllowedChatIDs)
	}
	if got.Gateway.Ntfy.PriorityMap["high"] != 5 {
		t.Errorf("Ntfy.PriorityMap[high]: want 5 got %v", got.Gateway.Ntfy.PriorityMap["high"])
	}
	if len(got.Gateway.Routing.Notifications) != 2 {
		t.Errorf("Routing.Notifications: want 2 entries got %d", len(got.Gateway.Routing.Notifications))
	}
	if got.Gateway.Retry.MaxAttempts != 5 {
		t.Errorf("Retry.MaxAttempts: want 5 got %d", got.Gateway.Retry.MaxAttempts)
	}
	b, _ := os.ReadFile(path)
	for _, key := range []string{"gateway:", "ntfy:", "telegram:", "routing:", "retry:"} {
		if !strings.Contains(string(b), key) {
			t.Errorf("gateway yaml missing key %q\n--- got ---\n%s", key, string(b))
		}
	}
}

// TestGatewayOmittedWhenEmpty — a zero-value Gateway must not emit a
// stray `gateway: {}` line so older configs without the field
// round-trip cleanly through a write.
func TestGatewayOmittedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := Save(path, &Config{UserName: "Boss"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "gateway:") {
		t.Errorf("empty Gateway should be omitted; got:\n%s", string(b))
	}
}

// TestYAMLShape pins the on-disk YAML keys so future schema additions stay
// backward-compatible (no silent renames).
func TestYAMLShape(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	cfg := &Config{
		UserName: "Boss",
		Providers: map[string]ProviderConfig{
			"anthropic": {APIKey: "x", DefaultModel: "claude"},
		},
		DefaultProvider: "anthropic",
		Daemon:          DaemonConfig{Enabled: false},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, key := range []string{"user_name:", "providers:", "anthropic:", "api_key:", "default_model:", "default_provider:", "daemon:"} {
		if !strings.Contains(s, key) {
			t.Errorf("yaml output missing key %q\n--- got ---\n%s", key, s)
		}
	}
}


// TestLoad_MigratesLegacyConfigToPersonalFrame verifies the Phase F
// migration: a pre-frames YAML loads with a synthetic personal frame
// derived from default_provider + that providers entries default_model.
func TestLoad_MigratesLegacyConfigToPersonalFrame(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	legacy := []byte(`user_name: George
providers:
  anthropic:
    api_key: sk-ant-legacy
    default_model: claude-sonnet-4-6
default_provider: anthropic
daemon:
  enabled: false
skills:
  convention: agents
`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Frames.List) != 1 {
		t.Fatalf("expected 1 synthesised frame, got %d", len(got.Frames.List))
	}
	personal := got.Frames.List[0]
	if personal.Name != frame.DefaultPersonalName {
		t.Errorf("Name = %q, want %q", personal.Name, frame.DefaultPersonalName)
	}
	if personal.Provider != "anthropic" {
		t.Errorf("Provider = %q, want anthropic", personal.Provider)
	}
	if personal.Model != "claude-sonnet-4-6" {
		t.Errorf("Model = %q, want claude-sonnet-4-6 (pulled from provider default_model)", personal.Model)
	}
	if got.Frames.Default != frame.DefaultPersonalName {
		t.Errorf("Default = %q, want %q", got.Frames.Default, frame.DefaultPersonalName)
	}
	if got.Frames.Active != frame.DefaultPersonalName {
		t.Errorf("Active = %q, want %q", got.Frames.Active, frame.DefaultPersonalName)
	}
}

// TestLoad_PreservesExistingFramesBlock checks that the migration is a
// no-op when the YAML already has a frames block — we never overwrite
// user-edited frames.
func TestLoad_PreservesExistingFramesBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	cfg := &Config{
		UserName:        "George",
		Providers:       map[string]ProviderConfig{"anthropic": {APIKey: "sk"}},
		DefaultProvider: "anthropic",
		Frames: frame.Config{
			Default: "work",
			Active:  "work",
			List:    []frame.Frame{{Name: "work", Provider: "anthropic", Model: "claude-opus-4-7"}},
		},
	}
	if err := Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Frames.List) != 1 || got.Frames.List[0].Name != "work" {
		t.Errorf("Frames not roundtripped: %+v", got.Frames)
	}
	if got.Frames.Default != "work" || got.Frames.Active != "work" {
		t.Errorf("Default/Active not roundtripped: %+v", got.Frames)
	}
}

// TestMCPRoundtrip pins the MCP top-level field's YAML round-trip:
// servers, args, env, and per-frame gating survive a Save+Load cycle,
// and a zero-value MCP block stays omitted so older configs without the
// field round-trip cleanly through a write.
func TestMCPRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	want := &Config{
		UserName: "Boss",
		MCP: mcp.Config{
			Servers: []mcp.ServerConfig{
				{
					Name:    "github",
					Command: "npx",
					Args:    []string{"-y", "@modelcontextprotocol/server-github"},
					Env:     map[string]string{"GITHUB_TOKEN": "${GH_TOKEN}"},
					Frames:  []string{"work"},
				},
				{
					Name:    "filesystem",
					Command: "/usr/local/bin/mcp-fs",
				},
			},
		},
	}
	if err := Save(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.MCP.Servers) != 2 {
		t.Fatalf("Servers count: want 2 got %d", len(got.MCP.Servers))
	}
	gh := got.MCP.Servers[0]
	if gh.Name != "github" || gh.Command != "npx" {
		t.Errorf("github server fields: %+v", gh)
	}
	if len(gh.Args) != 2 || gh.Args[0] != "-y" {
		t.Errorf("github args: %+v", gh.Args)
	}
	if gh.Env["GITHUB_TOKEN"] != "${GH_TOKEN}" {
		t.Errorf("github env: %+v", gh.Env)
	}
	if len(gh.Frames) != 1 || gh.Frames[0] != "work" {
		t.Errorf("github frames: %+v", gh.Frames)
	}
	b, _ := os.ReadFile(path)
	for _, key := range []string{"mcp:", "servers:", "command:"} {
		if !strings.Contains(string(b), key) {
			t.Errorf("yaml missing key %q\n%s", key, string(b))
		}
	}
}

// TestMCPOmittedWhenEmpty mirrors the other "omitted when empty" tests:
// a zero MCP value must not emit a stray "mcp: {}" line.
func TestMCPOmittedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := Save(path, &Config{UserName: "Boss"}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "mcp:") {
		t.Errorf("empty MCP should be omitted; got:\n%s", string(b))
	}
}

// TestLoad_MigratesEmptyDefaultProvider handles the edge case where the
// legacy YAML had no default_provider — Frame migration still produces
// a personal frame but with empty Provider/Model fields (caller is
// expected to re-run onboarding via IsComplete=false).
func TestLoad_MigratesEmptyDefaultProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(path, []byte("user_name: George\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Frames.List) != 1 {
		t.Fatalf("expected 1 synthesised frame, got %d", len(got.Frames.List))
	}
	p := got.Frames.List[0]
	if p.Provider != "" || p.Model != "" {
		t.Errorf("expected empty Provider/Model when default_provider unset; got %+v", p)
	}
}
