package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/mcp"
	"github.com/georgebuilds/carlos/internal/tools"
)

// TestWireMCP_EmptyConfigIsCleanNoOp pins the zero-server path: no
// warnings, no tools, the close func is callable. Boot should not
// emit anything when MCP isn't configured.
func TestWireMCP_EmptyConfigIsCleanNoOp(t *testing.T) {
	reg := tools.NewRegistry()
	var buf bytes.Buffer
	servers, closer, count := wireMCP(context.Background(), &buf, mcp.Config{}, "personal", reg)
	if count != 0 {
		t.Errorf("expected 0 discovered tools, got %d", count)
	}
	if len(servers) != 0 {
		t.Errorf("expected no connected servers, got %d", len(servers))
	}
	if buf.Len() != 0 {
		t.Errorf("expected no warnings on empty config; got %q", buf.String())
	}
	closer() // must not panic
}

// TestWireMCP_BadCommandSurfacesWarnings drives the failure-on-
// connect path: a nonexistent binary is best-effort skipped + a
// "carlos: mcp:" warning lands in the writer. Boot continues.
func TestWireMCP_BadCommandSurfacesWarnings(t *testing.T) {
	reg := tools.NewRegistry()
	var buf bytes.Buffer
	cfg := mcp.Config{
		Servers: []mcp.ServerConfig{
			{Name: "ghost", Command: "/path/that/definitely/does/not/exist"},
		},
	}
	servers, closer, count := wireMCP(context.Background(), &buf, cfg, "personal", reg)
	defer closer()
	if count != 0 {
		t.Errorf("expected 0 tools from a failed connect, got %d", count)
	}
	if len(servers) != 0 {
		t.Errorf("failed-connect server should not be returned; got %d", len(servers))
	}
	if !strings.Contains(buf.String(), "carlos: mcp:") {
		t.Errorf("expected a 'carlos: mcp:' warning line, got %q", buf.String())
	}
}
