package farewell

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// DefaultTapFormulaURL is the raw-content URL for the carlos formula
// in georgebuilds/homebrew-tap. Pinned here because both production
// and tests reference it; tests override via the formulaURL parameter
// when they want to point at a local mock server.
const DefaultTapFormulaURL = "https://raw.githubusercontent.com/georgebuilds/homebrew-tap/main/Formula/carlos.rb"

// tapFetchTimeout caps the HTTP request to the tap so the post-exit
// upgrade probe never drags shutdown for more than a couple of
// seconds even when the user is offline / GitHub raw is slow.
// Applied as a context deadline by the caller; we don't double-set
// it here.

// tapVersionLineRE picks the version string out of the
// goreleaser-generated formula. Matches the (well-formed) pattern
//
//	version "0.7.18"
//
// at the start of a line, tolerating any amount of leading
// whitespace. Multiline-mode anchor + a non-greedy capture inside
// the quotes keeps it robust against future formula additions.
var tapVersionLineRE = regexp.MustCompile(`(?m)^\s*version\s+"([^"]+)"`)

// FetchTapFormulaVersion pulls the carlos formula from the homebrew
// tap and returns the bare version string ("0.7.18", no "v" prefix
// — that matches what goreleaser writes into the formula). The
// context's deadline bounds the HTTP request; on any error
// (network, non-200, parse failure) we return ("", err) so callers
// can degrade silently.
//
// formulaURL == "" picks DefaultTapFormulaURL. Tests override with
// a httptest.Server URL so they exercise the parse path without
// touching the network.
func FetchTapFormulaVersion(ctx context.Context, formulaURL string) (string, error) {
	if formulaURL == "" {
		formulaURL = DefaultTapFormulaURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, formulaURL, nil)
	if err != nil {
		return "", fmt.Errorf("tap formula request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("tap formula fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tap formula: HTTP %d", resp.StatusCode)
	}
	// 64 KB is a generous upper bound for a goreleaser-formula —
	// real ones are ~2 KB. Cap so a hijacked endpoint can't stream
	// gigabytes into our process while it's trying to shut down.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return "", fmt.Errorf("tap formula read: %w", err)
	}
	return parseFormulaVersion(string(body))
}

// parseFormulaVersion extracts the version string from a homebrew
// formula body. Returns an error when no version line is found so
// the caller (and tests) can distinguish "parsed empty" from
// "parse succeeded with empty value".
func parseFormulaVersion(body string) (string, error) {
	m := tapVersionLineRE.FindStringSubmatch(body)
	if len(m) < 2 || m[1] == "" {
		return "", errors.New("tap formula: no version line found")
	}
	return m[1], nil
}

// CheckTapUpdate fetches the live tap formula version and reports
// whether it's newer than currentVersion. Returns the remote
// version string and true when an upgrade is available; ("", false)
// when versions match, when remote is older (shouldn't happen, but
// we won't nag), when the binary is a dev/non-semver build, or on
// any fetch/parse error.
//
// This is the authoritative "is there a new release?" check —
// independent of brew's local cache, so it works even when the
// user hasn't run `brew update` recently or has
// HOMEBREW_NO_AUTO_UPDATE set. Cost: one HTTPS round-trip to
// raw.githubusercontent.com (the goreleaser tap target), bounded
// by the supplied context.
func CheckTapUpdate(ctx context.Context, currentVersion, formulaURL string) (string, bool) {
	if currentVersion == "" || currentVersion == "dev" {
		return "", false
	}
	if normalizeSemver(currentVersion) == "" {
		// Non-semver build (commit hash, "dev (abc1234)", etc.). We
		// can't compare, so don't pretend.
		return "", false
	}
	remote, err := FetchTapFormulaVersion(ctx, formulaURL)
	if err != nil {
		return "", false
	}
	if !semverGreater(remote, currentVersion) {
		return "", false
	}
	return remote, true
}

// normalizeSemver strips the leading "v" and any trailing build
// metadata ("+dirty", "-rc1") so semverGreater compares the three
// numeric segments cleanly. Returns "" when the input doesn't look
// like a numeric semver (so callers can early-exit).
func normalizeSemver(v string) string {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "+-"); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return ""
	}
	// Sanity check: every segment must be all-digits.
	for _, seg := range strings.Split(v, ".") {
		if seg == "" {
			return ""
		}
		for _, r := range seg {
			if r < '0' || r > '9' {
				return ""
			}
		}
	}
	return v
}

// semverGreater reports whether a > b under simple numeric-segment
// semver comparison. Both inputs are normalized first; either being
// non-semver returns false (we won't nag on a comparison we can't
// trust). Segment counts can differ — "0.8" is treated as ahead of
// "0.7.18" by extending the shorter side with zeros.
func semverGreater(a, b string) bool {
	a = normalizeSemver(a)
	b = normalizeSemver(b)
	if a == "" || b == "" {
		return false
	}
	aSegs := strings.Split(a, ".")
	bSegs := strings.Split(b, ".")
	for i := 0; i < max2(len(aSegs), len(bSegs)); i++ {
		ai := segInt(aSegs, i)
		bi := segInt(bSegs, i)
		if ai != bi {
			return ai > bi
		}
	}
	return false
}

func segInt(segs []string, i int) int {
	if i >= len(segs) {
		return 0
	}
	n, _ := strconv.Atoi(segs[i])
	return n
}

func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}
