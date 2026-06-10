// Phase 11 follow-up - MultiBackend fan-out aggregator for web_search.
//
// One query, N backends, concurrent fan-out, interleaved merge with URL
// dedup. Each backend gets its own PerBackendTimeout so a slow or dead
// backend can't hold the whole query hostage - its failure ends up in
// LastErrors() instead. The primary motivation is robustness: pair a
// commercial backend (Brave) with a free fallback (DuckDuckGo) and a
// long-tail backend (SearXNG) without paying the latency cost of the
// slowest one.
//
// Result merge is interleave-by-rank: walk rank=1,2,3,... and at each
// rank take the first not-yet-seen URL from each backend in registration
// order. This biases toward consensus (URLs that show up first in every
// backend) while still letting a single backend's unique top result
// surface.
//
// All-fail policy: if every selected backend errors, the call returns
// nil + a wrapped error listing each backend's failure. If at least one
// backend succeeds (even with zero results) the call returns whatever
// merged results we have + nil; the per-backend errors stay accessible
// via LastErrors(). The intent is that the model sees results when any
// route works, and the operator sees the full error map for debugging.

package tools

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// defaultPerBackendTimeout caps each fan-out goroutine. 5s is a deliberate
// trade-off: long enough for a healthy backend to round-trip, short enough
// that a hung connection doesn't pull the median latency up.
const defaultPerBackendTimeout = 5 * time.Second

// MultiBackend fans a single query out across a primary backend and zero
// or more auxiliary backends concurrently. It satisfies SearchBackend, so
// callers can treat it as a drop-in for any single backend.
type MultiBackend struct {
	// Primary is the first backend in the merge order. Required.
	Primary SearchBackend
	// Aux are additional backends, merged in declaration order after Primary.
	Aux []SearchBackend
	// PerBackendTimeout caps each backend's individual fan-out call.
	// Zero falls back to defaultPerBackendTimeout (5s).
	PerBackendTimeout time.Duration

	mu       sync.Mutex
	lastErrs map[string]error
}

// NewMultiBackend constructs a MultiBackend with the given primary +
// optional auxiliaries. Use this rather than the struct literal when
// you want defaults applied.
func NewMultiBackend(primary SearchBackend, aux ...SearchBackend) *MultiBackend {
	return &MultiBackend{
		Primary:           primary,
		Aux:               aux,
		PerBackendTimeout: defaultPerBackendTimeout,
		lastErrs:          map[string]error{},
	}
}

// Name returns the static "multi" identifier; individual contributing
// backend names are available via Names().
func (*MultiBackend) Name() string { return "multi" }

// Search runs every backend (Primary + Aux) concurrently and returns the
// interleaved-dedup top max. Equivalent to SearchSubset(ctx, query, max,
// nil, 0) - i.e. no name filter, each backend gets the full quota.
func (m *MultiBackend) Search(ctx context.Context, query string, max int) ([]SearchResult, error) {
	return m.SearchSubset(ctx, query, max, nil, 0)
}

