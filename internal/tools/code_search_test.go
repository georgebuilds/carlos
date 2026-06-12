package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeIndexer spins up an httptest.Server with handlers per indexer.
// It returns the endpoint map ready to plug into CodeSearchTool.
func fakeIndexer(t *testing.T, handlers map[string]http.HandlerFunc) (map[string]string, func()) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for prefix, fn := range handlers {
			if strings.HasPrefix(r.URL.Path, "/"+prefix) {
				fn(w, r)
				return
			}
		}
		http.NotFound(w, r)
	}))
	endpoints := map[string]string{
		"codewiki": srv.URL + "/codewiki/{owner}/{repo}",
		"context7": srv.URL + "/context7/{owner}/{repo}/llms.txt",
		"deepwiki": srv.URL + "/deepwiki/{owner}/{repo}",
	}
	return endpoints, srv.Close
}

func runCodeSearch(t *testing.T, tool *CodeSearchTool, in codeSearchInput) codeSearchResult {
	t.Helper()
	raw, _ := json.Marshal(in)
	out, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("code_search: %v", err)
	}
	var resp codeSearchResult
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestCodeSearch_DefaultsToCarlosRepo(t *testing.T) {
	endpoints, stop := fakeIndexer(t, map[string]http.HandlerFunc{
		"codewiki": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><head><title>codewiki carlos</title></head><body>codewiki body</body></html>"))
		},
		"context7": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("context7 llms.txt for carlos"))
		},
		"deepwiki": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><head><title>deepwiki carlos</title></head><body>deepwiki body</body></html>"))
		},
	})
	defer stop()
	tool := NewCodeSearchTool()
	tool.Endpoints = endpoints
	resp := runCodeSearch(t, tool, codeSearchInput{})
	if resp.Repo != defaultCodeSearchRepo {
		t.Errorf("default repo = %q, want %q", resp.Repo, defaultCodeSearchRepo)
	}
	if len(resp.Services) != 3 {
		t.Errorf("expected 3 services hit; got %d", len(resp.Services))
	}
	for _, h := range resp.Services {
		if h.Status != http.StatusOK {
			t.Errorf("service %s status = %d; want 200", h.Service, h.Status)
		}
		if h.Excerpt == "" {
			t.Errorf("service %s had no excerpt", h.Service)
		}
	}
}

func TestCodeSearch_ExplicitRepoOverridesDefault(t *testing.T) {
	endpoints, stop := fakeIndexer(t, map[string]http.HandlerFunc{
		"deepwiki": func(w http.ResponseWriter, r *http.Request) {
			if !strings.Contains(r.URL.Path, "anneal") {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><title>anneal</title><body>fastica</body></html>"))
		},
	})
	defer stop()
	tool := NewCodeSearchTool()
	tool.Endpoints = endpoints
	resp := runCodeSearch(t, tool, codeSearchInput{
		Repo:     "georgebuilds/anneal",
		Services: []string{"deepwiki"},
	})
	if resp.Repo != "georgebuilds/anneal" {
		t.Errorf("repo = %q, want georgebuilds/anneal", resp.Repo)
	}
	if len(resp.Services) != 1 {
		t.Errorf("filter should restrict to 1 service; got %d", len(resp.Services))
	}
	if resp.Services[0].Service != "deepwiki" {
		t.Errorf("expected deepwiki; got %s", resp.Services[0].Service)
	}
}

func TestCodeSearch_ServicesFilterDropsUnknown(t *testing.T) {
	endpoints, stop := fakeIndexer(t, map[string]http.HandlerFunc{
		"deepwiki": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("deepwiki ok"))
		},
	})
	defer stop()
	tool := NewCodeSearchTool()
	tool.Endpoints = endpoints
	resp := runCodeSearch(t, tool, codeSearchInput{Services: []string{"deepwiki", "google", "stackoverflow"}})
	if len(resp.Services) != 1 {
		t.Errorf("unknown services should be dropped; got %d entries", len(resp.Services))
	}
}

func TestCodeSearch_404PropagatesAsError(t *testing.T) {
	endpoints, stop := fakeIndexer(t, map[string]http.HandlerFunc{
		"codewiki": func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		},
		"context7": func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		},
		"deepwiki": func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		},
	})
	defer stop()
	tool := NewCodeSearchTool()
	tool.Endpoints = endpoints
	resp := runCodeSearch(t, tool, codeSearchInput{})
	for _, h := range resp.Services {
		if h.Error == "" {
			t.Errorf("404 should surface in Error; %s did not", h.Service)
		}
		if h.Status != http.StatusNotFound {
			t.Errorf("status = %d, want 404", h.Status)
		}
	}
	if resp.Note == "" {
		t.Error("all-failed responses should carry a note hint")
	}
}

func TestCodeSearch_PartialFailureKeepsGoodResults(t *testing.T) {
	endpoints, stop := fakeIndexer(t, map[string]http.HandlerFunc{
		"codewiki": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", 500)
		},
		"context7": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("context7 works"))
		},
		"deepwiki": func(w http.ResponseWriter, r *http.Request) {
			http.NotFound(w, r)
		},
	})
	defer stop()
	tool := NewCodeSearchTool()
	tool.Endpoints = endpoints
	resp := runCodeSearch(t, tool, codeSearchInput{})
	if resp.Note != "" {
		t.Errorf("at least one hit should suppress the note; got %q", resp.Note)
	}
	var ctxHit *codeSearchServiceHit
	for i := range resp.Services {
		if resp.Services[i].Service == "context7" {
			ctxHit = &resp.Services[i]
		}
	}
	if ctxHit == nil || ctxHit.Excerpt == "" {
		t.Fatalf("context7 hit missing or empty: %+v", ctxHit)
	}
}

