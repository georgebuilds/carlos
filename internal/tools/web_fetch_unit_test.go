package tools

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
)

// TestWebFetchClient_InjectedClientWins — client() returns an injected
// client verbatim.
func TestWebFetchClient_InjectedClientWins(t *testing.T) {
	injected := &http.Client{}
	tool := &WebFetchTool{Client: injected}
	if got := tool.client(); got != injected {
		t.Error("client() should return the injected client")
	}
}

// TestWebFetchClient_DialControlRejectsPrivate — the default client's
// Dialer.Control refuses a dial to a private IP when AllowPrivate is off,
// closing the DNS-rebinding TOCTOU.
func TestWebFetchClient_DialControlRejectsPrivate(t *testing.T) {
	tool := &WebFetchTool{} // AllowPrivate false
	cli := tool.client()
	tr, ok := cli.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("expected *http.Transport, got %T", cli.Transport)
	}
	_, err := tr.DialContext(context.Background(), "tcp", "127.0.0.1:9")
	if err == nil || !strings.Contains(err.Error(), "private address") {
		t.Errorf("Control should reject a private dial; got %v", err)
	}
}

// TestWebFetchClient_DialControlAllowsWhenOptedIn — AllowPrivate=true makes
// Control a no-op (dial fails only because nothing listens, not by guard).
func TestWebFetchClient_DialControlAllowsWhenOptedIn(t *testing.T) {
	tool := &WebFetchTool{AllowPrivate: true}
	cli := tool.client()
	tr := cli.Transport.(*http.Transport)
	_, err := tr.DialContext(context.Background(), "tcp", "127.0.0.1:9")
	if err != nil && strings.Contains(err.Error(), "private address") {
		t.Errorf("AllowPrivate should bypass the Control guard; got %v", err)
	}
}

// TestWebFetchClient_RedirectToPrivateBlocked — CheckRedirect refuses a
// 30x bounce to a private host when AllowPrivate is off.
func TestWebFetchClient_RedirectToPrivateBlocked(t *testing.T) {
	tool := &WebFetchTool{}
	cli := tool.client()
	req, _ := http.NewRequest("GET", "http://169.254.169.254/meta", nil)
	origin, _ := http.NewRequest("GET", "http://public.example/start", nil)
	if err := cli.CheckRedirect(req, []*http.Request{origin}); err == nil ||
		!strings.Contains(err.Error(), "private host") {
		t.Errorf("CheckRedirect should block a private redirect; got %v", err)
	}
}

// TestWebFetchClient_TooManyRedirects — CheckRedirect refuses once the
// redirect chain reaches MaxRedirects.
func TestWebFetchClient_TooManyRedirects(t *testing.T) {
	tool := &WebFetchTool{AllowPrivate: true, MaxRedirects: 2}
	cli := tool.client()
	req, _ := http.NewRequest("GET", "http://public.example/again", nil)
	// A via chain already at the cap trips the guard.
	via := []*http.Request{{}, {}}
	if err := cli.CheckRedirect(req, via); err == nil ||
		!strings.Contains(err.Error(), "too many redirects") {
		t.Errorf("CheckRedirect should refuse past the cap; got %v", err)
	}
}

// TestIsPrivateIP_Table covers each classification branch directly.
func TestIsPrivateIP_Table(t *testing.T) {
	cases := []struct {
		ip       string
		wantPriv bool
		reason   string
	}{
		{"127.0.0.1", true, "loopback"},
		{"::1", true, "loopback"},
		{"0.0.0.0", true, "unspecified"},
		{"169.254.1.1", true, "link-local"},
		{"fe80::1", true, "link-local"},
		{"239.0.0.1", true, "multicast"},
		{"10.0.0.1", true, "private (RFC1918 / ULA)"},
		{"192.168.1.1", true, "private (RFC1918 / ULA)"},
		{"172.16.0.1", true, "private (RFC1918 / ULA)"},
		{"fc00::1", true, "private (RFC1918 / ULA)"},
		{"8.8.8.8", false, ""},
		{"1.1.1.1", false, ""},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("bad test IP %q", c.ip)
		}
		gotPriv, gotReason := isPrivateIP(ip)
		if gotPriv != c.wantPriv {
			t.Errorf("isPrivateIP(%s) priv = %v, want %v", c.ip, gotPriv, c.wantPriv)
		}
		if c.wantPriv && gotReason != c.reason {
			t.Errorf("isPrivateIP(%s) reason = %q, want %q", c.ip, gotReason, c.reason)
		}
	}
}

