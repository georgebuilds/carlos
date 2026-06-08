package onboarding

import (
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
)

// TestEnsurePersonalFrame_SeedsFreshConfig pins the regression that
// otherwise leaves a post-onboarding chat in legacy single-shelf mode:
// when Run() returns a cfg without a frames block, the helper has to
// synthesise a "personal" frame derived from the chosen provider + model.
func TestEnsurePersonalFrame_SeedsFreshConfig(t *testing.T) {
	cfg := &config.Config{
		UserName:        "Boss",
		DefaultProvider: "anthropic",
		Providers: map[string]config.ProviderConfig{
			"anthropic": {APIKey: "sk-test", DefaultModel: "claude-opus-4-7"},
		},
	}
	ensurePersonalFrame(cfg)

	if len(cfg.Frames.List) != 1 {
		t.Fatalf("Frames.List = %d, want 1", len(cfg.Frames.List))
	}
	p := cfg.Frames.List[0]
	if p.Name != frame.DefaultPersonalName {
		t.Errorf("personal frame name = %q, want %q", p.Name, frame.DefaultPersonalName)
	}
	if cfg.Frames.Default != frame.DefaultPersonalName {
		t.Errorf("Frames.Default = %q, want %q", cfg.Frames.Default, frame.DefaultPersonalName)
	}
	if cfg.Frames.Active != frame.DefaultPersonalName {
		t.Errorf("Frames.Active = %q, want %q", cfg.Frames.Active, frame.DefaultPersonalName)
	}
}

// TestEnsurePersonalFrame_NoopWhenFramesPresent guards the partial
// (--only) re-onboard path: the caller passes an existing config whose
// frames are already wired, and the helper must not clobber them.
func TestEnsurePersonalFrame_NoopWhenFramesPresent(t *testing.T) {
	existing := frame.Frame{Name: "work", Provider: "openai", Model: "gpt-4o"}
	cfg := &config.Config{
		UserName:        "Boss",
		DefaultProvider: "anthropic",
		Frames: frame.Config{
			List:    []frame.Frame{existing},
			Default: "work",
			Active:  "work",
		},
	}
	ensurePersonalFrame(cfg)

	if len(cfg.Frames.List) != 1 || cfg.Frames.List[0].Name != "work" {
		t.Errorf("existing frames mutated: %+v", cfg.Frames.List)
	}
	if cfg.Frames.Default != "work" || cfg.Frames.Active != "work" {
		t.Errorf("existing default/active mutated: default=%q active=%q",
			cfg.Frames.Default, cfg.Frames.Active)
	}
}

// TestEnsurePersonalFrame_NilCfg covers the defensive nil-guard so a
// future caller that miswires the helper crashes nothing.
func TestEnsurePersonalFrame_NilCfg(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("nil cfg panicked: %v", r)
		}
	}()
	ensurePersonalFrame(nil)
}
