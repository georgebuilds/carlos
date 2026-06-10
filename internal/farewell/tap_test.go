package farewell

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseFormulaVersion_HappyPath(t *testing.T) {
	in := `# typed: false
class Carlos < Formula
  desc "Pure-Go TUI agent"
  homepage "https://github.com/georgebuilds/carlos"
  version "0.7.18"
  license "GPL-3.0-or-later"
`
	got, err := parseFormulaVersion(in)
	if err != nil {
		t.Fatalf("parseFormulaVersion: %v", err)
	}
	if got != "0.7.18" {
		t.Errorf("got %q, want %q", got, "0.7.18")
	}
}

func TestParseFormulaVersion_MissingVersionLine(t *testing.T) {
	in := `class Carlos < Formula
  desc "Pure-Go TUI agent"
  homepage "https://github.com/georgebuilds/carlos"
end
`
	if _, err := parseFormulaVersion(in); err == nil {
		t.Error("expected error on missing version line; got nil")
	}
}

func TestParseFormulaVersion_AcceptsWhitespaceVariations(t *testing.T) {
	cases := []string{
		`  version "1.2.3"`,
		"\tversion \"1.2.3\"",
		`version "1.2.3"`,
	}
	for _, in := range cases {
		got, err := parseFormulaVersion(in)
		if err != nil {
			t.Errorf("input %q: parseFormulaVersion: %v", in, err)
			continue
		}
		if got != "1.2.3" {
			t.Errorf("input %q: got %q, want 1.2.3", in, got)
		}
	}
}

func TestNormalizeSemver(t *testing.T) {
	cases := map[string]string{
		"0.7.18":         "0.7.18",
		"v0.7.18":        "0.7.18",
		"v0.7.18+dirty":  "0.7.18",
		"v0.7.18-rc1":    "0.7.18",
		"  v0.7.18  ":    "0.7.18",
		"dev":            "",   // non-numeric
		"abc1234":        "",   // commit hash
		"v0.7":           "0.7",
		"":               "",
		"v":              "",
	}
	for in, want := range cases {
		if got := normalizeSemver(in); got != want {
			t.Errorf("normalizeSemver(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSemverGreater(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"0.7.18", "0.7.17", true},  // patch newer
		{"0.7.17", "0.7.18", false}, // patch older
		{"0.8.0", "0.7.99", true},   // minor newer
		{"1.0.0", "0.9.99", true},   // major newer
		{"0.7.18", "0.7.18", false}, // equal
		{"v0.7.18", "0.7.17", true}, // mixed v-prefix
		{"v0.7.18+dirty", "0.7.18", false}, // build metadata stripped
		{"0.8", "0.7.18", true},     // shorter side wins on segment compare
		{"dev", "0.7.18", false},    // non-semver → false
		{"0.7.18", "dev", false},    // non-semver → false
	}
	for _, tc := range cases {
		got := semverGreater(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("semverGreater(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// fakeTap returns a httptest.Server whose only endpoint serves the
// supplied formula body when GET'd. Used by the network tests below
// so they exercise the HTTP path without touching the real tap.
func fakeTap(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchTapFormulaVersion_HappyPath(t *testing.T) {
	srv := fakeTap(t, `class Carlos < Formula
  version "1.2.3"
end`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got, err := FetchTapFormulaVersion(ctx, srv.URL)
	if err != nil {
		t.Fatalf("FetchTapFormulaVersion: %v", err)
	}
	if got != "1.2.3" {
		t.Errorf("got %q, want 1.2.3", got)
	}
}

func TestFetchTapFormulaVersion_Non200ReturnsErr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := FetchTapFormulaVersion(ctx, srv.URL); err == nil {
		t.Error("expected error on HTTP 404; got nil")
	}
}

func TestFetchTapFormulaVersion_UnparseableBodyReturnsErr(t *testing.T) {
	srv := fakeTap(t, "this is not a homebrew formula")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := FetchTapFormulaVersion(ctx, srv.URL); err == nil {
		t.Error("expected parse error on bad body; got nil")
	}
}

func TestFetchTapFormulaVersion_RespectsContextDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block longer than the test's deadline so the request must
		// be cancelled by ctx.
		time.Sleep(500 * time.Millisecond)
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := FetchTapFormulaVersion(ctx, srv.URL)
	if err == nil {
		t.Error("expected context-deadline error; got nil")
	}
}

// TestCheckTapUpdate_RemoteNewerReturnsTrue is the headline behavior:
// when the tap's formula version is greater than the running binary
// (the goreleaser writes 0.7.18 after we ship; binary is still
// v0.7.17), the function returns the remote version and true.
func TestCheckTapUpdate_RemoteNewerReturnsTrue(t *testing.T) {
	srv := fakeTap(t, `version "0.7.18"`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	newer, ok := CheckTapUpdate(ctx, "v0.7.17", srv.URL)
	if !ok {
		t.Fatal("expected ok=true on remote-newer; got false")
	}
	if newer != "0.7.18" {
		t.Errorf("remote version = %q, want 0.7.18", newer)
	}
}

func TestCheckTapUpdate_SameVersionReturnsFalse(t *testing.T) {
	srv := fakeTap(t, `version "0.7.18"`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, ok := CheckTapUpdate(ctx, "v0.7.18", srv.URL); ok {
		t.Error("same version should return ok=false")
	}
}

func TestCheckTapUpdate_RemoteOlderReturnsFalse(t *testing.T) {
	// In the wild this shouldn't happen, but if a rollback left the
	// tap behind the local binary we definitely don't want to nag
	// the user to "upgrade" to an older release.
	srv := fakeTap(t, `version "0.7.10"`)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, ok := CheckTapUpdate(ctx, "v0.7.18", srv.URL); ok {
		t.Error("remote-older should return ok=false")
	}
}

func TestCheckTapUpdate_DevBuildShortCircuits(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		_, _ = w.Write([]byte(`version "999.999.999"`))
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, ok := CheckTapUpdate(ctx, "dev", srv.URL); ok {
		t.Error("dev build should never produce an upgrade notice")
	}
	if called {
		t.Error("dev build should short-circuit before hitting the network")
	}
}

func TestCheckTapUpdate_NonSemverShortCircuits(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	t.Cleanup(srv.Close)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, ok := CheckTapUpdate(ctx, "dev (abc1234)", srv.URL); ok {
		t.Error("non-semver current version should not produce an upgrade notice")
	}
	if called {
		t.Error("non-semver should short-circuit before hitting the network")
	}
}

func TestCheckTapUpdate_NetworkErrorDegradeSilent(t *testing.T) {
	// Point at an immediately-closed server so Do() returns an error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, ok := CheckTapUpdate(ctx, "v0.7.17", srv.URL); ok {
		t.Error("network error should not produce an upgrade notice")
	}
}

// TestFetchTapFormulaVersion_DefaultURLIsExportedConstant pins the
// public hook tests use to point at httptest mocks. If the default
// URL changes (e.g. tap moves to a different repo), tests should
// keep working by overriding via the parameter.
func TestFetchTapFormulaVersion_DefaultURLIsExportedConstant(t *testing.T) {
	if !strings.HasPrefix(DefaultTapFormulaURL, "https://") {
		t.Errorf("DefaultTapFormulaURL should be an https URL; got %q", DefaultTapFormulaURL)
	}
	if !strings.Contains(DefaultTapFormulaURL, "carlos") {
		t.Errorf("DefaultTapFormulaURL should reference the carlos formula; got %q", DefaultTapFormulaURL)
	}
}

// Smoke check that the imports compile against fmt — keeps the file
// from regressing if a future edit drops the import.
var _ = fmt.Sprintf
