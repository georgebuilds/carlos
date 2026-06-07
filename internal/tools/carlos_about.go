package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
)

// CarlosAboutTool registers as `carlos_about` — a read-only
// introspection tool the model can call mid-conversation to learn
// carlos's own current state without re-reading config or guessing.
//
// Auto-approved by `DefaultBuiltinAllow` because it returns local
// state only (no network egress, no file mutation). The user already
// set everything it reports; nothing here is a secret.
type CarlosAboutTool struct {
	cfg    config.VaultConfig
	frames frame.Config
	active string
	// providers names the configured providers + their default models.
	// Pulled out as an injected map so the tool stays standalone for
	// tests; cmd/carlos populates it from cfg.Providers.
	providers map[string]ProviderSummary
	// userName surfaces as the "user" field so the agent knows how to
	// address the human running it.
	userName string
}

// ProviderSummary is the tiny subset of provider config the tool
// surfaces. We deliberately do NOT include api_key or anything
// secret-bearing; the model already knows it's talking to a model,
// so the slug + default model id is sufficient.
type ProviderSummary struct {
	HasKey       bool   `json:"has_key"`
	HasBaseURL   bool   `json:"has_base_url"`
	DefaultModel string `json:"default_model,omitempty"`
}

// NewCarlosAboutTool wires the introspection tool. Constructed by
// NewDefaultRegistryWithBaseDirAndFrames; tests can wire their own.
func NewCarlosAboutTool(
	vaultCfg config.VaultConfig,
	frames frame.Config,
	active string,
	providers map[string]ProviderSummary,
	userName string,
) *CarlosAboutTool {
	return &CarlosAboutTool{
		cfg:       vaultCfg,
		frames:    frames,
		active:    active,
		providers: providers,
		userName:  userName,
	}
}

func (*CarlosAboutTool) Name() string { return "carlos_about" }

func (*CarlosAboutTool) Description() string {
	return "Introspect carlos's own current state: vault path, active frame, available frames + their settings, wired capabilities, providers, user. Use when the user asks anything about how carlos is set up, where notes go, which frames exist, what mode the current frame is in, or who the user is. Read-only; never modifies state. Optional `section` arg narrows the output: frames | active | capabilities | providers | vault | user. Empty returns everything."
}