// SearchSubset is the full fan-out entry point.
//
//   - subset: when non-empty, restrict the fan-out to backends whose
//     Name() is in the list. Unknown names are silently dropped (a
//     typo in config shouldn't take the whole query down). Empty/nil
//     = all backends.
//   - perBackendMax: when >0, the per-backend quota passed to each
//     backend's Search(). When 0, each backend receives max - i.e. the
//     trim happens post-merge.
//
// If the subset filters out every backend, returns (nil, nil) and
// records a sentinel "subset matched no backends" error under the
// "multi" key in LastErrors. Empty results + nil error means "I ran but
// found nothing"; this signals "I had nothing to run".
func (m *MultiBackend) SearchSubset(ctx context.Context, query string, max int, subset []string, perBackendMax int) ([]SearchResult, error) {
	// Reset LastErrors at the start of every call. The contract is
	// "snapshot of the most recent run" - stale entries from a previous
	// run would mislead the caller.
	m.mu.Lock()
	m.lastErrs = map[string]error{}
	m.mu.Unlock()

	if max <= 0 {
		// Zero quota means no backend call needed. Returning early also
		// avoids spawning goroutines we'd immediately throw away.
		return nil, nil
	}

	all := m.allBackends()
	selected := filterBackends(all, subset)
	if len(selected) == 0 {
		// Either no backends configured at all, or subset rejected
		// everything. Either way we record the sentinel + return empty.
		m.recordErr("multi", fmt.Errorf("subset matched no backends"))
		return nil, nil
	}

	timeout := m.PerBackendTimeout
	if timeout == 0 {
		timeout = defaultPerBackendTimeout
	}
	quota := perBackendMax
	if quota <= 0 {
		quota = max
	}

	// Fan-out. Each goroutine derives its own timeout-capped ctx from
	// the parent so parent cancellation propagates naturally.
	type backendOutcome struct {
		name    string
		results []SearchResult
		err     error
	}
	outcomes := make(chan backendOutcome, len(selected))
	var wg sync.WaitGroup
	for _, b := range selected {
		wg.Add(1)
		go func(b SearchBackend) {
			defer wg.Done()
			cctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			results, err := b.Search(cctx, query, quota)
			// Stamp Source on every result. The web_search struct
			// gains a Source field in the integration step; setting
			// it here means tests don't need the integration in place.
			for i := range results {
				results[i].Source = b.Name()
			}
			outcomes <- backendOutcome{name: b.Name(), results: results, err: err}
		}(b)
	}

	// Always wait for every goroutine to finish before closing the
	// outcomes channel — closing it while a worker still has a send
	// pending would panic ("send on closed channel"). Per-backend ctxs
	// are derived from `ctx` via WithTimeout, so a cancelled parent
	// propagates and unwinds workers promptly (every backend's Search
	// method honors ctx per the SearchBackend contract); the wait is
	// bounded by PerBackendTimeout at worst.
	wg.Wait()
	close(outcomes)
	parentCancelled := ctx.Err() != nil

	// Collect everything currently in the channel.
	byName := make(map[string][]SearchResult, len(selected))
	for o := range outcomes {
		if o.err != nil {
			m.recordErr(o.name, o.err)
			continue
		}
		byName[o.name] = o.results
	}

	// If parent ctx was cancelled and some backends never reported,
	// record a ctx-cancelled error for each missing backend so
	// LastErrors reflects the full picture.
	if parentCancelled {
		ctxErr := ctx.Err()
		if ctxErr == nil {
			ctxErr = context.Canceled
		}
		for _, b := range selected {
			name := b.Name()
			if _, ok := byName[name]; ok {
				continue
			}
			if _, ok := m.LastErrors()[name]; ok {
				continue
			}
			m.recordErr(name, ctxErr)
		}
	}

	merged := interleaveByRank(selected, byName, max)

	// All-fail: every selected backend errored. Build a wrapped error
	// listing each failure so the caller can log it; LastErrors still
	// carries the structured map.
	if len(byName) == 0 {
		errs := m.LastErrors()
		if parentCancelled {
			return nil, ctx.Err()
		}
		return nil, wrapAllErrors(errs)
	}

	// At least one backend succeeded. If parent ctx was cancelled we
	// still return ctx.Err() at the end - the caller asked us to stop -
	// but the partial results are visible to anyone who calls
	// LastErrors() to introspect.
	if parentCancelled {
		return merged, ctx.Err()
	}
	return merged, nil
}

// LastErrors returns a snapshot of the per-backend errors from the most
// recent Search/SearchSubset call. Successful backends are omitted. Safe
// to call concurrently with other LastErrors calls; not synchronised
// against an in-flight Search (callers should not call both at once).
func (m *MultiBackend) LastErrors() map[string]error {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]error, len(m.lastErrs))
	for k, v := range m.lastErrs {
		out[k] = v
	}
	return out
}

// Names returns the Name() of every contributing backend, Primary first,
// then Aux in declaration order. Used by the web_search tool to populate
// the JSON output.Backends field.
func (m *MultiBackend) Names() []string {
	all := m.allBackends()
	out := make([]string, 0, len(all))
	for _, b := range all {
		out = append(out, b.Name())
	}
	return out
}

// Backends returns the underlying backend slice, Primary first then Aux.
// Production code should prefer Names(); this exists for tests that need
// to introspect the chain.
func (m *MultiBackend) Backends() []SearchBackend {
	return m.allBackends()
}

// allBackends returns Primary + Aux as a single slice, skipping a nil
// Primary so callers don't need to defensively check.
func (m *MultiBackend) allBackends() []SearchBackend {
	out := make([]SearchBackend, 0, 1+len(m.Aux))
	if m.Primary != nil {
		out = append(out, m.Primary)
	}
	out = append(out, m.Aux...)
	return out
}

// filterBackends narrows backends to those whose Name() is in subset.
// Empty subset = pass-through. Unknown names are silently dropped.
// Ordering follows the input backends slice, not the subset slice -
// merge ordering must stay deterministic across calls.
func filterBackends(backends []SearchBackend, subset []string) []SearchBackend {
	if len(subset) == 0 {
		return backends
	}
	want := make(map[string]struct{}, len(subset))
	for _, s := range subset {
		want[s] = struct{}{}
	}
	out := make([]SearchBackend, 0, len(backends))
	for _, b := range backends {
		if _, ok := want[b.Name()]; ok {
			out = append(out, b)
		}
	}
	return out
}

