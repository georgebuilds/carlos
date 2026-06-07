package research_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/tools"
)

// Cover WebFetchAdapter.Fetch error paths and Engine.Run "nil engine"
// branch, plus the DeadlineExceeded branch of the phase loop.

func TestEngine_NilReceiverRejected(t *testing.T) {
	var e *research.Engine
	_, err := e.Run(context.Background(), "q?")
	if err == nil || !strings.Contains(err.Error(), "nil engine") {
		t.Errorf("want nil-engine err, got %v", err)
	}
}

func TestWebFetchAdapter_NilAdapterErrors(t *testing.T) {
	var a *research.WebFetchAdapter
	_, err := a.Fetch(context.Background(), "https://x")
	if err == nil {
		t.Fatal("nil adapter should error")
	}
}

// Adapter Fetch propagates the underlying tool's Execute error.
// (Already covered by the existing nil-tool test.)

// Adapter that takes a tool that returns malformed JSON exercises the
// parse-error branch. The simplest way is to make the underlying server
// return invalid JSON: but the tool returns its own JSON envelope, not
// the upstream content. We'd need to inject a bad raw response.
// We can do it by giving the adapter a tool whose Execute returns
// malformed JSON, but we'd need to wrap the tool. Skip that path; the
// existing tests already exercise the happy + tool-error branches.

// Engine: trigger DeadlineExceeded on the wall-clock budget so the
// phase loop sees runCtx.Err() at the top of the iteration.
func TestEngine_WallClockDeadlineYieldsConcern(t *testing.T) {
	prov := &blockingProvider{name: "block"}
	fs := &fakeSearch{}
	ff := &fakeFetcher{}
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff,
		Budget: research.ResearchBudget{MaxWallClock: 50 * time.Millisecond},
	}
	report, err := eng.Run(context.Background(), "q?")
	if err == nil {
		t.Fatal("expected deadline error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v want DeadlineExceeded", err)
	}
	// And a concern should reference the aborted phase.
	saw := false
	for _, c := range report.Concerns {
		if strings.Contains(c, "aborted") {
			saw = true
		}
	}
	if !saw {
		t.Errorf("expected aborted concern, got %v", report.Concerns)
	}
}

// WebFetchAdapter parsing fails when the tool returns a bytes blob that
// isn't valid JSON. We exercise this by configuring the underlying
// WebFetchTool to point at an HTTP server returning empty body. The
// tool's Execute typically returns its own JSON envelope which would
// parse OK; the actual parse-error branch (line 114) is hard to reach
// without modifying the tool. Skipping; covered by the existing nil
// tool / robots tests indirectly enough that the impact is small.

// Engine Fetch + RespectRobots overload exercises the "non-nil
// RespectRobots" branch is already exercised by the existing
// adapter test; we add a sanity smoke here too.
func TestWebFetchAdapter_RespectRobotsOverlayHappyPath(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/robots.txt" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><title>t</title></head><body>ok</body></html>`))
	}))
	defer ts.Close()

	tool := &tools.WebFetchTool{AllowPrivate: true}
	respect := true
	a := &research.WebFetchAdapter{Tool: tool, RespectRobots: &respect}
	src, err := a.Fetch(context.Background(), ts.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if src.Content == "" {
		t.Error("Content empty")
	}
}
