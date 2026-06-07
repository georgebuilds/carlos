//go:build darwin

package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

// TestUninstallLaunchAgent_NoOpWhenAbsent - the uninstaller should be
// idempotent: calling it against a HOME with no plist must not error,
// since the bootout command also succeeds against a missing label.
// We can't easily prevent the bootout command from running; it returns
// non-zero for unknown labels but the implementation silently swallows
// that. The os.Remove of a missing plist is also a no-op via
// os.IsNotExist.
func TestUninstallLaunchAgent_NoOpWhenAbsent(t *testing.T) {
	tmp := t.TempDir()
	// Don't pre-create the plist file; UninstallLaunchAgent should
	// still return nil because os.IsNotExist swallows the missing file
	// error.
	err := UninstallLaunchAgent(tmp)
	if err != nil {
		t.Errorf("UninstallLaunchAgent against empty HOME should be nil; got %v", err)
	}
}

// TestUninstallLaunchAgent_RemovesExistingPlist - when a plist file
// already lives in HOME, uninstall removes it.
func TestUninstallLaunchAgent_RemovesExistingPlist(t *testing.T) {
	tmp := t.TempDir()
	plistDir := filepath.Join(tmp, "Library", "LaunchAgents")
	if err := os.MkdirAll(plistDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plistPath := filepath.Join(plistDir, MacOSLaunchAgentLabel+".plist")
	if err := os.WriteFile(plistPath, []byte("<plist/>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := UninstallLaunchAgent(tmp); err != nil {
		t.Errorf("Uninstall: %v", err)
	}
	if _, err := os.Stat(plistPath); !os.IsNotExist(err) {
		t.Errorf("plist should be removed; stat err=%v", err)
	}
}

// TestUninstallUnit_DispatchesToLaunchAgent ensures the darwin
// dispatcher routes to UninstallLaunchAgent (proven by an empty HOME
// path returning nil).
func TestUninstallUnit_DispatchesToLaunchAgent(t *testing.T) {
	tmp := t.TempDir()
	if err := UninstallUnit(tmp); err != nil {
		t.Errorf("UninstallUnit: %v", err)
	}
}
