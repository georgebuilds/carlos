//go:build darwin

package daemon

import (
	"strings"
	"testing"
)

// TestMacOSPlistTemplate asserts the rendered plist contains every
// load-bearing element. We don't shell out to plutil because that's a
// macOS-only binary AND it would couple the test to the host's plutil
// version; instead we string-check the structure.
func TestMacOSPlistTemplate(t *testing.T) {
	plist := MacOSPlistTemplateFor("/usr/local/bin/carlos")
	mustContain := []string{
		`<?xml version="1.0"`,
		`<plist version="1.0">`,
		`<key>Label</key>`,
		`<string>com.georgebuilds.carlos</string>`,
		`<key>ProgramArguments</key>`,
		`<string>/usr/local/bin/carlos</string>`,
		`<string>daemon</string>`,
		`<string>run</string>`,
		`<key>RunAtLoad</key>`,
		`<true/>`,
		`<key>KeepAlive</key>`,
	}
	for _, sub := range mustContain {
		if !strings.Contains(plist, sub) {
			t.Errorf("plist missing %q\nfull:\n%s", sub, plist)
		}
	}
}

// TestMacOSPlistPath asserts the path layout matches the install
// docstring.
func TestMacOSPlistPath(t *testing.T) {
	got := macOSPlistPath("/Users/test")
	want := "/Users/test/Library/LaunchAgents/com.georgebuilds.carlos.plist"
	if got != want {
		t.Fatalf("plist path: got %q want %q", got, want)
	}
}
