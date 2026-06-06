// Package config owns the on-disk schema for ~/.carlos/config.yaml.
//
// Writes are atomic (temp + fsync + rename) so a ctrl-c mid-onboarding never
// leaves a corrupt YAML file behind. Mode is 0600 (API keys are secrets);
// the parent directory is 0700.
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/miniyaml"
	"github.com/georgebuilds/carlos/internal/schedule"
)

// Config is the on-disk shape of ~/.carlos/config.yaml.
//
// New fields must default to zero-valued YAML so older configs load forward
// without rewriting; older clients ignore unknown fields by default.
type Config struct {
	UserName        string                    `json:"user_name"`
	Providers       map[string]ProviderConfig `json:"providers,omitempty"`
	DefaultProvider string                    `json:"default_provider,omitempty"`
	// Daemon is always serialized (no omitempty) so the on-disk schema
	// is stable for tooling that greps the YAML for daemon.enabled.
	Daemon DaemonConfig `json:"daemon"`
	// Skills always serialized so the convention preference is grep-able.
	Skills SkillsConfig `json:"skills"`
	// Schedules are the user's scheduled runs (Phase 8b). The daemon
	// reads this list at startup and on SIGHUP. The TUI's /schedule
	// add|list|rm slash commands edit the same list via config.Save.
	//
	// Emitted only when non-empty so older configs round-trip without
	// a stray empty `schedules: []` line.
	Schedules []schedule.Schedule `json:"schedules,omitempty"`
	// Theme is the user's color preference (Phase 9 slice 9a). All
	// fields default to zero-value, which triggers autodetect at TUI
	// startup; emitted only when non-empty so older configs round-trip
	// without a stray empty `theme: {}` line.
	Theme ThemeConfig `json:"theme,omitempty"`
	// Vault is the user's Obsidian-flavored markdown vault (Phase 12
	// slice 12a/12b). Path is set during onboarding (slice 12c); when
	// empty, the seven notes_* tools error with "vault not
	// configured" unless the per-call `vault:` override is supplied.
	// Emitted only when non-empty so older configs round-trip
	// without a stray empty `vault: {}` line.
	Vault VaultConfig `json:"vault,omitempty"`
	// Gateway is the messaging-broker config (Post-v1 phase G). All
	// adapters default to disabled; the broker fan-out is a no-op
	// until at least one channel is enabled + routing references it.
	// Emitted only when non-empty so older configs round-trip without
	// a stray empty `gateway: {}` line.
	Gateway GatewayConfig `json:"gateway,omitempty"`
	// Frames is the user's set of session contexts (Phase F). When the
	// block is absent or empty, Load() synthesises a single "personal"
	// frame from DefaultProvider + the matching providers entry, so the
	// rest of carlos can always assume a non-empty List. Emitted only
	// when non-empty so older configs round-trip without a stray empty
	// `frames: {}` line.
	Frames frame.Config `json:"frames,omitempty"`
}

// GatewayConfig is the on-disk shape of the gateway: block. Adapters
// each get a sub-block; routing maps OutboundKind → ordered channel
// preference; retry controls send-attempt cadence.
//
// The structs are mirrored 1:1 from the spec § Config shape; new
// fields land here, get a YAML round-trip test, and then are read by
// the daemon's gateway-construction path.
type GatewayConfig struct {
	// Enabled is the master switch. When false, the daemon does not
	// construct a Broker; the in-process CLI gateway still runs.
	Enabled  bool                `json:"enabled,omitempty"`
	Ntfy     NtfyGatewayConfig   `json:"ntfy,omitempty"`
	Telegram TelegramConfig      `json:"telegram,omitempty"`
	Signal   SignalConfig        `json:"signal,omitempty"`
	Custom   CustomGatewayConfig `json:"custom,omitempty"`
	Routing  GatewayRouting      `json:"routing,omitempty"`
	Retry    GatewayRetry        `json:"retry,omitempty"`
}

