package research

import (
	"net/url"
	"sort"
	"strings"

	"github.com/georgebuilds/carlos/internal/tools"
)

// reputation.go is the shared, HEURISTIC-ONLY source-quality module
// used by two phases:
//
//   - search (item 11j): ranks deduped results by trust tier so
//     higher-quality domains are fetched and read first, and flags a
//     concern when the result set is dominated by low-tier sources.
//   - synthesize (item 11g): tags each passage with its source's tier
//     and whether it is single-sourced so the model can caveat weak
//     claims, and records weak-source observations as concerns.
//
// No network calls, no new dependencies. Every signal is computed from
// data already in hand: the URL, the title, and the http/https scheme.
// The whitelist and SEO patterns below are deliberately plain package
// vars so they are easy to review and tune.

// Tier is a coarse, three-level trust bucket for a source.
type Tier int

const (
	// TierLow is the default: unknown domain, or one matching an SEO /
	// content-farm downrank heuristic.
	TierLow Tier = iota
	// TierMedium is a plain-https domain with no negative SEO signal but
	// no positive whitelist match either.
	TierMedium
	// TierTrusted is a whitelisted high-authority domain (academic,
	// government, primary reference, canonical code host, …).
	TierTrusted
)

// String renders the tier as a short, stable tag used in prompts and
// concern strings. Stable because the synthesis manifest embeds it and
// the model is told to read it.
func (t Tier) String() string {
	switch t {
	case TierTrusted:
		return "trusted"
	case TierMedium:
		return "medium"
	default:
		return "low"
	}
}

// Reputation is the computed quality verdict for one source. It is
// stored on Source (set during the search ranking pass) so the
// synthesis phase can reuse it without recomputing. Score is a small
// integer used only for stable ranking; it is NOT a calibrated
// probability, just trusted(2) > medium(1) > low(0).
type Reputation struct {
	Tier  Tier
	Score int
}

// trustedSuffixes are domain suffixes that, when matched on a label
// boundary, mark a source as trusted. Academic + government TLDs and a
// curated set of primary references / canonical hosts. Tune freely.
//
// A "suffix on a label boundary" means example.edu and dept.mit.edu
// both match ".edu", but notedu.com does not (it would have to be
// "x.edu" or "edu" exactly).
var trustedSuffixes = []string{
	// Academic / government TLDs.
	".edu",
	".gov",
	".ac.uk",
	".gov.uk",
	".edu.au",
	".gov.au",
	// Primary research + scholarship.
	"arxiv.org",
	"semanticscholar.org",
	"openalex.org",
	"ncbi.nlm.nih.gov",
	"pubmed.ncbi.nlm.nih.gov",
	"nature.com",
	"science.org",
	"acm.org",
	"ieee.org",
	"doi.org",
	// Canonical references.
	"wikipedia.org", // matches *.wikipedia.org (en., de., …)
	"who.int",
	"nist.gov",
	// Canonical code / standards hosts.
	"github.com",
	"gitlab.com",
	"w3.org",
	"ietf.org",
	"rfc-editor.org",
	"python.org",
	"golang.org",
	"go.dev",
	"mozilla.org",
	"developer.mozilla.org",
}

// seoDownrankPatterns are substrings that, found in the domain or the
// title, push a source down to TierLow regardless of scheme. These are
// thin-SEO / aggregator / content-farm tells. Substring match is
// intentional: it is broad on purpose so listicle spam gets caught.
// Keep entries lowercase; matching is case-folded.
var seoDownrankPatterns = []string{
	"top-10",
	"top10",
	"top-100",
	"best-of",
	"listicle",
	"clickbait",
	"buzz",
	"affiliate",
	"coupon",
	"dealsblog",
	"-deals",
	"ezinearticles",
	"hubpages",
	"squidoo",
	"answers.com",
	"ask.com",
	"ehow",
	"wikihow",
	"content-farm",
	"sponsored",
	"you-wont-believe",
	"you-won't-believe",
}

