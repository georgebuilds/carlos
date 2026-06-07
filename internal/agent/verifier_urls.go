// Phase 5 slice 5d - URLRefetcher adapter.
//
// Extracts URLs from the artifact body (via the existing
// CitationAuditor regex), HEADs each one (falling back to GET on
// 405/501), and scores by the fraction of URLs that responded with a
// healthy status (2xx; 3xx counts as healthy if redirect-followable;
// 4xx/5xx/network-error counts as broken).
//
// Concurrency cap: 8 simultaneous fetches. The HTTP client has a 5s
// per-URL timeout. Both knobs are exposed on URLRefetcherVerifier for
// tests that need to dial them up or down.
//
// Decision boundaries match decisionFromRatio in verifier_adapters.go:
//
//	healthy fraction >= 0.95 → accept
//	healthy fraction <  0.5  → reject
//	otherwise                → needs_revision
//
// Score scales: scoreFromRatio(healthy/total).
package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

// URLRefetcherVerifier is the tool-grounded citation-refetch check.
type URLRefetcherVerifier struct {
	// Client is the HTTP client used for HEAD / GET. Default (nil) =
	// a client with a 5s per-request timeout and no redirect-following
	// (we want to see the 3xx status, not follow blindly into a fresh
	// status). Tests inject a custom client to swap in httptest.Server
	// transports or to disable network entirely.
	Client *http.Client

	// PerURLTimeout overrides the Client's per-request timeout. Only
	// honored when Client is nil (since we own client construction in
	// that case). Default (zero) = urlRefetcherPerURLTimeout.
	PerURLTimeout time.Duration

	// MaxParallel caps simultaneous fetches. Default (zero) =
	// urlRefetcherMaxParallel.
	MaxParallel int
}

const (
	urlRefetcherPerURLTimeout = 5 * time.Second
	urlRefetcherMaxParallel   = 8
)

// NewURLRefetcherVerifier returns a URLRefetcherVerifier with default
// timeout and concurrency.
func NewURLRefetcherVerifier() *URLRefetcherVerifier {
	return &URLRefetcherVerifier{
		PerURLTimeout: urlRefetcherPerURLTimeout,
		MaxParallel:   urlRefetcherMaxParallel,
	}
}

// Name implements ToolGroundedVerifier. Returns "urls".
func (*URLRefetcherVerifier) Name() string { return "urls" }

// urlStatus is the per-URL fetch outcome.
type urlStatus struct {
	URL    string
	Status int    // HTTP status code; 0 on network error
	Err    string // non-empty when Status == 0
}

// Verify extracts URLs from content (using CitationAuditor), fetches
// each, and produces the report. workdir is ignored - URL fetches are
// network-only.
func (v *URLRefetcherVerifier) Verify(ctx context.Context, workdir string, content []byte) (VerificationReport, error) {
	_ = workdir

	urls := extractURLsForRefetch(content)
	if len(urls) == 0 {
		// No URLs to check is a trivially clean accept; the artifact
		// makes no factual claims that point at remote sources. Score
		// 10 keeps it above acceptScoreThreshold so the queue gate
		// short-circuits.
		return VerificationReport{
			Decision:   VerificationAccept,
			Score:      10,
			JudgeModel: "urls:none",
		}, nil
	}

	client := v.Client
	if client == nil {
		perURL := v.PerURLTimeout
		if perURL <= 0 {
			perURL = urlRefetcherPerURLTimeout
		}
		client = &http.Client{
			Timeout: perURL,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				// We want to see the 3xx redirect status verbatim,
				// not follow it. http.ErrUseLastResponse is the
				// stdlib idiom to halt redirect-following while still
				// returning the response.
				return http.ErrUseLastResponse
			},
		}
	}
	parallel := v.MaxParallel
	if parallel <= 0 {
		parallel = urlRefetcherMaxParallel
	}

	statuses := fetchURLs(ctx, client, urls, parallel)

	healthy, broken := classifyStatuses(statuses)
	total := len(statuses)
	if total == 0 {
		// Defensive: if every URL was somehow skipped we don't want
		// to divide by zero. Treat as no-URL case.
		return VerificationReport{
			Decision:   VerificationAccept,
			Score:      10,
			JudgeModel: "urls:none",
		}, nil
	}
	ratio := float64(healthy) / float64(total)

	report := VerificationReport{
		JudgeModel: fmt.Sprintf("urls:%d", total),
		Score:      scoreFromRatio(ratio),
		Decision:   decisionFromRatio(ratio),
	}
	if len(broken) > 0 {
		concerns := make([]string, 0, len(broken)+1)
		concerns = append(concerns, fmt.Sprintf("%d/%d URLs broken", len(broken), total))
		const maxBrokenListed = 8
		listed := broken
		if len(listed) > maxBrokenListed {
			listed = listed[:maxBrokenListed]
		}
		for _, s := range listed {
			if s.Status == 0 {
				concerns = append(concerns, fmt.Sprintf("%s (%s)", s.URL, s.Err))
			} else {
				concerns = append(concerns, fmt.Sprintf("%s (HTTP %d)", s.URL, s.Status))
			}
		}
		report.Concerns = concerns
	}
	return report, nil
}