// NtfyGatewayConfig captures the ntfy adapter's per-instance state.
// Server may point at the public ntfy.sh or a self-hosted instance
// behind Tailscale Funnel; ActionEndpoint is the public URL the
// adapter exposes for action-button callbacks (signed by SigningKey).
type NtfyGatewayConfig struct {
	Enabled        bool              `json:"enabled,omitempty"`
	Server         string            `json:"server,omitempty"`
	Topic          string            `json:"topic,omitempty"`
	ActionEndpoint string            `json:"action_endpoint,omitempty"`
	Token          string            `json:"token,omitempty"`
	SigningKey     string            `json:"signing_key,omitempty"`
	PriorityMap    map[string]int    `json:"priority_map,omitempty"`
	ListenAddr     string            `json:"listen_addr,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
}

// TelegramConfig captures the Telegram Bot API adapter state. BotToken
// may be inlined or expressed as env:VARNAME for indirection (the
// daemon resolves this at load time).
type TelegramConfig struct {
	Enabled        bool    `json:"enabled,omitempty"`
	BotToken       string  `json:"bot_token,omitempty"`
	APIBaseURL     string  `json:"api_base_url,omitempty"`
	AllowedChatIDs []int64 `json:"allowed_chat_ids,omitempty"`
	ParseMode      string  `json:"parse_mode,omitempty"`
	PollTimeoutSec int     `json:"poll_timeout_sec,omitempty"`
}

// SignalConfig captures the signal-cli adapter state. Post-v1; the
// stub adapter ships disabled by default.
type SignalConfig struct {
	Enabled         bool   `json:"enabled,omitempty"`
	SignalCLISocket string `json:"signal_cli_socket,omitempty"`
	SenderNumber    string `json:"sender_number,omitempty"`
}

// CustomGatewayConfig captures the post-v1 custom-app adapter state.
// The Tailscale listen addr keeps the surface off the public internet.
type CustomGatewayConfig struct {
	Enabled    bool   `json:"enabled,omitempty"`
	ListenAddr string `json:"listen_addr,omitempty"`
	AuthToken  string `json:"auth_token,omitempty"`
}

// GatewayRouting is the YAML shape of the routing block. Mirror of
// gateway.RoutingConfig with string-typed channels (so the YAML is
// human-editable and the Source type stays in the gateway package).
type GatewayRouting struct {
	Notifications []string `json:"notifications,omitempty"`
	Approvals     []string `json:"approvals,omitempty"`
	Conversations []string `json:"conversations,omitempty"`
}

// GatewayRetry is the YAML shape of the retry block. Durations are
// strings ("1s", "60s") parsed via time.ParseDuration at the daemon
// boundary so the on-disk format stays readable.
type GatewayRetry struct {
	MaxAttempts    int    `json:"max_attempts,omitempty"`
	BackoffInitial string `json:"backoff_initial,omitempty"`
	BackoffMax     string `json:"backoff_max,omitempty"`
}

// VaultConfig captures the user's primary vault location + the glob
// patterns excluded from indexing. The same patterns are applied to
// any ad-hoc vault opened via the per-call `vault:` override.
//
// Path may use `~` for the home directory; the notes package canonicalises
// it (filepath.Abs + Clean) on first Open. An empty Path means "no vault
// configured" — tool calls without a per-call override return the
// configured-vault error envelope to the model.
//
// Exclude patterns use the same syntax as `filepath.Match`. The notes
// package additionally honors a trailing `/**` to mean "this directory
// and everything under it" (e.g. `templates/**`).
type VaultConfig struct {
	Path    string   `json:"path,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

// ThemeConfig captures the user's TUI color preferences. Both fields
// are optional; the empty value preserves the historical autodetect-
// to-dark behavior.
//
// Read by cmd/carlos at TUI mount and passed into theme.Load. See
// internal/theme for the resolution order and palette definitions.
type ThemeConfig struct {
	// Variant pins the palette: "dark" or "light". Empty string ("")
	// means autodetect via COLORFGBG, falling back to "dark". Any
	// other value is treated as autodetect (the theme package is
	// permissive — never produces a black-on-black surprise).
	Variant string `json:"variant,omitempty"`
	// Accent overrides the brand-blue Accent slot. Accepts
	// "#rrggbb"/"#rgb" hex or a decimal 0-255 ANSI palette index.
	// Empty string ("") keeps the variant's default accent.
	Accent string `json:"accent,omitempty"`
}

// ProviderConfig holds per-provider secrets and the user's default model pick.
//
// `api_key` applies to anthropic/openai/openrouter. `base_url` applies to
// ollama. The two are mutually exclusive in practice but the struct holds
// both so future providers (e.g. a self-hosted gateway with auth) can use
// either or both.
type ProviderConfig struct {
	APIKey       string `json:"api_key,omitempty"`
	BaseURL      string `json:"base_url,omitempty"`
	DefaultModel string `json:"default_model,omitempty"`
}

// DaemonConfig records the user's choice from the onboarding daemon screen.
// Phase 0.5 only captures intent; Phase 8 reads `enabled` and performs the
// platform-specific install, populating `unit_path` for later uninstall.
type DaemonConfig struct {
	Enabled  bool   `json:"enabled"`
	UnitPath string `json:"unit_path,omitempty"`
}

// SkillsConfig captures the user's preferred convention for *writing* new
// skills. Read paths are unaffected: carlos always loads from both
// .claude/skills/ AND .agents/skills/ (project and user level). See
// SPEC § Skill model § Convention paths.
//
// Convention values:
//   - "agents" — write to .agents/skills/<name>/SKILL.md (open standard, default)
//   - "claude" — write to .claude/skills/<name>/SKILL.md (Claude Code convention)
type SkillsConfig struct {
	Convention string `json:"convention"`
}

// SkillsConvention* are the valid values for SkillsConfig.Convention.
const (
	SkillsConventionAgents = "agents"
	SkillsConventionClaude = "claude"
)

// DefaultSkillsConvention is the default for the onboarding screen — open
// standard wins by default (user can flip).
const DefaultSkillsConvention = SkillsConventionAgents

// DefaultUserName is the prefilled value for the name screen.
const DefaultUserName = "Boss"

// DefaultPath returns ~/.carlos/config.yaml using the OS user's home dir.
// Falls back to ".carlos/config.yaml" (relative) if the home dir cannot be
// resolved; callers can override with CARLOS_CONFIG for tests.
func DefaultPath() string {
	if env := os.Getenv("CARLOS_CONFIG"); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".carlos", "config.yaml")
	}
	return filepath.Join(home, ".carlos", "config.yaml")
}

// DefaultDir returns the parent directory for DefaultPath (i.e. ~/.carlos).
func DefaultDir() string {
	return filepath.Dir(DefaultPath())
}

// Load parses the YAML at path. Returns (nil, os.ErrNotExist) — wrapped — if
// the file is absent, so callers can distinguish "no config yet, run
// onboarding" from "config exists but failed to parse".
//
// On a successful parse, Load runs the Phase F migration: if cfg.Frames.List
// is empty, a single "personal" frame is synthesised from DefaultProvider +
// the matching ProviderConfig.DefaultModel. Pre-frames YAML therefore
// loads forward without rewriting; the file gets a `frames:` block on the
// next Save (which the onboarding flow + slash commands trigger).
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := miniyaml.UnmarshalStruct(b, &cfg); err != nil {
		return nil, fmt.Errorf("config: parse %s: %w", path, err)
	}
	cfg.Frames = migrateFrames(cfg)
	return &cfg, nil
}

// migrateFrames synthesises a "personal" frame from the legacy top-level
// provider + model when the on-disk Config has no frames block. Pure
// function so the same path covers both Load() and tests.
func migrateFrames(cfg Config) frame.Config {
	model := ""
	if cfg.DefaultProvider != "" {
		if pc, ok := cfg.Providers[cfg.DefaultProvider]; ok {
			model = pc.DefaultModel
		}
	}
	return frame.MigrateFromLegacy(cfg.Frames, cfg.DefaultProvider, model)
}

// Save writes cfg to path atomically: write to "<path>.tmp", fsync, rename
// over the destination. Creates the parent directory (mode 0700) if missing.
// File mode is 0600.
//
// Atomic-rename guarantees on POSIX (Darwin/Linux) ensure callers see either
// the old file contents or the new file contents — never a half-written one,
// even on ctrl-c or panic mid-write.
func Save(path string, cfg *Config) error {
	if cfg == nil {
		return errors.New("config: Save called with nil cfg")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("config: mkdir %s: %w", dir, err)
	}
	// Tighten dir mode in case it already existed with looser perms.
	_ = os.Chmod(dir, 0o700)

	data, err := miniyaml.MarshalStruct(cfg)
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}

	tmp := path + ".tmp"
	// O_TRUNC handles stale tmp files from a prior interrupted save.
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("config: open tmp %s: %w", tmp, err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("config: write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("config: fsync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("config: close tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("config: rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// IsComplete returns true iff the config has the minimum onboarding fields
// set: a user name plus at least one provider with an API key or base URL.
// A config that fails this check should re-trigger the onboarding flow.
func IsComplete(cfg *Config) bool {
	if cfg == nil || cfg.UserName == "" {
		return false
	}
	if len(cfg.Providers) == 0 {
		return false
	}
	for _, p := range cfg.Providers {
		if p.APIKey != "" || p.BaseURL != "" {
			return true
		}
	}
	return false
}

// Exists reports whether path can be opened for reading. Used by `carlos
// onboard` to drive the overwrite-confirm prompt.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// readAllClose is a small helper for callers that already hold an open
// file; not currently used elsewhere but kept for symmetry with Save.
func readAllClose(r io.ReadCloser) ([]byte, error) {
	defer r.Close()
	return io.ReadAll(r)
}

var _ = readAllClose // silence unused-warning in the rare case io changes
