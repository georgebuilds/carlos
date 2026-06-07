//go:build linux

package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LinuxServiceName is the systemd unit name the user invokes:
// `systemctl --user status carlos.service`.
const LinuxServiceName = "carlos.service"

// linuxUnitPath returns ~/.config/systemd/user/carlos.service.
func linuxUnitPath(home string) string {
	return filepath.Join(home, ".config", "systemd", "user", LinuxServiceName)
}

// linuxUnitTemplate renders the systemd --user unit that runs
// `carlos daemon run`. Restart=always keeps it alive across crashes;
// the unit is wired into default.target so it starts on user login
// (matches launchd's RunAtLoad).
func linuxUnitTemplate(binaryPath string) string {
	return fmt.Sprintf(`[Unit]
Description=carlos background daemon
After=default.target

[Service]
Type=simple
ExecStart=%s daemon run
Restart=always
RestartSec=5
# StandardOutput/StandardError default to journal - view with
#   journalctl --user -u carlos -f
NoNewPrivileges=true

[Install]
WantedBy=default.target
`, binaryPath)
}

// InstallSystemdUnit writes the unit file and runs systemctl --user
// enable --now. Idempotent: daemon-reload before enable picks up unit
// changes if the user upgrades the binary path.
func InstallSystemdUnit(home string) (string, error) {
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

	unitPath := linuxUnitPath(home)
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return "", fmt.Errorf("install: mkdir systemd user dir: %w", err)
	}
	content := linuxUnitTemplate(binPath)
	if err := os.WriteFile(unitPath, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("install: write unit: %w", err)
	}

	// Refresh + enable + start.
	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return unitPath, fmt.Errorf("install: systemctl daemon-reload: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "--user", "enable", "--now", LinuxServiceName).CombinedOutput(); err != nil {
		return unitPath, fmt.Errorf("install: systemctl enable --now: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return unitPath, nil
}

// UninstallSystemdUnit stops + disables the unit and removes the unit
// file. No-ops cleanly if the unit isn't present.
func UninstallSystemdUnit(home string) error {
	if home == "" {
		var err error
		home, err = os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("uninstall: home dir: %w", err)
		}
	}
	_ = exec.Command("systemctl", "--user", "disable", "--now", LinuxServiceName).Run()
	unitPath := linuxUnitPath(home)
	if err := os.Remove(unitPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("uninstall: remove unit: %w", err)
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	return nil
}

// LinuxUnitTemplateFor renders the unit text for inspection without
// touching disk. Exported for tests + the notes file.
func LinuxUnitTemplateFor(binaryPath string) string {
	return linuxUnitTemplate(binaryPath)
}
