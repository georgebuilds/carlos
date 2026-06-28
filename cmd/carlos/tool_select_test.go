package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/mcp"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/tools"
)

type ftool struct{ name string }

func (f ftool) Name() string                                    { return f.name }
func (f ftool) Description() string                             { return "" }
func (f ftool) Schema() []byte                                  { return nil }
func (f ftool) Execute(context.Context, []byte) ([]byte, error) { return nil, nil }

func toolSpecNames(s []providers.ToolSpec) []string {
	out := make([]string, len(s))
	for i, sp := range s {
		out[i] = sp.Name
	}
	return out
}

func TestNewToolSelector_AvailabilityFilter(t *testing.T) {
	cfg := mcp.Config{Servers: []mcp.ServerConfig{
		{Name: "digitalocean", Command: "x", Tools: []string{"a"}}, // only "a" exposed
	}}
	sel := newToolSelector(cfg, "openrouter", nil)
	in := []providers.ToolSpec{{Name: "read"}, {Name: "digitalocean__a"}, {Name: "digitalocean__b"}}
	out := sel(in, "claude-opus-4-8") // no cap for claude
	got := strings.Join(toolSpecNames(out), ",")
	if got != "read,digitalocean__a" {
		t.Errorf("availability filter got %q want read,digitalocean__a", got)
	}
}

func TestNewToolSelector_CapAndLog(t *testing.T) {
	var buf strings.Builder
	sel := newToolSelector(mcp.Config{}, "openrouter", &buf)
	in := make([]providers.ToolSpec, 0, 210)
	for i := 0; i < 10; i++ {
		in = append(in, providers.ToolSpec{Name: fmt.Sprintf("b%02d", i)})
	}
	for i := 0; i < 200; i++ {
		in = append(in, providers.ToolSpec{Name: fmt.Sprintf("do__t%03d", i)})
	}
	out := sel(in, "x-ai/grok-4") // caps at 200
	if len(out) != 200 {
		t.Errorf("capped to %d want 200", len(out))
	}
	if !strings.Contains(buf.String(), "dropped 10 tool(s)") {
		t.Errorf("drop not logged: %q", buf.String())
	}
	// No cap, no log.
	buf.Reset()
	out = sel(in, "claude-opus-4-8")
	if len(out) != 210 || buf.Len() != 0 {
		t.Errorf("uncapped model should pass all and not log: len=%d log=%q", len(out), buf.String())
	}
}

func TestMCPAvailability_ExposedAndSet(t *testing.T) {
	a := newMCPAvailability(mcp.Config{Servers: []mcp.ServerConfig{
		{Name: "do", Tools: []string{"a"}},
		{Name: "home"}, // empty allowlist => all exposed
	}})
	if !a.ToolExposed("read") { // built-in
		t.Error("built-in must be exposed")
	}
	if !a.ToolExposed("do__a") || a.ToolExposed("do__b") {
		t.Error("allowlist not applied")
	}
	if !a.ToolExposed("home__anything") {
		t.Error("empty allowlist => expose all")
	}
	// set replaces the allowlist; empty clears it (expose all).
	a.set("do", []string{"b"})
	if a.ToolExposed("do__a") || !a.ToolExposed("do__b") {
		t.Error("set did not replace allowlist")
	}
	a.set("do", nil)
	if !a.ToolExposed("do__a") {
		t.Error("clearing allowlist should expose all")
	}
}

// TestMCPAvailability_ConcurrentAccess exercises the lock under -race: a
// reader (agent-loop analog) and a writer (UI-goroutine analog) hit the same
// snapshot. Without the mutex this trips the race detector.
func TestMCPAvailability_ConcurrentAccess(t *testing.T) {
	a := newMCPAvailability(mcp.Config{Servers: []mcp.ServerConfig{{Name: "do", Tools: []string{"a"}}}})
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			a.set("do", []string{"a", "b"})
			a.set("do", nil)
		}
		close(done)
	}()
	for i := 0; i < 1000; i++ {
		_ = a.ToolExposed("do__a")
	}
	<-done
}

