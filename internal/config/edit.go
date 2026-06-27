package config

import (
	"errors"
	"fmt"
	"strings"

	"github.com/georgebuilds/carlos/internal/frame"
)

// edit.go holds the small post-onboarding mutation helpers the `/config`
// settings overlay uses. They mutate the in-memory Config only; the
// caller persists with Save (the atomic temp+fsync+rename path). Keeping
// Save out of these makes them pure and unit-testable, and lets the
// overlay batch a few edits before one write.

// SetProvider upserts a pantry provider entry by name. A blank name is
// rejected so a stray empty key never lands in the map (it would resolve
// to a nameless, unusable provider). The map is lazily created.
func (c *Config) SetProvider(name string, pc ProviderConfig) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("config: provider name is empty")
	}
	if c.Providers == nil {
		c.Providers = map[string]ProviderConfig{}
	}
	c.Providers[name] = pc
	return nil
}

// Provider returns the pantry entry for name and whether it exists.
func (c *Config) Provider(name string) (ProviderConfig, bool) {
	pc, ok := c.Providers[name]
	return pc, ok
}

// ProviderNames returns the configured pantry provider names, sorted, so
// the settings list renders in a stable order regardless of map
// iteration.
func (c *Config) ProviderNames() []string {
	names := make([]string, 0, len(c.Providers))
	for n := range c.Providers {
		names = append(names, n)
	}
	sortStringsLocal(names)
	return names
}

// SetDefaultProvider records which pantry entry a frame falls back to when
// it does not pin its own provider. A blank value clears it.
func (c *Config) SetDefaultProvider(name string) {
	c.DefaultProvider = strings.TrimSpace(name)
}

// SetFrameProvider pins a frame's provider and model (either may be blank
// to inherit). Returns an error when the frame does not exist.
func (c *Config) SetFrameProvider(frameName, provider, model string) error {
	f := c.Frames.Find(frameName)
	if f == nil {
		return fmt.Errorf("config: no frame %q", frameName)
	}
	f.Provider = strings.TrimSpace(provider)
	f.Model = strings.TrimSpace(model)
	return nil
}

// SetFrameProviderOverride sets a frame's per-provider override. A zero
// override clears the entry (so the frame falls back to the shared pantry)
// rather than persisting an all-empty shadow. The map is lazily created.
func (c *Config) SetFrameProviderOverride(frameName, provider string, ov frame.ProviderOverride) error {
	f := c.Frames.Find(frameName)
	if f == nil {
		return fmt.Errorf("config: no frame %q", frameName)
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return errors.New("config: provider name is empty")
	}
	if ov == (frame.ProviderOverride{}) {
		delete(f.ProviderOverride, provider)
		return nil
	}
	if f.ProviderOverride == nil {
		f.ProviderOverride = map[string]frame.ProviderOverride{}
	}
	f.ProviderOverride[provider] = ov
	return nil
}

// sortStringsLocal is a tiny insertion sort kept local so this file does
// not pull in the sort package for one call site (mirrors the pattern in
// internal/mcp/config.go).
func sortStringsLocal(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
