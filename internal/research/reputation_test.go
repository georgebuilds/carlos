package research

// White-box tests for the reputation module. Package `research` (not
// `research_test`) so the unexported helpers are reachable directly -
// matches the whitebox_more_test.go convention in this package.

import (
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/tools"
)

func TestExtractDomain(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"whitespace only", "   ", ""},
		{"plain https", "https://example.com/path", "example.com"},
		{"strips www", "https://www.example.com", "example.com"},
		{"strips port", "https://example.com:8443/x", "example.com"},
		{"subdomain kept", "https://en.wikipedia.org/wiki/Go", "en.wikipedia.org"},
		{"no scheme", "example.com/path", "example.com"},
		{"no scheme with www", "www.example.com", "example.com"},
		{"ipv4 host", "http://192.168.0.1:80/admin", "192.168.0.1"},
		{"uppercase host", "HTTPS://EXAMPLE.COM", "example.com"},
		{"trailing dot", "https://example.com./x", "example.com"},
		{"userinfo no scheme", "user@host.example.com/p", "host.example.com"},
		{"bare token", "localhost", "localhost"},
		{"control-char garbage", "ht\x00tp://\x00", "ht\x00tp"},
		// A bare "%" fails url.Parse both with and without the synthetic
		// scheme, so the leading-token fallback runs and returns it
		// verbatim (no slash, no port, no userinfo to strip).
		{"unparseable percent", "%", "%"},
		// Unparseable both ways AND carries userinfo: the fallback strips
		// up to and including the "@", leaving the bare token.
		{"unparseable with userinfo", "u@%", "%"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := extractDomain(tt.in); got != tt.want {
				t.Errorf("extractDomain(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestExtractDomain_IPv6Bracketed(t *testing.T) {
	// Bracketed IPv6 literals are handled by url.Parse on the parsed
	// paths; assert we don't mangle one when a scheme is present.
	got := extractDomain("http://[2001:db8::1]:8080/x")
	if got != "2001:db8::1" {
		t.Errorf("extractDomain ipv6 = %q, want 2001:db8::1", got)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name  string
		url   string
		title string
		want  Tier
	}{
		{"edu trusted", "https://stanford.edu/page", "Lecture", TierTrusted},
		{"gov trusted", "https://nasa.gov/mission", "Apollo", TierTrusted},
		{"arxiv trusted", "https://arxiv.org/abs/1234", "Paper", TierTrusted},
		{"wikipedia subdomain trusted", "https://en.wikipedia.org/wiki/Go", "Go", TierTrusted},
		{"github trusted", "https://github.com/golang/go", "go", TierTrusted},
		{"ac.uk trusted", "https://cam.ac.uk/research", "Research", TierTrusted},
		{"plain https medium", "https://someblog.example/post", "A post", TierMedium},
		{"http low", "http://someblog.example/post", "A post", TierLow},
		{"empty url low", "", "title", TierLow},
		{"seo title on trusted host still low", "https://github.com/x", "Top-10 best repos", TierLow},
		{"seo domain low", "https://top10deals.example/x", "Cheap stuff", TierLow},
		{"content farm low", "https://www.wikihow.com/Do-Thing", "How to", TierLow},
		{"near-miss not trusted", "https://notarxiv.org/x", "X", TierMedium},
		{"suffix substring not trusted", "https://myedu.com/x", "X", TierMedium},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classify(tt.url, tt.title).Tier
			if got != tt.want {
				t.Errorf("classify(%q,%q).Tier = %v, want %v", tt.url, tt.title, got, tt.want)
			}
		})
	}
}

func TestClassify_ScoreMatchesTier(t *testing.T) {
	cases := map[string]Tier{
		"https://mit.edu":        TierTrusted,
		"https://blog.example/x": TierMedium,
		"http://blog.example/x":  TierLow,
	}
	for u, wantTier := range cases {
		rep := classify(u, "")
		if rep.Tier != wantTier {
			t.Errorf("classify(%q) tier = %v want %v", u, rep.Tier, wantTier)
		}
		if rep.Score != int(rep.Tier) {
			t.Errorf("classify(%q) score = %d want %d", u, rep.Score, int(rep.Tier))
		}
	}
}

func TestTierString(t *testing.T) {
	cases := map[Tier]string{
		TierTrusted: "trusted",
		TierMedium:  "medium",
		TierLow:     "low",
		Tier(99):    "low", // default arm
	}
	for tier, want := range cases {
		if got := tier.String(); got != want {
			t.Errorf("Tier(%d).String() = %q want %q", tier, got, want)
		}
	}
}

