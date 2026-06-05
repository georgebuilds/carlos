//go:build linux

package daemon

// InstallUnit is the platform-agnostic install entry point. On linux
// it dispatches to InstallSystemdUnit.
func InstallUnit(home string) (string, error) {
	return InstallSystemdUnit(home)
}

// UninstallUnit is the platform-agnostic uninstall entry point.
func UninstallUnit(home string) error {
	return UninstallSystemdUnit(home)
}
