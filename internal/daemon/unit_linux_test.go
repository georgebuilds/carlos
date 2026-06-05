//go:build linux

package daemon

import (
	"strings"
	"testing"
)

// TestLinuxUnitTemplate asserts the systemd unit contains all required
// directives.
func TestLinuxUnitTemplate(t *testing.T) {
	unit := LinuxUnitTemplateFor("/usr/local/bin/carlos")
	mustContain := []string{
		"[Unit]",
		"Description=carlos background daemon",
		"[Service]",
		"Type=simple",
		"ExecStart=/usr/local/bin/carlos daemon run",
		"Restart=always",
		"[Install]",
		"WantedBy=default.target",
	}
	for _, sub := range mustContain {
		if !strings.Contains(unit, sub) {
			t.Errorf("unit missing %q\nfull:\n%s", sub, unit)
		}
	}
}

// TestLinuxUnitPath asserts the path layout matches the install
// docstring.
func TestLinuxUnitPath(t *testing.T) {
	got := linuxUnitPath("/home/test")
	want := "/home/test/.config/systemd/user/carlos.service"
	if got != want {
		t.Fatalf("unit path: got %q want %q", got, want)
	}
}
