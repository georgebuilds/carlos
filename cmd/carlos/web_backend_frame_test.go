// Regression tests for the web backend's frame resolution (the rail card's
// "frame" cell). Two layers: the Frame() contract per attachment, and the
// frameName resolution newCarlosBackend performs at construction (the same
// env -> cwd-hint -> persisted -> default chain runtime_tui uses).
package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/frame"
	"github.com/georgebuilds/carlos/internal/web"
)

// Frame resolves at attach: the attached thread reads the backend's resolved
// frame, anything else reads "" (spec §11.4).
func TestCarlosBackend_FrameResolvesAtAttach(t *testing.T) {
	b := &carlosBackend{
		frameName: "work",
		attached:  map[string]*webThread{"t1": {}},
	}
	if got := b.Frame("t1"); got != "work" {
		t.Errorf("attached thread frame = %q, want \"work\"", got)
	}
	if got := b.Frame("t2"); got != "" {
		t.Errorf("detached thread frame = %q, want \"\"", got)
	}
}

// newCarlosBackend must resolve frameName from the config's frame chain so
// Frame() has a non-empty answer for attached threads. An empty frameName
// here is the server-side way the web GUI's frame cell goes blank.
func TestNewCarlosBackend_ResolvesFrameNameFromConfig(t *testing.T) {
	t.Setenv("CARLOS_FRAME", "") // neutralize the developer's environment

	log, err := agent.OpenSQLiteEventLog(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	cfg := &config.Config{
		UserName:        "george",
		DefaultProvider: "anthropic",
		Providers: map[string]config.ProviderConfig{
			"anthropic": {APIKey: "test-key"},
		},
		Frames: frame.Config{
			Active: "work",
			List: []frame.Frame{
				{Name: "work"},
				{Name: "personal"},
			},
		},
	}
	srv := web.NewServer(web.Options{Log: log, Token: "test-token"})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	b, err := newCarlosBackend(ctx, cfg, log, srv)
	if err != nil {
		t.Fatalf("newCarlosBackend: %v", err)
	}
	t.Cleanup(b.Shutdown)

	if b.frameName != "work" {
		t.Errorf("frameName = %q, want \"work\" (persisted active)", b.frameName)
	}
	// Nothing attached yet: every thread still reads "".
	if got := b.Frame("t1"); got != "" {
		t.Errorf("pre-attach frame = %q, want \"\"", got)
	}
}

// A config whose persisted active names a frame that no longer exists must
// not blow up construction; frameName stays "" (the read-only style answer).
func TestNewCarlosBackend_StaleActiveFrameLeavesNameEmpty(t *testing.T) {
	t.Setenv("CARLOS_FRAME", "")

	log, err := agent.OpenSQLiteEventLog(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })

	cfg := &config.Config{
		UserName:        "george",
		DefaultProvider: "anthropic",
		Providers: map[string]config.ProviderConfig{
			"anthropic": {APIKey: "test-key"},
		},
		Frames: frame.Config{
			Active: "deleted-frame",
			List:   []frame.Frame{{Name: "personal"}},
		},
	}
	srv := web.NewServer(web.Options{Log: log, Token: "test-token"})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	b, err := newCarlosBackend(ctx, cfg, log, srv)
	if err != nil {
		t.Fatalf("newCarlosBackend: %v", err)
	}
	t.Cleanup(b.Shutdown)

	if b.frameName != "" {
		t.Errorf("frameName = %q, want \"\" for a stale active frame", b.frameName)
	}
}