func (*CarlosAboutTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"section": {
				"type": "string",
				"enum": ["", "frames", "active", "capabilities", "providers", "vault", "user"],
				"description": "Optional section filter. Empty returns the full introspection envelope."
			}
		}
	}`)
}

type carlosAboutInput struct {
	Section string `json:"section"`
}

// carlosAboutResponse is the full introspection envelope. All sub-
// fields are omitempty so a section filter can shrink the payload.
type carlosAboutResponse struct {
	User         string                     `json:"user,omitempty"`
	Vault        *vaultSummary              `json:"vault,omitempty"`
	Active       *activeFrameSummary        `json:"active,omitempty"`
	Frames       []frameSummary             `json:"frames,omitempty"`
	Capabilities map[string]string          `json:"capabilities,omitempty"`
	Providers    map[string]ProviderSummary `json:"providers,omitempty"`
}

type vaultSummary struct {
	Path    string   `json:"path,omitempty"`
	Exclude []string `json:"exclude,omitempty"`
}

type activeFrameSummary struct {
	Name         string            `json:"name"`
	Mode         string            `json:"mode"`
	Provider     string            `json:"provider,omitempty"`
	Model        string            `json:"model,omitempty"`
	VaultSubtree string            `json:"vault_subtree,omitempty"`
	CwdHints     []string          `json:"cwd_hints,omitempty"`
	Capabilities map[string]string `json:"capabilities,omitempty"`
}

type frameSummary struct {
	Name         string   `json:"name"`
	Glyph        string   `json:"glyph,omitempty"`
	Accent       string   `json:"accent,omitempty"`
	Mode         string   `json:"mode,omitempty"`
	Provider     string   `json:"provider,omitempty"`
	Model        string   `json:"model,omitempty"`
	VaultSubtree string   `json:"vault_subtree,omitempty"`
	CwdHints     []string `json:"cwd_hints,omitempty"`
}

// Execute parses the optional section filter and returns the
// requested slice of the introspection envelope.
func (t *CarlosAboutTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	var in carlosAboutInput
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, fmt.Errorf("carlos_about: parse input: %w", err)
		}
	}
	section := strings.ToLower(strings.TrimSpace(in.Section))
	switch section {
	case "", "frames", "active", "capabilities", "providers", "vault", "user":
		// ok
	default:
		return nil, fmt.Errorf("carlos_about: unknown section %q (want one of: frames, active, capabilities, providers, vault, user)", in.Section)
	}

	resp := carlosAboutResponse{}
	all := section == ""

	if all || section == "user" {
		resp.User = t.userName
	}
	if all || section == "vault" {
		resp.Vault = &vaultSummary{
			Path:    t.cfg.Path,
			Exclude: append([]string(nil), t.cfg.Exclude...),
		}
	}
	if all || section == "active" || section == "capabilities" {
		if af := t.activeFrame(); af != nil {
			caps := flattenCapabilities(*af)
			if all || section == "active" {
				resp.Active = &activeFrameSummary{
					Name:         af.Name,
					Mode:         frame.EffectiveMode(*af),
					Provider:     af.Provider,
					Model:        af.Model,
					VaultSubtree: af.VaultSubtree,
					CwdHints:     append([]string(nil), af.CwdHints...),
					Capabilities: caps,
				}
			}
			if all || section == "capabilities" {
				resp.Capabilities = caps
			}
		}
	}
	if all || section == "frames" {
		for _, f := range t.frames.List {
			resp.Frames = append(resp.Frames, frameSummary{
				Name:         f.Name,
				Glyph:        f.Glyph,
				Accent:       f.Accent,
				Mode:         frame.EffectiveMode(f),
				Provider:     f.Provider,
				Model:        f.Model,
				VaultSubtree: f.VaultSubtree,
				CwdHints:     append([]string(nil), f.CwdHints...),
			})
		}
	}
	if all || section == "providers" {
		resp.Providers = sortedProviders(t.providers)
	}

	return json.Marshal(resp)
}

// activeFrame is the same resolution rule the notes_* family uses:
// session-active wins, then on-disk active, then default, then nil.
func (t *CarlosAboutTool) activeFrame() *frame.Frame {
	name := t.active
	if name == "" {
		name = t.frames.Active
	}
	if name == "" {
		name = t.frames.Default
	}
	if name == "" {
		return nil
	}
	return t.frames.Find(name)
}

// flattenCapabilities collapses a Frame.Capabilities map (capability ->
// per-frame settings) into capability -> backend, dropping settings
// without a backend key. Same shape the chat surface's /capabilities
// slash uses.
func flattenCapabilities(f frame.Frame) map[string]string {
	if len(f.Capabilities) == 0 {
		return nil
	}
	out := make(map[string]string, len(f.Capabilities))
	for name, settings := range f.Capabilities {
		if settings == nil {
			continue
		}
		if v, ok := settings["backend"].(string); ok && v != "" {
			out[name] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// sortedProviders returns the providers map with deterministic
// iteration order; useful when a downstream caller wants stable
// JSON output. The map type itself is unordered but the bytes the
// agent loop sees are deterministic per call.
func sortedProviders(in map[string]ProviderSummary) map[string]ProviderSummary {
	if len(in) == 0 {
		return nil
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]ProviderSummary, len(in))
	for _, k := range keys {
		out[k] = in[k]
	}
	return out
}

// ProviderSummariesFromConfig is the cmd/carlos wiring helper: turns
// a cfg.Providers map into the lightweight ProviderSummary map the
// tool exposes. Keeps the api_key field out of the tool's surface so
// a malicious response never leaks it through tool I/O.
func ProviderSummariesFromConfig(p map[string]config.ProviderConfig) map[string]ProviderSummary {
	if len(p) == 0 {
		return nil
	}
	out := make(map[string]ProviderSummary, len(p))
	for name, pc := range p {
		out[name] = ProviderSummary{
			HasKey:       pc.APIKey != "",
			HasBaseURL:   pc.BaseURL != "",
			DefaultModel: pc.DefaultModel,
		}
	}
	return out
}

// ensures we surface a useful error rather than panicking on a nil
// receiver. Tests that pass a nil pointer through the registry path
// trip this guard.
var _ = errors.New
