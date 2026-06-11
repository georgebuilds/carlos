package tools

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestInterleaveByIndex_ZeroRankFallback — when every backend returns
// rank-0 results, SearchSubset falls back to positional interleave. We
// drive it through the public SearchSubset entry to keep the test honest
// about the dispatch in interleaveByRank.
func TestInterleaveByIndex_ZeroRankFallback(t *testing.T) {
	a := &fakeMulti{name: "a", results: []SearchResult{
		{Rank: 0, URL: "https://a.example/1", Title: "a1"},
		{Rank: 0, URL: "https://a.example/2", Title: "a2"},
	}}
	b := &fakeMulti{name: "b", results: []SearchResult{
		{Rank: 0, URL: "https://b.example/1", Title: "b1"},
	}}
	m := NewMultiBackend(a, b)
	got, _, err := m.SearchSubset(context.Background(), "q", 10, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// Positional interleave: a[0], b[0], a[1].
	wantURLs := []string{"https://a.example/1", "https://b.example/1", "https://a.example/2"}
	if len(got) != len(wantURLs) {
		t.Fatalf("got %d results, want %d: %+v", len(got), len(wantURLs), got)
	}
	for i, w := range wantURLs {
		if got[i].URL != w {
			t.Errorf("result[%d] = %q, want %q", i, got[i].URL, w)
		}
	}
}

// TestInterleaveByIndex_DedupAndEmptyURL — index fallback dedups by
// normalised URL and skips empty-URL rows.
func TestInterleaveByIndex_DedupAndEmptyURL(t *testing.T) {
	a := &fakeMulti{name: "a", results: []SearchResult{
		{Rank: 0, URL: "https://dup.example/", Title: "a-dup"},
		{Rank: 0, URL: "", Title: "empty"},
	}}
	b := &fakeMulti{name: "b", results: []SearchResult{
		// Same URL without trailing slash -> normalised duplicate of a's.
		{Rank: 0, URL: "https://dup.example", Title: "b-dup"},
		{Rank: 0, URL: "https://b.example/unique", Title: "b-unique"},
	}}
	m := NewMultiBackend(a, b)
	got, _, err := m.SearchSubset(context.Background(), "q", 10, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var urls []string
	for _, r := range got {
		urls = append(urls, r.URL)
	}
	// dup.example appears once (a wins, position 0); empty skipped; b-unique kept.
	if len(got) != 2 {
		t.Fatalf("expected 2 deduped results, got %d: %v", len(got), urls)
	}
	if got[0].URL != "https://dup.example/" {
		t.Errorf("first result should be a's dup row; got %q", got[0].URL)
	}
	if got[1].URL != "https://b.example/unique" {
		t.Errorf("second should be b-unique; got %q", got[1].URL)
	}
}

// TestNormaliseURL_HostOnly — a bare scheme://host with no path is fully
// lowercased (the slash<0 branch).
func TestNormaliseURL_HostOnly(t *testing.T) {
	cases := map[string]string{
		"https://Example.COM":      "https://example.com",
		"HTTPS://Example.com/Path": "https://example.com/Path",
		"https://example.com/":     "https://example.com",
		"https://example.com/?q=1": "https://example.com/?q=1",
		"  ":                       "",
		"relative/path":            "relative/path",
	}
	for in, want := range cases {
		if got := normaliseURL(in); got != want {
			t.Errorf("normaliseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestSearchSubset_ZeroTimeoutUsesDefault — a struct-literal MultiBackend
// (PerBackendTimeout==0) falls back to the default timeout instead of
// using a 0 deadline that would cancel every backend immediately.
func TestSearchSubset_ZeroTimeoutUsesDefault(t *testing.T) {
	a := &fakeMulti{name: "a", results: []SearchResult{
		mkResult(1, "https://a.example/1", "a1"),
	}}
	// Struct literal, NOT NewMultiBackend, so PerBackendTimeout is 0.
	m := &MultiBackend{Primary: a}
	got, _, err := m.SearchSubset(context.Background(), "q", 5, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("zero-timeout default should still return results; got %d", len(got))
	}
}

// TestWrapAllErrors_Empty — wrapAllErrors with an empty map returns the
// no-detail sentinel rather than a nil error.
func TestWrapAllErrors_Empty(t *testing.T) {
	err := wrapAllErrors(map[string]error{})
	if err == nil {
		t.Fatal("expected a non-nil error for empty map")
	}
	if !strings.Contains(err.Error(), "no detail recorded") {
		t.Errorf("want no-detail sentinel, got %v", err)
	}
}

// TestWrapAllErrors_SortedStable — multiple errors are listed in sorted
// key order for stable messages.
func TestWrapAllErrors_SortedStable(t *testing.T) {
	err := wrapAllErrors(map[string]error{
		"zeta":  errors.New("z-down"),
		"alpha": errors.New("a-down"),
	})
	msg := err.Error()
	if strings.Index(msg, "alpha") > strings.Index(msg, "zeta") {
		t.Errorf("keys should be sorted alpha before zeta; got %q", msg)
	}
}
