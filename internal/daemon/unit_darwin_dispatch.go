//go:build darwin

package daemon

// InstallUnit is the platform-agnostic install entry point. On darwin
// it dispatches to InstallLaunchAgent; on linux to InstallSystemdUnit;
// on other platforms to a stub that returns ErrUnsupportedPlatform.
func InstallUnit(home string) (string, error) {
	return InstallLaunchAgent(home)
}

// UninstallUnit is the platform-agnostic uninstall entry point.
func UninstallUnit(home string) error {
	return UninstallLaunchAgent(home)
}
