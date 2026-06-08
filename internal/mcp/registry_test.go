package mcp

import (
	"context"
	"errors"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestConnectAll_PartialFailure pins the boot-resilience contract:
// one missing/broken server doesn't abort the rest. The user gets a
// warning on stderr and carlos continues with whatever servers came up.
//
// The "good" entry here is also broken (we pass a path that won't
// exist either) - we don't need a real MCP server, we just need to
// prove that ConnectAll's per-server fail-and-continue loop produces
// independent warnings for each failure rather than aborting on the
// first one.
func TestConnectAll_PartialFailure(t *testing.T) {
	cfg := Config{
		Servers: []ServerConfig{
			{Name: "first", Command: "/definitely/not/a/real/binary-1"},
			{Name: "second", Command: "/definitely/not/a/real/binary-2"},
		},
	}
	servers, warnings := ConnectAll(context.Background(), cfg, "")
	if len(servers) != 0 {
		t.Errorf("expected zero servers (both bogus), got %d", len(servers))
	}
	if len(warnings) != 2 {
		t.Fatalf("expected 2 per-server warnings, got %d: %v", len(warnings), warnings)
	}
	if !strings.Contains(warnings[0], "first") || !strings.Contains(warnings[1], "second") {
		t.Errorf("warnings should name each server; got %v", warnings)
	}
}

// TestConnectAll_NoServers is the hot-path quick-return - users who
// haven't configured MCP at all shouldn't pay a syscall for it.
func TestConnectAll_NoServers(t *testing.T) {
	servers, warnings := ConnectAll(context.Background(), Config{}, "personal")
	if servers != nil || warnings != nil {
		t.Errorf("want nil/nil for empty config; got servers=%v warnings=%v", servers, warnings)
	}
}

// TestConnectAll_FilterByFrame checks the frame gate runs before
// Connect: a server scoped to "work" isn't spawned during a "personal"
// session.
func TestConnectAll_FilterByFrame(t *testing.T) {
	cfg := Config{
		Servers: []ServerConfig{
			{Name: "work-only", Command: "/missing", Frames: []string{"work"}},
		},
	}
	// "personal" frame: server is filtered out before Connect runs,
	// so no warnings should fire (we never tried to spawn it).
	servers, warnings := ConnectAll(context.Background(), cfg, "personal")
	if len(servers) != 0 || len(warnings) != 0 {
		t.Errorf("expected no spawn attempt for frame-gated server; got servers=%v warnings=%v",
			servers, warnings)
	}
}

// TestDiscoverTools_PartialFailure verifies that a discovery failure
// on one server doesn't abort the rest: the good server's tools still
// land in the output, the bad server contributes a warning.
func TestDiscoverTools_PartialFailure(t *testing.T) {
	good := &Server{Name: "good", Session: &stubSession{
		listFn: func(ctx context.Context, p *sdk.ListToolsParams) (*sdk.ListToolsResult, error) {
			return &sdk.ListToolsResult{Tools: []*sdk.Tool{
				{Name: "alpha", Description: "α"},
				{Name: "beta", Description: "β"},
			}}, nil
		},
	}}
	bad := &Server{Name: "bad", Session: &stubSession{
		listFn: func(ctx context.Context, p *sdk.ListToolsParams) (*sdk.ListToolsResult, error) {
			return nil, errors.New("server died")
		},
	}}
	tools, warnings := DiscoverTools(context.Background(), []*Server{good, bad})
	if len(tools) != 2 {
		t.Errorf("want 2 tools from the good server; got %d", len(tools))
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "bad") {
		t.Errorf("want a single 'bad' warning; got %v", warnings)
	}
	if tools[0].Name() != "good__alpha" || tools[1].Name() != "good__beta" {
		t.Errorf("tool names off: %q, %q", tools[0].Name(), tools[1].Name())
	}
}

// TestDiscoverTools_NoServers is the empty-input quick path.
func TestDiscoverTools_NoServers(t *testing.T) {
	tools, warnings := DiscoverTools(context.Background(), nil)
	if tools != nil || warnings != nil {
		t.Errorf("want nil/nil for no servers; got %v, %v", tools, warnings)
	}
}

// TestCloseAll_BestEffort verifies CloseAll doesn't bail on the first
// error: every server's Close runs, even if an earlier one failed.
func TestCloseAll_BestEffort(t *testing.T) {
	a := &stubSession{}
	b := &stubSession{}
	CloseAll([]*Server{
		{Name: "a", Session: a},
		{Name: "b", Session: b},
	})
	if !a.closed || !b.closed {
		t.Errorf("not all Close calls ran: a=%v b=%v", a.closed, b.closed)
	}
}