// extractDomain pulls the lowercased host (no scheme, no www., no port)
// from a URL. It degrades gracefully: a URL with no scheme is retried
// with a synthetic "http://" prefix; anything still unparseable falls
// back to a best-effort token before the first slash. Returns "" only
// when the input is empty or yields no host at all.
func extractDomain(rawURL string) string {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return ""
	}
	host := parseHost(raw)
	if host == "" {
		// No scheme (e.g. "example.com/path"): retry with one so
		// url.Parse populates Host rather than Path.
		host = parseHost("http://" + raw)
	}
	if host == "" {
		// Still nothing parseable: best-effort grab of the leading
		// token up to the first slash, then strip any userinfo / port.
		// This branch (and only this branch) may leave a port on, so it
		// does the port strip itself; parsed hosts already had their
		// port removed by url.URL.Hostname and must not be re-stripped
		// (that would mangle an unbracketed IPv6 literal).
		host = raw
		if i := strings.IndexAny(host, "/?#"); i >= 0 {
			host = host[:i]
		}
		if i := strings.LastIndex(host, "@"); i >= 0 {
			host = host[i+1:]
		}
		if i := strings.LastIndex(host, ":"); i >= 0 && !strings.Contains(host, "[") {
			host = host[:i]
		}
	}
	return normalizeHost(host)
}

// parseHost runs url.Parse and returns the hostname (port stripped by
// url.URL.Hostname), or "" if parsing fails or yields no host.
func parseHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

// normalizeHost lowercases, strips a trailing dot and a leading
// "www.". Port handling is done by the caller (url.URL.Hostname for
// parsed hosts, an explicit strip in the leading-token fallback) so
// this stays safe for unbracketed IPv6 literals.
func normalizeHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimSuffix(host, ".")
	host = strings.TrimPrefix(host, "www.")
	return host
}

// matchesSuffix reports whether host equals suffix or ends with it on a
// label boundary. For dotted suffixes (".edu") it is a plain HasSuffix;
// for bare suffixes ("arxiv.org") it also requires the preceding char
// to be "." (or the start) so "notarxiv.org" does not match.
func matchesSuffix(host, suffix string) bool {
	if host == suffix {
		return true
	}
	if strings.HasPrefix(suffix, ".") {
		return strings.HasSuffix(host, suffix)
	}
	return strings.HasSuffix(host, "."+suffix)
}

// classify computes the trust tier for one (url, title) pair. Order of
// precedence:
//
//  1. Unparseable / empty domain  -> low.
//  2. SEO / content-farm tell in domain or title -> low (overrides a
//     whitelist match: a "top-10" article on a trusted host is still
//     thin).
//  3. Whitelisted domain suffix -> trusted.
//  4. Plain https -> medium.
//  5. Anything else (http, or odd) -> low.
func classify(rawURL, title string) Reputation {
	host := extractDomain(rawURL)
	if host == "" {
		return Reputation{Tier: TierLow, Score: int(TierLow)}
	}

	lowerTitle := strings.ToLower(title)
	for _, pat := range seoDownrankPatterns {
		if strings.Contains(host, pat) || strings.Contains(lowerTitle, pat) {
			return Reputation{Tier: TierLow, Score: int(TierLow)}
		}
	}

	for _, suf := range trustedSuffixes {
		if matchesSuffix(host, suf) {
			return Reputation{Tier: TierTrusted, Score: int(TierTrusted)}
		}
	}

	if isHTTPS(rawURL) {
		return Reputation{Tier: TierMedium, Score: int(TierMedium)}
	}
	return Reputation{Tier: TierLow, Score: int(TierLow)}
}

// isHTTPS reports whether the URL uses the https scheme. A URL with no
// scheme is treated as NOT https (we cannot prove transport security),
// which lands it in the low tier unless whitelisted.
func isHTTPS(rawURL string) bool {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Scheme, "https")
}

// rankByReputation returns a NEW slice of results sorted by trust tier,
// highest first, STABLE within a tier. "Stable within a tier" means two
// results with the same tier keep their original relative order (which
// is the backend's search rank), so a reputation pass never scrambles
// otherwise-equal results. The input slice is not mutated.
func rankByReputation(results []tools.SearchResult) []tools.SearchResult {
	out := make([]tools.SearchResult, len(results))
	copy(out, results)
	sort.SliceStable(out, func(i, j int) bool {
		ri := classify(out[i].URL, out[i].Title)
		rj := classify(out[j].URL, out[j].Title)
		return ri.Score > rj.Score
	})
	return out
}

// lowTierFraction returns the share of results in the low tier, in
// [0,1]. An empty slice returns 0 (no domination to flag).
func lowTierFraction(results []tools.SearchResult) float64 {
	if len(results) == 0 {
		return 0
	}
	low := 0
	for _, r := range results {
		if classify(r.URL, r.Title).Tier == TierLow {
			low++
		}
	}
	return float64(low) / float64(len(results))
}

// lowTierDominationThreshold is the fraction of low-tier sources above
// which the search phase records a "dominated by low-tier" concern.
// Strictly greater-than, so a 50/50 split does not trip it.
const lowTierDominationThreshold = 0.5