// extractURLsForRefetch reuses CitationAuditor to pull URLs out of
// content. We take URLs only (skip path / [N] citations) because the
// refetcher's deterministic check is HTTP-only - local paths get
// validated elsewhere, numeric refs don't have a target. Dedupes via
// the auditor's own dedup pass.
func extractURLsForRefetch(content []byte) []string {
	a := (&CitationAuditor{}).Audit(content)
	out := make([]string, 0, len(a.Citations))
	for _, c := range a.Citations {
		if isHTTPURL(c) {
			out = append(out, c)
		}
	}
	return out
}

// isHTTPURL is a cheap http(s)://-prefix check. We don't url.Parse
// because the auditor already constrained the alphabet that can appear
// in a "URL" citation; we just need to drop the path/ref entries.
func isHTTPURL(s string) bool {
	return len(s) >= 7 && (s[:7] == "http://" || (len(s) >= 8 && s[:8] == "https://"))
}

// fetchURLs HEADs every URL in parallel (capped at maxParallel), with
// GET fallback on 405/501. Returns a urlStatus per URL in input order.
//
// Implementation notes:
//   - sync.WaitGroup + semaphore channel for the parallelism cap, same
//     pattern as the existing internal/agent/spawn.go fan-out.
//   - per-request context is derived from the parent ctx so a global
//     cancel kills outstanding fetches.
//   - we do not retry on transient errors. The auditor will run on
//     replay if the user wants to re-verify after a flake.
func fetchURLs(ctx context.Context, client *http.Client, urls []string, maxParallel int) []urlStatus {
	out := make([]urlStatus, len(urls))
	sem := make(chan struct{}, maxParallel)
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, u string) {
			defer wg.Done()
			defer func() { <-sem }()
			out[i] = fetchOne(ctx, client, u)
		}(i, u)
	}
	wg.Wait()
	return out
}

// fetchOne issues HEAD; if the server returns 405/501 (HEAD not
// allowed / not implemented), we retry with GET. We don't read the
// body - the status code is enough signal.
func fetchOne(ctx context.Context, client *http.Client, u string) urlStatus {
	resp, err := doRequest(ctx, client, http.MethodHead, u)
	if err == nil && (resp.StatusCode == http.StatusMethodNotAllowed || resp.StatusCode == http.StatusNotImplemented) {
		// Drain + close before reissue so the connection can be reused.
		closeResponse(resp)
		resp, err = doRequest(ctx, client, http.MethodGet, u)
	}
	if err != nil {
		return urlStatus{URL: u, Err: err.Error()}
	}
	defer closeResponse(resp)
	return urlStatus{URL: u, Status: resp.StatusCode}
}

// doRequest builds a request with the parent ctx and runs it. Wraps
// the boilerplate so fetchOne stays focused.
func doRequest(ctx context.Context, client *http.Client, method, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	// A polite UA so endpoints that 403 default UAs treat us as a
	// genuine client. carlos identifies itself; we don't impersonate
	// a browser.
	req.Header.Set("User-Agent", "carlos-verifier/1.0 (+phase5d)")
	return client.Do(req)
}

// closeResponse drains and closes resp.Body. Safe to call with a nil
// resp - useful for the dual-method fetch path above.
func closeResponse(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_ = resp.Body.Close()
}

// classifyStatuses splits the urlStatus slice into (healthy count,
// broken slice). Healthy = 2xx; 3xx counts as healthy if redirect-
// followable (any 3xx - slice 5d brief lumps them all as "followable").
// Broken = 4xx, 5xx, or network error.
//
// The broken slice is sorted by status code ascending (with network
// errors first) so the concerns list is stable across runs.
func classifyStatuses(statuses []urlStatus) (int, []urlStatus) {
	healthy := 0
	var broken []urlStatus
	for _, s := range statuses {
		switch {
		case s.Status >= 200 && s.Status < 400:
			healthy++
		default:
			broken = append(broken, s)
		}
	}
	sort.SliceStable(broken, func(i, j int) bool {
		return broken[i].Status < broken[j].Status
	})
	return healthy, broken
}

// ErrURLRefetcherNoNetwork is a sentinel callers can errors.Is against
// when they want to surface "verifier could not reach the network" as
// a distinct outcome. Today we never return it - all URL errors are
// folded into the broken count - but the sentinel reserves the name
// for a future per-error-class enhancement.
var ErrURLRefetcherNoNetwork = errors.New("urls: network unreachable")

// Compile-time check: URLRefetcherVerifier implements ToolGroundedVerifier.
var _ ToolGroundedVerifier = (*URLRefetcherVerifier)(nil)
