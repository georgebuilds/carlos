package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
)

func newAboutTool() *CarlosAboutTool {
	return NewCarlosAboutTool(
		config.VaultConfig{Path: "/Volumes/nas/carlos-vault", Exclude: []string{"private/**"}},
		frame.Config{
			Default: "personal",
			Active:  "personal",
			List: []frame.Frame{
				{
					Name:         "personal",
					Glyph:        "◉",
					Accent:       "cream",
					Provider:     "anthropic",
					Model:        "claude-sonnet-4-6",
					Mode:         "solo",
					VaultSubtree: "personal",
					CwdHints:     []string{"~/Code/anneal"},
					Capabilities: map[string]map[string]any{
						"calendar": {"backend": "apple-calendar"},
					},
				},
				{
					Name:         "work",
					Glyph:        "▣",
					Accent:       "rust",
					Provider:     "anthropic",
					Mode:         "orchestrator",
					VaultSubtree: "work",
					CwdHints:     []string{"~/Code/ludus*"},
					Capabilities: map[string]map[string]any{
						"calendar": {"backend": "caldav"},
						"email":    {"backend": "fastmail-imap"},
					},
				},
			},
		},
		"personal",
		map[string]ProviderSummary{
			"anthropic": {HasKey: true, DefaultModel: "claude-sonnet-4-6"},
		},
		"George",
	)
}

func runAbout(t *testing.T, tool *CarlosAboutTool, section string) carlosAboutResponse {
	t.Helper()
	in, _ := json.Marshal(carlosAboutInput{Section: section})
	out, err := tool.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("carlos_about: %v", err)
	}
	var resp carlosAboutResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestCarlosAbout_FullEnvelope(t *testing.T) {
	resp := runAbout(t, newAboutTool(), "")
	if resp.User != "George" {
		t.Errorf("user = %q, want George", resp.User)
	}
	if resp.Vault == nil || resp.Vault.Path == "" {
		t.Errorf("vault should be populated; got %+v", resp.Vault)
	}
	if resp.Active == nil || resp.Active.Name != "personal" {
		t.Errorf("active frame should be personal; got %+v", resp.Active)
	}
	if len(resp.Frames) != 2 {
		t.Errorf("frames len = %d, want 2", len(resp.Frames))
	}
	if resp.Capabilities["calendar"] != "apple-calendar" {
		t.Errorf("capabilities should reflect active frame; got %+v", resp.Capabilities)
	}
	if _, ok := resp.Providers["anthropic"]; !ok {
		t.Errorf("providers should include anthropic; got %+v", resp.Providers)
	}
}

func TestCarlosAbout_SectionUser(t *testing.T) {
	resp := runAbout(t, newAboutTool(), "user")
	if resp.User != "George" {
		t.Errorf("user = %q, want George", resp.User)
	}
	if resp.Vault != nil || resp.Active != nil || len(resp.Frames) != 0 {
		t.Errorf("section=user should leave other fields zero; got %+v", resp)
	}
}

func TestCarlosAbout_SectionVault(t *testing.T) {
	resp := runAbout(t, newAboutTool(), "vault")
	if resp.Vault == nil || resp.Vault.Path != "/Volumes/nas/carlos-vault" {
		t.Errorf("vault wrong: %+v", resp.Vault)
	}
	if len(resp.Vault.Exclude) != 1 || resp.Vault.Exclude[0] != "private/**" {
		t.Errorf("exclude not surfaced; got %+v", resp.Vault.Exclude)
	}
	if resp.User != "" {
		t.Errorf("section=vault should not surface user; got %q", resp.User)
	}
}

func TestCarlosAbout_SectionActive(t *testing.T) {
	resp := runAbout(t, newAboutTool(), "active")
	if resp.Active == nil {
		t.Fatal("active should be populated")
	}
	if resp.Active.Name != "personal" {
		t.Errorf("active = %q, want personal", resp.Active.Name)
	}
	if resp.Active.Mode != "solo" {
		t.Errorf("mode = %q, want solo", resp.Active.Mode)
	}
	if resp.Active.VaultSubtree != "personal" {
		t.Errorf("vault_subtree = %q, want personal", resp.Active.VaultSubtree)
	}
}

func TestCarlosAbout_SectionFrames(t *testing.T) {
	resp := runAbout(t, newAboutTool(), "frames")
	if len(resp.Frames) != 2 {
		t.Errorf("got %d frames, want 2", len(resp.Frames))
	}
	var work *frameSummary
	for i := range resp.Frames {
		if resp.Frames[i].Name == "work" {
			work = &resp.Frames[i]
		}
	}
	if work == nil {
		t.Fatal("work frame missing from list")
	}
	if work.Mode != "orchestrator" {
		t.Errorf("work mode = %q, want orchestrator", work.Mode)
	}
}

func TestCarlosAbout_SectionCapabilities(t *testing.T) {
	resp := runAbout(t, newAboutTool(), "capabilities")
	if resp.Capabilities["calendar"] != "apple-calendar" {
		t.Errorf("active frame's capabilities should surface; got %+v", resp.Capabilities)
	}
	if resp.Active != nil {
		t.Errorf("section=capabilities should not include active")
	}
}

func TestCarlosAbout_SectionProviders(t *testing.T) {
	resp := runAbout(t, newAboutTool(), "providers")
	p, ok := resp.Providers["anthropic"]
	if !ok {
		t.Fatal("providers should include anthropic")
	}
	if !p.HasKey {
		t.Error("HasKey should be true")
	}
}

func TestCarlosAbout_UnknownSectionErrors(t *testing.T) {
	tool := newAboutTool()
	b, _ := json.Marshal(carlosAboutInput{Section: "garbage"})
	if _, err := tool.Execute(context.Background(), b); err == nil {
		t.Error("unknown section should reject")
	} else if !strings.Contains(err.Error(), "unknown section") {
		t.Errorf("error should mention 'unknown section'; got %v", err)
	}
}

func TestCarlosAbout_NoInputReturnsFull(t *testing.T) {
	tool := newAboutTool()
	out, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var resp carlosAboutResponse
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.User == "" || resp.Vault == nil || resp.Active == nil {
		t.Errorf("nil input should return full envelope; got %+v", resp)
	}
}

func TestCarlosAbout_ProviderSummariesNeverIncludesAPIKey(t *testing.T) {
	summaries := ProviderSummariesFromConfig(map[string]config.ProviderConfig{
		"anthropic": {APIKey: "sk-very-secret", DefaultModel: "claude-sonnet-4-6"},
	})
	raw, _ := json.Marshal(summaries)
	if strings.Contains(string(raw), "sk-very-secret") {
		t.Errorf("API key must NEVER appear in carlos_about output; got %s", raw)
	}
	if strings.Contains(string(raw), "api_key") {
		t.Errorf("JSON should not even mention api_key field; got %s", raw)
	}
	if !summaries["anthropic"].HasKey {
		t.Errorf("HasKey should be true when APIKey is set")
	}
}

func TestCarlosAbout_NoFramesWiredOmitsActiveAndFrames(t *testing.T) {
	tool := NewCarlosAboutTool(
		config.VaultConfig{Path: "/v"},
		frame.Config{},
		"",
		nil,
		"Boss",
	)
	resp := runAbout(t, tool, "")
	if resp.Active != nil {
		t.Errorf("no frames wired -> active should be nil; got %+v", resp.Active)
	}
	if len(resp.Frames) != 0 {
		t.Errorf("no frames wired -> frames should be empty; got %+v", resp.Frames)
	}
	if resp.User != "Boss" {
		t.Errorf("user should still surface; got %q", resp.User)
	}
}
