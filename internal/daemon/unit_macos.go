//go:build darwin

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// MacOSLaunchAgentLabel is the launchd label used in the plist + the
// `launchctl bootout` argument. Reverse-DNS form by convention.
const MacOSLaunchAgentLabel = "com.georgebuilds.carlos"

// macOSPlistPath returns ~/Library/LaunchAgents/<label>.plist.
func macOSPlistPath(home string) string {
	return filepath.Join(home, "Library", "LaunchAgents", MacOSLaunchAgentLabel+".plist")
}

// macOSPlistTemplate renders a launchd LaunchAgent plist that runs the
// carlos binary's `daemon run` subcommand at user login.
//
// KeepAlive is true so launchd restarts the daemon if it crashes;
// RunAtLoad is true so it starts on user login.
//
// We deliberately don't set Throttle / StandardOutPath here - those are
// future-polish slices (8c/8d). Stdout/stderr go to the launchd
// per-user log; users can tail them with `log stream --predicate
// 'subsystem == "carlos"'` once we add os.Log instrumentation.
func macOSPlistTemplate(binaryPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>daemon</string>
        <string>run</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>ProcessType</key>
    <string>Background</string>
</dict>
</plist>
`, MacOSLaunchAgentLabel, binaryPath)
}

// InstallLaunchAgent writes the launchd plist and runs `launchctl
// bootstrap` to enable it. Idempotent: re-running boots out + re-loads
// so a config change in the plist takes effect.
//
// Returns the absolute path of the plist so callers can persist it in
// config.Daemon.UnitPath.
func InstallLaunchAgent(home string) (string, error) {
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("install: home dir: %w", err)
		}
	}
	binPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("install: locate carlos binary: %w", err)
	}
	binPath, _ = filepath.EvalSymlinks(binPath)

	plistPath := macOSPlistPath(home)
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return "", fmt.Errorf("install: mkdir LaunchAgents: %w", err)
	}
	content := macOSPlistTemplate(binPath)
	if err := os.WriteFile(plistPath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("install: write plist: %w", err)
	}

	// Best-effort: unload any previous instance (ignore errors - first
	// install will report "not loaded").
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", uid+"/"+MacOSLaunchAgentLabel).Run()

	// Load + enable.
	if out, err := exec.Command("launchctl", "bootstrap", uid, plistPath).CombinedOutput(); err != nil {
		return plistPath, fmt.Errorf("install: launchctl bootstrap: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("launchctl", "enable", uid+"/"+MacOSLaunchAgentLabel).CombinedOutput(); err != nil {
		return plistPath, fmt.Errorf("install: launchctl enable: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return plistPath, nil
}

// UninstallLaunchAgent reverses InstallLaunchAgent: boots out the
// LaunchAgent and removes the plist file. No-ops cleanly if the plist
// isn't present.
func UninstallLaunchAgent(home string) error {
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("uninstall: home dir: %w", err)
		}
	}
	plistPath := macOSPlistPath(home)
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", uid+"/"+MacOSLaunchAgentLabel).Run()
	if err := os.Remove(plistPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("uninstall: remove plist: %w", err)
	}
	return nil
}

// MacOSPlistTemplateFor renders the plist text for inspection without
// touching disk. Exported for tests + the notes file.
func MacOSPlistTemplateFor(binaryPath string) string {
	return macOSPlistTemplate(binaryPath)
}