// TestIsPrivateIP_Nil — a nil IP is not private.
func TestIsPrivateIP_Nil(t *testing.T) {
	if priv, _ := isPrivateIP(nil); priv {
		t.Error("nil IP must not be classified private")
	}
}

// TestIsPrivateHost_Literals — localhost variants and literal private IPs
// are refused; a public literal IP passes. host:port is split first.
func TestIsPrivateHost_Literals(t *testing.T) {
	cases := []struct {
		host     string
		wantPriv bool
	}{
		{"localhost", true},
		{"localhost.", true},
		{"foo.localhost", true},
		{"LOCALHOST", true},
		{"127.0.0.1", true},
		{"127.0.0.1:8080", true},
		{"10.1.2.3", true},
		{"[::1]:443", true},
		{"8.8.8.8", false},
		{"8.8.8.8:80", false},
	}
	for _, c := range cases {
		gotPriv, reason := isPrivateHost(c.host)
		if gotPriv != c.wantPriv {
			t.Errorf("isPrivateHost(%q) = (%v,%q), want priv %v", c.host, gotPriv, reason, c.wantPriv)
		}
	}
}

// TestParseRobots — star group disallows are collected; non-star groups,
// comments, blank lines, and malformed lines are ignored.
func TestParseRobots_StarGroup(t *testing.T) {
	body := strings.Join([]string{
		"# a comment",
		"",
		"User-agent: BadBot",
		"Disallow: /everything", // not the star group -> ignored
		"User-agent: *",         // enter star group
		"Disallow: /private",    // collected
		"Disallow:",             // empty value -> ignored
		"Disallow: /tmp # trailing comment",
		"NotADirective without colon",
		"Allow: /public", // unsupported key -> ignored
	}, "\n")
	got := parseRobots(body)
	want := map[string]bool{"/private": true, "/tmp": true}
	if len(got) != len(want) {
		t.Fatalf("parseRobots got %v, want keys %v", got, want)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected disallow %q in %v", g, got)
		}
	}
}

// TestParseRobots_NoStarGroup — a file that only addresses a named UA
// yields no disallows for us (we only honor the star group).
func TestParseRobots_NoStarGroup(t *testing.T) {
	got := parseRobots("User-agent: Googlebot\nDisallow: /\n")
	if len(got) != 0 {
		t.Errorf("named-UA-only robots should yield no star disallows; got %v", got)
	}
}

// TestRobotsAllows — prefix matching and the empty-path default.
func TestRobotsAllows_Prefix(t *testing.T) {
	dis := []string{"/private", "", "/admin"}
	cases := []struct {
		path string
		want bool
	}{
		{"/private/data", false},
		{"/admin", false},
		{"/public", true},
		{"", true},  // empty path becomes "/"
		{"/", true}, // not covered by any prefix
	}
	for _, c := range cases {
		if got := robotsAllows(dis, c.path); got != c.want {
			t.Errorf("robotsAllows(%q) = %v, want %v", c.path, got, c.want)
		}
	}
	if !robotsAllows(nil, "/anything") {
		t.Error("empty disallows must always allow")
	}
}

// TestEnsureTrailingBlock — the three terminal-state branches.
func TestEnsureTrailingBlock(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},                 // empty -> untouched
		{"text", "text\n\n"},     // no newline -> add two
		{"text\n", "text\n\n"},   // one newline -> add one
		{"text\n\n", "text\n\n"}, // already a block -> untouched
	}
	for _, c := range cases {
		var sb strings.Builder
		sb.WriteString(c.in)
		ensureTrailingBlock(&sb)
		if sb.String() != c.want {
			t.Errorf("ensureTrailingBlock(%q) = %q, want %q", c.in, sb.String(), c.want)
		}
	}
}

// TestBatchedModeMaxText — configured MaxTextBytes wins; otherwise the
// batched default applies.
func TestBatchedModeMaxText(t *testing.T) {
	if got := (&WebFetchTool{MaxTextBytes: 123}).batchedModeMaxText(); got != 123 {
		t.Errorf("configured MaxTextBytes should win; got %d", got)
	}
	if got := (&WebFetchTool{}).batchedModeMaxText(); got != defaultWebFetchBatchedMaxTextBytes {
		t.Errorf("default batched cap = %d, want %d", got, defaultWebFetchBatchedMaxTextBytes)
	}
}
