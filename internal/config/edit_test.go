package config

import (
	"reflect"
	"testing"

	"github.com/georgebuilds/carlos/internal/frame"
)

func TestSetProvider(t *testing.T) {
	c := &Config{}
	if err := c.SetProvider("  ", ProviderConfig{}); err == nil {
		t.Error("blank provider name should error")
	}
	if err := c.SetProvider("anthropic", ProviderConfig{APIKey: "k", DefaultModel: "m"}); err != nil {
		t.Fatal(err)
	}
	pc, ok := c.Provider("anthropic")
	if !ok || pc.APIKey != "k" || pc.DefaultModel != "m" {
		t.Errorf("provider not stored: %+v ok=%v", pc, ok)
	}
	// upsert overwrites
	_ = c.SetProvider("anthropic", ProviderConfig{APIKey: "k2"})
	pc, _ = c.Provider("anthropic")
	if pc.APIKey != "k2" || pc.DefaultModel != "" {
		t.Errorf("upsert wrong: %+v", pc)
	}
}

func TestProviderNamesSorted(t *testing.T) {
	c := &Config{Providers: map[string]ProviderConfig{
		"openrouter": {}, "anthropic": {}, "ollama": {},
	}}
	got := c.ProviderNames()
	want := []string{"anthropic", "ollama", "openrouter"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ProviderNames = %v, want %v", got, want)
	}
}

func TestSetDefaultProvider(t *testing.T) {
	c := &Config{}
	c.SetDefaultProvider("  openai  ")
	if c.DefaultProvider != "openai" {
		t.Errorf("DefaultProvider = %q", c.DefaultProvider)
	}
}

func TestSetFrameProvider(t *testing.T) {
	c := &Config{Frames: frame.Config{List: []frame.Frame{{Name: "work"}}}}
	if err := c.SetFrameProvider("nope", "x", "y"); err == nil {
		t.Error("unknown frame should error")
	}
	if err := c.SetFrameProvider("work", " openrouter ", " claude-opus-4-8 "); err != nil {
		t.Fatal(err)
	}
	f := c.Frames.Find("work")
	if f.Provider != "openrouter" || f.Model != "claude-opus-4-8" {
		t.Errorf("frame provider/model wrong: %+v", f)
	}
}

func TestSetFrameProviderOverride(t *testing.T) {
	c := &Config{Frames: frame.Config{List: []frame.Frame{{Name: "work"}}}}
	if err := c.SetFrameProviderOverride("nope", "p", frame.ProviderOverride{APIKey: "k"}); err == nil {
		t.Error("unknown frame should error")
	}
	if err := c.SetFrameProviderOverride("work", "  ", frame.ProviderOverride{APIKey: "k"}); err == nil {
		t.Error("blank provider should error")
	}
	// set an override
	if err := c.SetFrameProviderOverride("work", "openrouter", frame.ProviderOverride{APIKey: "sk-work"}); err != nil {
		t.Fatal(err)
	}
	f := c.Frames.Find("work")
	if f.ProviderOverride["openrouter"].APIKey != "sk-work" {
		t.Errorf("override not set: %+v", f.ProviderOverride)
	}
	// a zero override clears it
	if err := c.SetFrameProviderOverride("work", "openrouter", frame.ProviderOverride{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.ProviderOverride["openrouter"]; ok {
		t.Errorf("zero override should clear the entry: %+v", f.ProviderOverride)
	}
}
