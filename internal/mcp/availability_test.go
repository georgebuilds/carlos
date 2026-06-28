package mcp

import "testing"

func cfg2() Config {
	return Config{Servers: []ServerConfig{
		{Name: "digitalocean", Command: "do-mcp", Tools: []string{"droplet_list", "droplet_create"}, AutoApprove: true},
		{Name: "home-tools", Command: "home-mcp"}, // empty allowlist = all exposed
	}}
}

func TestToolExposed(t *testing.T) {
	c := cfg2()
	cases := []struct {
		name string
		want bool
	}{
		{"read", true},                           // built-in, no separator
		{"git_status", true},                     // built-in with single underscore
		{"digitalocean__droplet_list", true},     // in allowlist
		{"digitalocean__droplet_create", true},   // in allowlist
		{"digitalocean__database_delete", false}, // server has allowlist, omitted
		{"home-tools__anything", true},           // server has empty allowlist => all
		{"unknown__tool", true},                  // server not in config => exposed
	}
	for _, tc := range cases {
		if got := c.ToolExposed(tc.name); got != tc.want {
			t.Errorf("ToolExposed(%q)=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestToolExposed_HideAllSentinel(t *testing.T) {
	// A single empty-string entry means "expose none": nothing matches it.
	c := Config{Servers: []ServerConfig{{Name: "do", Command: "x", Tools: []string{""}}}}
	if c.ToolExposed("do__droplet_list") || c.ToolExposed("do__database_create") {
		t.Error("hide-all sentinel should hide every real tool")
	}
	if !c.ToolExposed("read") {
		t.Error("built-ins still exposed")
	}
}

func TestAutoApproves(t *testing.T) {
	c := cfg2()
	cases := []struct {
		name string
		want bool
	}{
		{"digitalocean__droplet_list", true}, // server AutoApprove=true
		{"home-tools__x", false},             // server AutoApprove=false
		{"read", false},                      // built-in
		{"unknown__t", false},                // unconfigured server
	}
	for _, tc := range cases {
		if got := c.AutoApproves(tc.name); got != tc.want {
			t.Errorf("AutoApproves(%q)=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestFind(t *testing.T) {
	c := cfg2()
	if sc := c.Find("digitalocean"); sc == nil || sc.Name != "digitalocean" {
		t.Errorf("Find(digitalocean)=%v", sc)
	}
	if sc := c.Find("nope"); sc != nil {
		t.Errorf("Find(nope)=%v want nil", sc)
	}
	// Find returns a live pointer into the slice: mutation persists.
	c.Find("home-tools").AutoApprove = true
	if !c.Servers[1].AutoApprove {
		t.Error("mutation through Find pointer did not persist")
	}
}

func TestSplitToolName(t *testing.T) {
	if s, r, ok := splitToolName("srv__tool"); !ok || s != "srv" || r != "tool" {
		t.Errorf("split srv__tool = %q,%q,%v", s, r, ok)
	}
	if _, _, ok := splitToolName("builtin"); ok {
		t.Error("builtin should not split")
	}
	// raw tool name may itself contain a single underscore; only the first
	// "__" separates.
	if s, r, ok := splitToolName("digitalocean__droplet_list"); !ok || s != "digitalocean" || r != "droplet_list" {
		t.Errorf("split = %q,%q,%v", s, r, ok)
	}
}