func TestMatchesSuffix(t *testing.T) {
	tests := []struct {
		host, suffix string
		want         bool
	}{
		{"arxiv.org", "arxiv.org", true},
		{"foo.arxiv.org", "arxiv.org", true},
		{"notarxiv.org", "arxiv.org", false},
		{"stanford.edu", ".edu", true},
		{"notedu", ".edu", false},
		{"x.edu.com", ".edu", false},
	}
	for _, tt := range tests {
		if got := matchesSuffix(tt.host, tt.suffix); got != tt.want {
			t.Errorf("matchesSuffix(%q,%q) = %v want %v", tt.host, tt.suffix, got, tt.want)
		}
	}
}

func TestIsHTTPS(t *testing.T) {
	tests := []struct {
		url  string
		want bool
	}{
		{"https://x.com", true},
		{"HTTPS://x.com", true},
		{"http://x.com", false},
		{"x.com", false},
		{"", false},
		{"%", false}, // url.Parse error branch -> not provably https
	}
	for _, tt := range tests {
		if got := isHTTPS(tt.url); got != tt.want {
			t.Errorf("isHTTPS(%q) = %v want %v", tt.url, got, tt.want)
		}
	}
}

func TestRankByReputation_StableWithinTier(t *testing.T) {
	in := []tools.SearchResult{
		{Rank: 1, Title: "low a", URL: "http://low-a.example"},   // low (http)
		{Rank: 2, Title: "trusted", URL: "https://mit.edu/x"},    // trusted
		{Rank: 3, Title: "low b", URL: "http://low-b.example"},   // low (http)
		{Rank: 4, Title: "medium", URL: "https://blog.example"},  // medium
		{Rank: 5, Title: "trusted2", URL: "https://arxiv.org/y"}, // trusted
	}
	got := rankByReputation(in)

	// Expected order: trusted (mit, arxiv in original order), medium,
	// then low (low-a, low-b in original order).
	wantURLs := []string{
		"https://mit.edu/x",
		"https://arxiv.org/y",
		"https://blog.example",
		"http://low-a.example",
		"http://low-b.example",
	}
	if len(got) != len(wantURLs) {
		t.Fatalf("len = %d want %d", len(got), len(wantURLs))
	}
	for i, w := range wantURLs {
		if got[i].URL != w {
			t.Errorf("rank[%d].URL = %q want %q (full=%+v)", i, got[i].URL, w, got)
		}
	}

	// Input must not be mutated.
	if in[0].URL != "http://low-a.example" {
		t.Errorf("input slice was mutated: %+v", in)
	}
}

func TestRankByReputation_Empty(t *testing.T) {
	if got := rankByReputation(nil); len(got) != 0 {
		t.Errorf("rankByReputation(nil) = %+v want empty", got)
	}
}

func TestFlagLowTierDomination_EmptyIsNoOp(t *testing.T) {
	// Defensive early return: the engine never calls this with an empty
	// source set (runSearch fails first), but the guard must hold.
	report := &Report{}
	flagLowTierDomination(report)
	if len(report.Concerns) != 0 {
		t.Errorf("empty report should record no concern; got %v", report.Concerns)
	}
}

func TestFlagLowTierDomination_NotTrippedAtExactlyHalf(t *testing.T) {
	// Threshold is strictly greater-than 0.5, so a 1/2 low split must
	// NOT trip the concern.
	report := &Report{Sources: []Source{
		{ID: "s1", Reputation: Reputation{Tier: TierLow}},
		{ID: "s2", Reputation: Reputation{Tier: TierTrusted}},
	}}
	flagLowTierDomination(report)
	for _, c := range report.Concerns {
		if strings.Contains(c, "dominated by low-tier") {
			t.Errorf("50%% low split should not trip concern; got %v", report.Concerns)
		}
	}
}

func TestLowTierFraction(t *testing.T) {
	tests := []struct {
		name string
		in   []tools.SearchResult
		want float64
	}{
		{"empty", nil, 0},
		{
			"all low",
			[]tools.SearchResult{{URL: "http://a.example"}, {URL: "http://b.example"}},
			1.0,
		},
		{
			"half low",
			[]tools.SearchResult{{URL: "http://a.example"}, {URL: "https://mit.edu"}},
			0.5,
		},
		{
			"none low",
			[]tools.SearchResult{{URL: "https://mit.edu"}, {URL: "https://arxiv.org/x"}},
			0.0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lowTierFraction(tt.in); got != tt.want {
				t.Errorf("lowTierFraction = %v want %v", got, tt.want)
			}
		})
	}
}