func TestMCPManager_SetAllowUpdatesAvailability(t *testing.T) {
	mm, _ := mgrFixture()
	mm.avail = newMCPAvailability(mm.cfg.MCP)
	if mm.avail.ToolExposed("digitalocean__database_create") {
		t.Fatal("precondition: database_create starts hidden")
	}
	if err := mm.SetAllow("digitalocean", []string{"droplet_list", "database_create"}); err != nil {
		t.Fatal(err)
	}
	if !mm.avail.ToolExposed("digitalocean__database_create") {
		t.Error("SetAllow must update the live availability snapshot")
	}
}

// Servers configured but none AutoApprove must yield nil, not an empty map, so
// the nil-vs-empty contract matches the no-servers path.
func TestMCPAutoApproveSet_NilWhenNoneAuto(t *testing.T) {
	got := mcpAutoApproveSet(mcp.Config{Servers: []mcp.ServerConfig{
		{Name: "do"}, {Name: "home"},
	}})
	if got != nil {
		t.Errorf("no auto-approved servers must return nil, got %v", got)
	}
}

func TestCapNotice(t *testing.T) {
	if n := capNotice("openrouter", "x-ai/grok-4", 240); !strings.Contains(n, "40 auto-dropped") {
		t.Errorf("notice=%q want overflow detail", n)
	}
	if n := capNotice("openrouter", "x-ai/grok-4", 200); n != "" {
		t.Errorf("at-cap should be silent, got %q", n)
	}
	if n := capNotice("anthropic", "claude-opus-4-8", 500); n != "" {
		t.Errorf("uncapped model should be silent, got %q", n)
	}
}

func TestExposedToolCount(t *testing.T) {
	reg := tools.NewRegistry()
	reg.Register(ftool{"read"})
	reg.Register(ftool{"digitalocean__a"})
	reg.Register(ftool{"digitalocean__b"})
	cfg := mcp.Config{Servers: []mcp.ServerConfig{{Name: "digitalocean", Command: "x", Tools: []string{"a"}}}}
	if got := exposedToolCount(reg, cfg); got != 2 { // read + digitalocean__a
		t.Errorf("exposedToolCount=%d want 2", got)
	}
	if got := exposedToolCount(nil, cfg); got != 0 {
		t.Errorf("nil registry should be 0, got %d", got)
	}
}

// TestHideAllSentinel_RoundTrip: the "expose none" sentinel ([""]) must
// survive a config Save/Load and still hide every tool (regression for the
// disable-all-silently-exposes-all bug).
func TestHideAllSentinel_RoundTrip(t *testing.T) {
	cfg := &config.Config{MCP: mcp.Config{Servers: []mcp.ServerConfig{
		{Name: "do", Command: "x", Tools: []string{""}},
	}}}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sc := got.MCP.Find("do")
	if sc == nil || len(sc.Tools) != 1 || sc.Tools[0] != "" {
		t.Fatalf("sentinel lost in round-trip: %+v", sc)
	}
	if got.MCP.ToolExposed("do__anything") {
		t.Error("after round-trip the sentinel must still hide all tools")
	}
}

// New ServerConfig availability/permission fields must survive the config
// Save/Load round-trip (the cross-session boundary).
func TestServerConfigFields_RoundTrip(t *testing.T) {
	cfg := &config.Config{
		MCP: mcp.Config{Servers: []mcp.ServerConfig{{
			Name:        "digitalocean",
			Command:     "do-mcp",
			Tools:       []string{"droplet_list", "droplet_create"},
			AutoApprove: true,
		}}},
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sc := got.MCP.Find("digitalocean")
	if sc == nil {
		t.Fatal("server lost in round-trip")
	}
	if strings.Join(sc.Tools, ",") != "droplet_list,droplet_create" {
		t.Errorf("Tools=%v lost", sc.Tools)
	}
	if !sc.AutoApprove {
		t.Error("AutoApprove lost in round-trip")
	}
}