func TestCodeSearch_TimeoutDoesntStallOtherServices(t *testing.T) {
	endpoints, stop := fakeIndexer(t, map[string]http.HandlerFunc{
		"codewiki": func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(200 * time.Millisecond) // longer than tool timeout below
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("slow"))
		},
		"context7": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("fast"))
		},
		"deepwiki": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("fast2"))
		},
	})
	defer stop()
	tool := NewCodeSearchTool()
	tool.Endpoints = endpoints
	tool.Timeout = 50 * time.Millisecond
	start := time.Now()
	resp := runCodeSearch(t, tool, codeSearchInput{})
	elapsed := time.Since(start)
	if elapsed > 500*time.Millisecond {
		t.Errorf("fan-out should not serialize; elapsed = %v", elapsed)
	}
	var slowHit *codeSearchServiceHit
	for i := range resp.Services {
		if resp.Services[i].Service == "codewiki" {
			slowHit = &resp.Services[i]
		}
	}
	if slowHit == nil || slowHit.Error == "" {
		t.Errorf("slow service should surface a timeout error; got %+v", slowHit)
	}
}

func TestCodeSearch_QueryRoundTrips(t *testing.T) {
	endpoints, stop := fakeIndexer(t, map[string]http.HandlerFunc{
		"deepwiki": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("ok"))
		},
	})
	defer stop()
	tool := NewCodeSearchTool()
	tool.Endpoints = endpoints
	resp := runCodeSearch(t, tool, codeSearchInput{Query: "how does cross-frame approval work", Services: []string{"deepwiki"}})
	if resp.Query != "how does cross-frame approval work" {
		t.Errorf("query did not round-trip; got %q", resp.Query)
	}
}

func TestSplitOwnerRepo_AcceptsVariants(t *testing.T) {
	cases := map[string][2]string{
		"georgebuilds/carlos":                    {"georgebuilds", "carlos"},
		"georgebuilds/carlos/":                   {"georgebuilds", "carlos"},
		"github.com/georgebuilds/carlos":         {"georgebuilds", "carlos"},
		"https://github.com/georgebuilds/carlos": {"georgebuilds", "carlos"},
	}
	for in, want := range cases {
		o, r, ok := splitOwnerRepo(in)
		if !ok {
			t.Errorf("splitOwnerRepo(%q) returned ok=false", in)
			continue
		}
		if o != want[0] || r != want[1] {
			t.Errorf("splitOwnerRepo(%q) = (%q,%q), want (%q,%q)", in, o, r, want[0], want[1])
		}
	}
}

func TestSplitOwnerRepo_RejectsBadInput(t *testing.T) {
	for _, in := range []string{"", "/", "carlos", "georgebuilds/", "/carlos"} {
		if _, _, ok := splitOwnerRepo(in); ok {
			t.Errorf("splitOwnerRepo(%q) should reject; returned ok=true", in)
		}
	}
}

// TestSplitOwnerRepo_RejectsTrailingSegments is the regression for the
// silent-drop bug at code_search.go:270-281. SplitN(s, "/", 3) was
// returning ("owner","repo",true) for inputs like
// "owner/repo/issues/12" — the trailing /issues/12 vanished and the
// indexer fan-out built nonsense URLs from the truncated input. The
// fix uses an unbounded Split + a strict 2-segment check, so any
// deep-link path is rejected as a malformed owner/repo coordinate.
func TestSplitOwnerRepo_RejectsTrailingSegments(t *testing.T) {
	cases := []string{
		"owner/repo/issues/12",
		"owner/repo/pulls/3",
		"owner/repo/blob/main/README.md",
		"https://github.com/owner/repo/issues",
		"github.com/owner/repo/issues",
	}
	for _, in := range cases {
		if _, _, ok := splitOwnerRepo(in); ok {
			t.Errorf("splitOwnerRepo(%q) should reject trailing segments; returned ok=true", in)
		}
	}
	// Trailing slash on a clean owner/repo still accepted.
	o, r, ok := splitOwnerRepo("owner/repo/")
	if !ok || o != "owner" || r != "repo" {
		t.Errorf("splitOwnerRepo(\"owner/repo/\") = (%q,%q,%v), want (owner, repo, true)", o, r, ok)
	}
}

func TestExcerpt_RespectsCap(t *testing.T) {
	long := strings.Repeat("a", 1000)
	got := excerpt(long, 100)
	if !strings.Contains(got, "truncated") {
		t.Error("excerpt should mark truncation")
	}
	if len(got) > 200 {
		t.Errorf("excerpt exceeded cap + marker length; got %d", len(got))
	}
}

func TestExcerpt_ShortPassesThrough(t *testing.T) {
	if got := excerpt("short", 100); got != "short" {
		t.Errorf("short input should pass through; got %q", got)
	}
}

func TestFillCodeSearchTemplate_SubstitutesPlaceholders(t *testing.T) {
	tmpl := "https://x.com/{owner}/{repo}/{repo}.json"
	got := fillCodeSearchTemplate(tmpl, "alice", "tools")
	if got != "https://x.com/alice/tools/tools.json" {
		t.Errorf("substitution wrong; got %q", got)
	}
}
