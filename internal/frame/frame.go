// Package frame owns the "personal" + N user-defined frames model. A frame
// bundles the per-session knobs that shift carlos's tone, defaults, and
// blast radius: which provider/model to use, which Obsidian subtree to
// search, which gateway routes apply, which orchestrator mode is default,
// and which cwd prefixes auto-pick the frame at fresh launch.
//
// Frames are NOT isolated stores. Cross-frame READ is free (notes_search
// returns hits across every frame, labelled). Cross-frame WRITE prompts
// with ReasonCrossFrameAllow (wired by Phase F-12 in internal/agent).
//
// The package intentionally has no dependency on internal/config so the
// config package can import frame.Config without creating a cycle.
package frame

import "regexp"

// Frame is one row in the config's `frames.list`.
//
// Field tags use JSON because miniyaml marshals via reflect against JSON
// struct tags (same convention as the rest of internal/config).
type Frame struct {
	Name string `json:"name"`
	// Glyph is the single visible character on the takeover-switcher tile
	// and inline picker. Defaults are filled in by DefaultGlyphFor when
	// the user omits it. ASCII / single-width Unicode only - emoji are
	// opt-in (user can override) but never required.
	Glyph string `json:"glyph,omitempty"`
	// Accent is one of the eight palette names (rust, slate, olive, teal,
	// plum, cream, sand, navy). Empty falls back to the theme's default
	// accent. The chat header pill, switcher tile border, and inline
	// picker glyph all colour with this.
	Accent string `json:"accent,omitempty"`
	// Provider names which entry in the shared providers pantry the frame
	// uses (e.g. "anthropic"). Empty means inherit from the parent config's
	// default_provider.
	Provider string `json:"provider,omitempty"`
	// Model is the per-frame model id (e.g. "claude-sonnet-4-6"). Empty
	// inherits the provider's default model.
	Model string `json:"model,omitempty"`
	// ProviderOverride lets a frame shadow specific keys from the shared
	// providers pantry - e.g. work uses LUDUS_ANTHROPIC_KEY for separate
	// billing while personal stays on ANTHROPIC_API_KEY. Resolved by
	// ResolveProvider.
	ProviderOverride map[string]ProviderOverride `json:"provider_override,omitempty"`
	// CwdHints are prefix patterns matched (with `filepath.Match` glob
	// support) against the symlink-resolved working directory at FRESH
	// launch. Empty list means "never auto-pick by cwd".
	CwdHints []string `json:"cwd_hints,omitempty"`
	// VaultSubtree is the path inside the configured Obsidian vault that
	// notes_* tools default to when this frame is active (e.g. "personal/"
	// or "work/"). Empty means whole vault.
	VaultSubtree string `json:"vault_subtree,omitempty"`
	// SystemPromptAppend is verbatim appended to the chat system prompt
	// for this frame (e.g. "Personal frame. Tone: relaxed. Verbosity:
	// low."). Cached per-frame at the provider boundary so swapping
	// frames doesn't invalidate the rest of the prefix.
	SystemPromptAppend string `json:"system_prompt_append,omitempty"`
	// Mode is the default orchestrator mode for this frame: "orchestrator",
	// "solo", or "tight". Empty falls back to the top-level modes.default
	// (or "solo" when that is also empty). Frame structure carries the
	// field today so Phase F-1 lands without the full orchestrator track.
	Mode string `json:"mode,omitempty"`
	// Capabilities is the per-frame capability map (Phase C-2). Keys are
	// capability names ("calendar", "email"); the inner map's "backend"
	// field selects which backend skill the loader picks. Treated as
	// opaque map[string]any so Phase C can flesh out without breaking the
	// schema today.
	Capabilities map[string]map[string]any `json:"capabilities,omitempty"`
}

// ProviderOverride shadows a single field on the shared providers pantry
// for one frame. All fields are optional; non-empty values override the
// corresponding field on the shared ProviderConfig at resolution time.
//
// Mirrors the shape of internal/config.ProviderConfig but lives here so
// the frame package stays standalone.
type ProviderOverride struct {
	APIKey       string `json:"api_key,omitempty"`
	BaseURL      string `json:"base_url,omitempty"`
	DefaultModel string `json:"default_model,omitempty"`
}

// Config is the on-disk shape of the `frames:` block in config.yaml.
//
// A missing or empty Config is fine - the loader synthesises a single
// "personal" frame from the legacy top-level provider/model. See
// MigrateFromLegacy.
type Config struct {
	// Default names the frame applied when no other signal picks one.
	// Empty falls back to "personal".
	Default string `json:"default,omitempty"`
	// Active records the last frame the user actively chose; persisted on
	// every switch. Empty means "use Default at startup".
	Active string `json:"active,omitempty"`
	// List is the ordered set of frames the user has configured. The
	// first frame in the list (after sorting by name when persisting) is
	// shown first in the switcher.
	List []Frame `json:"list,omitempty"`
}

