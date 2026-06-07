//go:build !darwin && !linux

package daemon

import "errors"

// ErrUnsupportedPlatform is returned by the install/uninstall verbs on
// platforms that don't have a native unit format wired (Windows, BSD,
// etc.). The daemon binary still builds - `carlos daemon run` works
// fine - but the enable/disable verbs surface a clean refusal.
var ErrUnsupportedPlatform = errors.New("daemon: install/uninstall not supported on this platform (use `carlos daemon run` manually)")

// InstallUnit returns ErrUnsupportedPlatform on non-darwin/linux.
func InstallUnit(home string) (string, error) {
	return "", ErrUnsupportedPlatform
}

// UninstallUnit returns ErrUnsupportedPlatform on non-darwin/linux.
func UninstallUnit(home string) error {
	return ErrUnsupportedPlatform
}
