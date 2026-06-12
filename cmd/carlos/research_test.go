package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/providers/fake"
	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/tools"
)

// TestSlugifyQuestion verifies the slug rules: lowercase, [a-z0-9]+
// runs joined by '-', punctuation collapsed, length capped, empty
// fallback.
func TestSlugifyQuestion(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"basic", "How widely is WebGPU supported?", "how-widely-is-webgpu-supported"},
		{"punctuation-collapsed", "What!!!  is...  Go?", "what-is-go"},
		{"trims-edge-dashes", "   --  cool stuff  --   ", "cool-stuff"},
		{"empty-fallback", "", "research"},
		{"all-punctuation-fallback", "!?.,()", "research"},
		{"length-capped", strings.Repeat("a", 200), strings.Repeat("a", 60)},
		{"length-cap-trims-trailing-dash",
			strings.Repeat("ab ", 30), // 'ab ab ab …' — every 3 chars adds 'ab-', so 60 chars lands mid-dash
			"ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab-ab",
		},
		{"digits-survive", "Go 1.26.3 release notes", "go-1-26-3-release-notes"},
		{"unicode-stripped", "café résumé", "caf-r-sum"},
		{"lowercases", "HELLO WORLD", "hello-world"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := slugifyQuestion(tc.in)
			if got != tc.want {
				t.Errorf("slugifyQuestion(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if len(got) > 60 {
				t.Errorf("slug too long: %d chars", len(got))
			}
		})
	}
}

// TestSaveResearchReport verifies the file lands at
// ~/.carlos/research/<slug>-<unix-ts>.md with 0600 perms inside a 0700
// directory. We override $HOME so the test never touches the real home
// dir.
func TestSaveResearchReport(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	ts := time.Date(2026, 6, 5, 10, 30, 0, 0, time.UTC)
	body := "# my report\n\nbody text\n"
	path, err := saveResearchReport("What is Go?", body, "", ts)
	if err != nil {
		t.Fatalf("saveResearchReport: %v", err)
	}

	// Path shape.
	wantDir := filepath.Join(tmp, ".carlos", "research")
	if !strings.HasPrefix(path, wantDir+string(filepath.Separator)) {
		t.Errorf("path = %q, want under %q", path, wantDir)
	}
	wantName := fmt.Sprintf("what-is-go-%d.md", ts.Unix())
	if filepath.Base(path) != wantName {
		t.Errorf("file name = %q, want %q", filepath.Base(path), wantName)
	}

	// File perms.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("file perms = %v, want 0600", info.Mode().Perm())
	}

	// Dir perms.
	dirInfo, err := os.Stat(wantDir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("dir perms = %v, want 0700", dirInfo.Mode().Perm())
	}

	// Content round-trips.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != body {
		t.Errorf("content mismatch: got %q want %q", string(got), body)
	}
}

// TestSaveResearchReport_DirCreationFails ensures the helper surfaces
// a readable error rather than panicking when ~/.carlos can't be
// created (e.g. HOME points at a regular file).
func TestSaveResearchReport_DirCreationFails(t *testing.T) {
	// Make HOME a path that already exists as a regular file so the
	// MkdirAll for ~/.carlos fails.
	tmp := t.TempDir()
	fakeHome := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(fakeHome, []byte("blocker"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	t.Setenv("HOME", fakeHome)

	_, err := saveResearchReport("hi", "body", "", time.Now())
	if err == nil {
		t.Fatal("expected error when HOME isn't a dir, got nil")
	}
	if !strings.Contains(err.Error(), "mkdir") {
		t.Errorf("error text should mention mkdir: %v", err)
	}
}

func TestSaveResearchReport_FrameScopedPath(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	ts := time.Date(2026, 6, 6, 15, 0, 0, 0, time.UTC)
	path, err := saveResearchReport("alpha", "body", "work", ts)
	if err != nil {
		t.Fatalf("saveResearchReport: %v", err)
	}
	wantDir := filepath.Join(tmp, ".carlos", "frames", "work", "research")
	if !strings.HasPrefix(path, wantDir+string(filepath.Separator)) {
		t.Errorf("path = %q, want under %q", path, wantDir)
	}
}

// TestBuildResearchEngine_Wires verifies the helper pulls the right
// tools out of the registry and constructs a usable engine.
func TestBuildResearchEngine_Wires(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	prov := fake.New("fake", fake.Script{
		{Kind: providers.EventStopReason, Stop: "end_turn"},
	})
	e := buildResearchEngine(prov, "fake-model", reg)
	if e == nil {
		t.Fatal("buildResearchEngine returned nil with full default registry")
	}
	if e.Provider == nil {
		t.Error("Provider not set")
	}
	if e.Model != "fake-model" {
		t.Errorf("Model = %q, want fake-model", e.Model)
	}
	if e.Search == nil {
		t.Error("Search not set")
	}
	if e.Fetcher == nil {
		t.Error("Fetcher not set")
	}
	if e.MaxSubQueries != research.DefaultMaxSubQueries {
		t.Errorf("MaxSubQueries = %d, want %d", e.MaxSubQueries, research.DefaultMaxSubQueries)
	}
	if e.SourcesPerQuery != research.DefaultSourcesPerQuery {
		t.Errorf("SourcesPerQuery = %d, want %d", e.SourcesPerQuery, research.DefaultSourcesPerQuery)
	}
}

// TestBuildResearchEngine_NilWhenToolsMissing — the chat surface must
// degrade cleanly when the registry doesn't carry web tools.
func TestBuildResearchEngine_NilWhenToolsMissing(t *testing.T) {
	empty := tools.NewRegistry()
	prov := fake.New("fake", nil)
	if e := buildResearchEngine(prov, "m", empty); e != nil {
		t.Errorf("expected nil engine when registry has no web tools, got %+v", e)
	}
	// Half-registered: only web_search → still nil.
	half := tools.NewRegistry()
	half.Register(tools.NewWebSearchTool())
	if e := buildResearchEngine(prov, "m", half); e != nil {
		t.Errorf("expected nil engine when only web_search is registered, got %+v", e)
	}
}

// TestBuildResearchEngine_NilArgs is a defensive nil-check pass.
func TestBuildResearchEngine_NilArgs(t *testing.T) {
	reg := tools.NewDefaultRegistry()
	if e := buildResearchEngine(nil, "m", reg); e != nil {
		t.Error("expected nil engine when provider is nil")
	}
	if e := buildResearchEngine(fake.New("f", nil), "m", nil); e != nil {
		t.Error("expected nil engine when registry is nil")
	}
}
