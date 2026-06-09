package usershell

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/frame"
)

// TestDefaultOutputDir_TargetsPerFramePersonalUsershell pins the
// v0.7.3 fix: defaultOutputDir() must point at the per-frame
// personal usershell directory, NOT the legacy ~/.carlos/usershell.
// The old default fed a loop where every session wrote to legacy
// and the next session's queueFrameMigration shoveled the files
// into the per-frame tree ("migrated N shell jobs to per-frame
// layout" on every boot).
//
// We assert the path SHAPE (a substring contract) rather than an
// exact string so frame.DefaultPersonalName can change without
// breaking this test — the only thing that must remain true is
// that the default is a per-frame, not a legacy, path.
func TestDefaultOutputDir_TargetsPerFramePersonalUsershell(t *testing.T) {
	got := defaultOutputDir()

	// Must include the per-frame breadcrumb.
	want := filepath.Join("frames", frame.DefaultPersonalName, "usershell")
	if !strings.HasSuffix(got, want) {
		t.Errorf("defaultOutputDir() = %q, expected suffix %q (per-frame layout)", got, want)
	}

	// Must NOT be the legacy path the v0.7.x cycle wrote to.
	if strings.HasSuffix(got, filepath.Join(".carlos", "usershell")) {
		t.Errorf("defaultOutputDir() = %q, still points at the legacy ~/.carlos/usershell location", got)
	}
}

// TestNew_FallbackUsesPerFrameDefault is a smoke test on the New()
// path: callers who omit OutputDir get a Manager whose outputDir
// is the per-frame personal location. Without this gate a future
// New() refactor could re-introduce the legacy default by accident.
func TestNew_FallbackUsesPerFrameDefault(t *testing.T) {
	m := New(Options{}) // no OutputDir
	if m.outputDir == "" {
		t.Fatal("manager constructed with empty outputDir; default fallback didn't fire")
	}
	want := filepath.Join("frames", frame.DefaultPersonalName, "usershell")
	if !strings.HasSuffix(m.outputDir, want) {
		t.Errorf("manager outputDir = %q, expected suffix %q", m.outputDir, want)
	}
}

// TestNew_ExplicitOutputDirWinsOverDefault confirms the precedence:
// callers passing an explicit OutputDir get exactly that path, no
// silent rewrite into the per-frame layout. Production wire-up in
// cmd/carlos/runtime_tui.go relies on this so the live active-
// frame's JobsDir is honored.
func TestNew_ExplicitOutputDirWinsOverDefault(t *testing.T) {
	custom := "/tmp/carlos-test-explicit-outputdir"
	m := New(Options{OutputDir: custom})
	if m.outputDir != custom {
		t.Errorf("explicit OutputDir overridden: got %q, want %q", m.outputDir, custom)
	}
}