// DefaultPersonalName is the conventional name for the default frame
// every install starts with. Hard-coded here because the migration path
// and the inline-picker fall-back both need a stable string.
const DefaultPersonalName = "personal"

// DefaultPersonalGlyph is the filled-dot glyph the design proposal pins
// as "you are here" for the personal frame.
const DefaultPersonalGlyph = "◉"

// DefaultPersonalAccent is the cream slot from the curated palette,
// matching the proposal's recommendation for the default frame.
const DefaultPersonalAccent = "cream"

// AccentPalette is the curated list of accent names a frame may pick.
// Anything outside this list is treated as "use theme default accent"
// rather than erroring - frames are user-edited YAML and we'd rather
// degrade than refuse to load.
var AccentPalette = []string{
	"rust", "slate", "olive", "teal",
	"plum", "cream", "sand", "navy",
}

// DefaultGlyphFor returns a sensible default glyph for a frame whose
// glyph is empty. Mapping comes from the proposal's "frame archetype"
// table; unrecognised names get the "+" sigil which paints visually as
// "new frame placeholder".
func DefaultGlyphFor(name string) string {
	switch name {
	case DefaultPersonalName:
		return DefaultPersonalGlyph
	case "work":
		return "▣"
	case "research":
		return "◈"
	case "writing":
		return "✦"
	}
	// Side-gig / client / outdoor frames default to the landmark glyph.
	if name == "client" || name == "side" || name == "ludus" {
		return "⛰"
	}
	return "+"
}

