package main

import (
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
)

func TestResolveProviderCreds_NoFrameUsesPantry(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"anthropic": {APIKey: "sk-pantry", DefaultModel: "claude-sonnet-4-6"},
		},
	}
	key, base, model := resolveProviderCreds(cfg, "anthropic", nil)
	if key != "sk-pantry" || model != "claude-sonnet-4-6" {
		t.Errorf("pantry path wrong: key=%q base=%q model=%q", key, base, model)
	}
}

func TestResolveProviderCreds_FrameOverrideShadowsAPIKey(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"anthropic": {APIKey: "sk-personal", DefaultModel: "claude-sonnet-4-6"},
		},
	}
	f := &frame.Frame{
		Name:     "work",
		Provider: "anthropic",
		ProviderOverride: map[string]frame.ProviderOverride{
			"anthropic": {APIKey: "sk-work-billing"},
		},
	}
	key, _, model := resolveProviderCreds(cfg, "anthropic", f)
	if key != "sk-work-billing" {
		t.Errorf("override should win; got key=%q", key)
	}
	if model != "claude-sonnet-4-6" {
		t.Errorf("default_model should fall through from pantry; got %q", model)
	}
}

func TestResolveProviderCreds_FrameOverrideShadowsBaseURL(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"ollama": {BaseURL: "http://default:11434"},
		},
	}
	f := &frame.Frame{
		Provider: "ollama",
		ProviderOverride: map[string]frame.ProviderOverride{
			"ollama": {BaseURL: "http://work:11434"},
		},
	}
	_, base, _ := resolveProviderCreds(cfg, "ollama", f)
	if base != "http://work:11434" {
		t.Errorf("override base_url should win; got %q", base)
	}
}

func TestResolveProviderCreds_FrameOverrideShadowsDefaultModel(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"anthropic": {APIKey: "sk-x", DefaultModel: "claude-sonnet-4-6"},
		},
	}
	f := &frame.Frame{
		Provider: "anthropic",
		ProviderOverride: map[string]frame.ProviderOverride{
			"anthropic": {DefaultModel: "claude-opus-4-7"},
		},
	}
	_, _, model := resolveProviderCreds(cfg, "anthropic", f)
	if model != "claude-opus-4-7" {
		t.Errorf("override default_model should win; got %q", model)
	}
}

func TestResolveProviderCreds_FrameWithoutOverrideKeyFallsThrough(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"anthropic": {APIKey: "sk-pantry"},
		},
	}
	f := &frame.Frame{
		Provider: "anthropic",
		ProviderOverride: map[string]frame.ProviderOverride{
			"openrouter": {APIKey: "sk-other"}, // different provider; should not apply
		},
	}
	key, _, _ := resolveProviderCreds(cfg, "anthropic", f)
	if key != "sk-pantry" {
		t.Errorf("unrelated override should not affect anthropic; got %q", key)
	}
}