// recordErr stashes a per-backend error into the LastErrors map. Locked
// because goroutines from the fan-out call it concurrently.
func (m *MultiBackend) recordErr(name string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lastErrs == nil {
		m.lastErrs = map[string]error{}
	}
	m.lastErrs[name] = err
}

// interleaveByRank merges per-backend result slices using interleave-by-rank
// dedup-by-URL. The algorithm:
//
//	for r = 1, 2, 3, ...
//	  for each backend in registration order
//	    if that backend has a result at rank r whose URL is not yet taken,
//	    add it to merged
//	  stop when merged is full
//
// Each result keeps its original Rank from its source backend (the field
// reflects "where this URL placed on its backend's SERP", not "where it
// landed in the merged list"). Result order in the returned slice is the
// merge order.
func interleaveByRank(backends []SearchBackend, byName map[string][]SearchResult, max int) []SearchResult {
	if max <= 0 {
		return nil
	}
	// Determine the maximum rank we'll need to scan. A backend's results
	// are not guaranteed to be 1-indexed contiguous (some backends might
	// produce sparse ranks), so we collect distinct ranks per backend.
	rankSets := make(map[string][]int, len(backends))
	maxRank := 0
	for _, b := range backends {
		results := byName[b.Name()]
		ranks := make([]int, 0, len(results))
		for _, r := range results {
			ranks = append(ranks, r.Rank)
			if r.Rank > maxRank {
				maxRank = r.Rank
			}
		}
		sort.Ints(ranks)
		rankSets[b.Name()] = ranks
	}
	// If every backend returned non-positive ranks (shouldn't happen but
	// guard anyway), fall back to slice index ordering.
	if maxRank == 0 {
		return interleaveByIndex(backends, byName, max)
	}

	seen := make(map[string]struct{})
	out := make([]SearchResult, 0, max)
	for r := 1; r <= maxRank && len(out) < max; r++ {
		for _, b := range backends {
			if len(out) >= max {
				break
			}
			results := byName[b.Name()]
			// Linear scan is fine; result lists are short (max ~20).
			for _, res := range results {
				if res.Rank != r {
					continue
				}
				key := normaliseURL(res.URL)
				if key == "" {
					// Skip empty-URL rows; they can't be deduplicated
					// meaningfully and they're not useful to the model.
					continue
				}
				if _, taken := seen[key]; taken {
					continue
				}
				seen[key] = struct{}{}
				out = append(out, res)
				break
			}
		}
	}
	return out
}

// interleaveByIndex is the fallback merger used when ranks are unusable
// (all zero / negative). Treats each backend's result slice positionally.
func interleaveByIndex(backends []SearchBackend, byName map[string][]SearchResult, max int) []SearchResult {
	seen := make(map[string]struct{})
	out := make([]SearchResult, 0, max)
	maxLen := 0
	for _, b := range backends {
		if n := len(byName[b.Name()]); n > maxLen {
			maxLen = n
		}
	}
	for i := 0; i < maxLen && len(out) < max; i++ {
		for _, b := range backends {
			if len(out) >= max {
				break
			}
			results := byName[b.Name()]
			if i >= len(results) {
				continue
			}
			res := results[i]
			key := normaliseURL(res.URL)
			if key == "" {
				continue
			}
			if _, taken := seen[key]; taken {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, res)
		}
	}
	return out
}

// normaliseURL is the dedup key. It strips a trailing slash and lowercases
// the scheme+host to catch "example.com/" vs "example.com" duplicates that
// otherwise survive into the merged list. We intentionally do NOT strip
// query strings or fragments - those carry semantic content for many sites.
func normaliseURL(u string) string {
	u = strings.TrimSpace(u)
	if u == "" {
		return ""
	}
	// Trim a single trailing slash on bare paths, but only if there's no
	// query string or fragment after it. Otherwise the slash is structural.
	if !strings.ContainsAny(u, "?#") && strings.HasSuffix(u, "/") {
		u = strings.TrimRight(u, "/")
	}
	// Lowercase scheme+host. Path/query stay case-sensitive.
	if i := strings.Index(u, "://"); i >= 0 {
		end := i + 3
		rest := u[end:]
		slash := strings.IndexAny(rest, "/?#")
		if slash < 0 {
			return strings.ToLower(u)
		}
		return strings.ToLower(u[:end+slash]) + rest[slash:]
	}
	return u
}

// wrapAllErrors builds a single error from a per-backend error map. Keys
// are sorted so the message is stable across runs (important for tests
// and for log diffing in production).
func wrapAllErrors(errs map[string]error) error {
	if len(errs) == 0 {
		return errors.New("multi: all backends failed (no detail recorded)")
	}
	keys := make([]string, 0, len(errs))
	for k := range errs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %v", k, errs[k]))
	}
	return fmt.Errorf("multi: all backends failed: %s", strings.Join(parts, "; "))
}