// validNameRE gates frame names: lowercase letter first, then up to 30
// more lowercase letters / digits / underscore / hyphen. Frame names are
// concatenated into ~/.carlos/frames/<name>/ filesystem paths and used
// as schedule.Frame keys / CARLOS_FRAME values; treating them as
// free-text invites path-escape (`../escape`) and unrenderable values.
var validNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,30}$`)

// IsValidName reports whether name is a legal frame identifier. Enforced
// at the new-frame wizard commit AND at config.Load so a hand-edited
// config.yaml with a bad name fails loudly instead of building paths
// that escape ~/.carlos/frames/.
func IsValidName(name string) bool {
	return validNameRE.MatchString(name)
}

// IsValidAccent reports whether the accent name is in the curated
// palette. Used by the new-frame wizard to gate the colour picker, not
// by Load - see comment on AccentPalette.
func IsValidAccent(name string) bool {
	for _, a := range AccentPalette {
		if a == name {
			return true
		}
	}
	return false
}

// NewPersonal returns a fresh personal Frame populated with the proposal
// defaults. Used by MigrateFromLegacy and by tests that need a zero-arg
// frame to work with.
//
// Mode defaults to ModeOrchestrator (was ModeSolo through v0.7.5). The
// solo default refused every Spawn, leaving the chat-side /agents
// surface empty and the `agent` delegation tool unreachable from a
// fresh install. A new user clicking "personal frame" was the most
// common path into carlos, and the most likely to hit "delegation is
// off" without context; flipping the default to orchestrator (cap 5)
// matches the legacy pre-modes behaviour and lets sub-agent spawn
// work out of the box. Users who want strict single-agent semantics
// can flip back to solo via `/mode solo` or in the frames editor.
func NewPersonal(provider, model string) Frame {
	return Frame{
		Name:     DefaultPersonalName,
		Glyph:    DefaultPersonalGlyph,
		Accent:   DefaultPersonalAccent,
		Provider: provider,
		Model:    model,
		Mode:     ModeOrchestrator,
	}
}

// Orchestrator-mode constants (per the 2026-06-06 orchestrator mode
// proposal). A frame's `mode` field accepts one of these three strings.
// Empty falls back to ModeOrchestrator at consumption time so partial
// or pre-v0.7.6 configs surface the same user-visible default as a
// freshly-onboarded install (NewPersonal also defaults to orchestrator;
// SPEC.md frames the "delegation on by default" stance). The safety
// backstop lives in SpawnCapFor, which still treats unknown modes as
// no-delegation so a corrupted frame cannot accidentally fan out.
const (
	// ModeSolo means carlos does the work itself; sub-agent delegation
	// is opt-in (the user explicitly invokes /agents). Best fit for
	// personal frames and small focused tasks.
	ModeSolo = "solo"
	// ModeTight means single-task focus. Like solo but with a hint to
	// the model that side-quests and tangents should be deferred. A
	// good fit for "I am pairing on one specific bug" sessions.
	ModeTight = "tight"
	// ModeOrchestrator means carlos delegates aggressively - large
	// problems get split across child agents that report back to the
	// parent. Best fit for work frames coordinating multiple workstreams.
	ModeOrchestrator = "orchestrator"
)

// IsValidMode reports whether the supplied string is one of the three
// supported modes. Used by the /mode slash to gate user input.
func IsValidMode(m string) bool {
	switch m {
	case ModeSolo, ModeTight, ModeOrchestrator:
		return true
	}
	return false
}

// EffectiveMode returns the mode the consumer should treat as active
// for a frame: the frame's own Mode when set, else the package default
// (ModeOrchestrator, matching NewPersonal). Pulled out so callers don't
// reimplement the fallback. The supervisor's SpawnCapFor stays
// independent and uses a safer no-delegation default for unknown modes
// — see its doc for the rationale.
func EffectiveMode(f Frame) string {
	if IsValidMode(f.Mode) {
		return f.Mode
	}
	return ModeOrchestrator
}

// SpawnCap* are the per-mode supervisor concurrency caps. Solo disables
// delegation outright (any Spawn attempt returns an error so the model
// sees the refusal as a tool_result and adjusts). Tight allows one
// in-flight child so the user can still ask carlos to fan out a single
// focused side task without losing the single-task-focus posture.
// Orchestrator preserves the legacy cap of 5.
const (
	SpawnCapSolo         = 0
	SpawnCapTight        = 1
	SpawnCapOrchestrator = 5
)

// SpawnCapFor returns the per-parent supervisor concurrency cap that
// matches mode. Unknown / empty modes fall back to the solo cap so a
// misconfigured frame defaults to the safest stance (no delegation)
// rather than silently allowing fan-out.
func SpawnCapFor(mode string) int {
	switch mode {
	case ModeOrchestrator:
		return SpawnCapOrchestrator
	case ModeTight:
		return SpawnCapTight
	case ModeSolo:
		return SpawnCapSolo
	}
	return SpawnCapSolo
}

// MigrateFromLegacy returns a Config with a synthetic personal frame
// derived from the supplied legacy provider + model. Idempotent: if
// existing already has a List, it is returned unchanged.
//
// Callers use this when loading a config that predates frames so the
// rest of the system can always assume a non-empty List.
func MigrateFromLegacy(existing Config, legacyProvider, legacyModel string) Config {
	if len(existing.List) > 0 {
		return existing
	}
	out := existing
	out.List = []Frame{NewPersonal(legacyProvider, legacyModel)}
	if out.Default == "" {
		out.Default = DefaultPersonalName
	}
	if out.Active == "" {
		out.Active = out.Default
	}
	return out
}

// Find returns a pointer to the named frame, or nil if absent.
// Returns a pointer rather than a value so callers can mutate in place
// during edit-flow wiring (Phase F-10); the slice itself is not resized.
func (c *Config) Find(name string) *Frame {
	if c == nil {
		return nil
	}
	for i := range c.List {
		if c.List[i].Name == name {
			return &c.List[i]
		}
	}
	return nil
}

// Names returns the ordered list of frame names. Convenience for the
// inline picker + the takeover-switcher tile order.
func (c *Config) Names() []string {
	if c == nil {
		return nil
	}
	out := make([]string, 0, len(c.List))
	for _, f := range c.List {
		out = append(out, f.Name)
	}
	return out
}

// ResolvedProvider is the post-merge view of which provider config the
// active frame should use at runtime. Returned by ResolveProvider.
type ResolvedProvider struct {
	Provider     string
	APIKey       string
	BaseURL      string
	DefaultModel string
	Model        string
}

// SharedProvider mirrors the fields of internal/config.ProviderConfig
// without importing it. ResolveProvider takes this shape so the frame
// package stays standalone.
type SharedProvider struct {
	APIKey       string
	BaseURL      string
	DefaultModel string
}

// ResolveProvider implements the pantry-with-overrides rule:
//
//	1. Pick the frame's Provider name; fall back to defaultProvider when empty.
//	2. Start with the shared pantry entry for that provider.
//	3. Apply the frame's ProviderOverride for the same vendor on top.
//	4. Model = frame.Model when set, else override.DefaultModel, else
//	   shared.DefaultModel.
//
// Returns ("", _, false) when neither the frame nor the default names a
// provider - callers prompt the user / re-run onboarding.
func ResolveProvider(
	f Frame,
	defaultProvider string,
	pantry map[string]SharedProvider,
) (ResolvedProvider, bool) {
	name := f.Provider
	if name == "" {
		name = defaultProvider
	}
	if name == "" {
		return ResolvedProvider{}, false
	}
	shared := pantry[name] // zero value if missing - caller decides
	out := ResolvedProvider{
		Provider:     name,
		APIKey:       shared.APIKey,
		BaseURL:      shared.BaseURL,
		DefaultModel: shared.DefaultModel,
	}
	if ov, ok := f.ProviderOverride[name]; ok {
		if ov.APIKey != "" {
			out.APIKey = ov.APIKey
		}
		if ov.BaseURL != "" {
			out.BaseURL = ov.BaseURL
		}
		if ov.DefaultModel != "" {
			out.DefaultModel = ov.DefaultModel
		}
	}
	switch {
	case f.Model != "":
		out.Model = f.Model
	case out.DefaultModel != "":
		out.Model = out.DefaultModel
	}
	return out, true
}
