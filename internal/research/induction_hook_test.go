package research_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/tools"
)

// recordingProposer implements research.SkillProposer, capturing every
// invocation. It can be configured to panic or block to prove the
// caller treats it as best-effort (it never does either in these tests,
// but the field documents the contract under test).
type recordingProposer struct {
	mu     sync.Mutex
	calls  int
	lastID string
	report *research.Report
}

func (p *recordingProposer) ProposeFromResearch(_ context.Context, agentID string, report *research.Report) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.lastID = agentID
	p.report = report
}

func (p *recordingProposer) snapshot() (int, string, *research.Report) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls, p.lastID, p.report
}

// inductionEngine builds an engine whose run produces a GOOD report:
// two distinct sources and a synthesis long enough to clear the gate.
func inductionEngine(t *testing.T) *research.Engine {
	t.Helper()
	longSynth := strings.Repeat("WebGPU shipped broadly across browsers by 2026 [p1][p2]. ", 8)
	prov := newScriptedProvider("p1",
		"sub-a\nsub-b",                        // decompose: two sub-queries
		`{"text":"alpha body","relevance":8}`, // read s1
		`{"text":"beta body","relevance":7}`,  // read s2
		longSynth,                             // synthesis
	)
	fs := &fakeSearch{defaultResults: []tools.SearchResult{
		{Rank: 1, URL: "https://a.example.com"},
		{Rank: 2, URL: "https://b.example.com"},
	}}
	ff := &fakeFetcher{bodies: map[string]string{
		"https://a.example.com": "alpha",
		"https://b.example.com": "beta",
	}}
	return &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, SourcesPerQuery: 2,
	}
}

// Hook fires on a successful, gate-passing run, and is handed the
// session's agent ID + the finalized report.
func TestSkillProposer_FiresOnGoodRun(t *testing.T) {
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))
	eng := inductionEngine(t)
	prop := &recordingProposer{}
	eng.SkillProposer = prop
	log := &recordingLog{}

	agentID, doneCh, err := research.SpawnResearch(context.Background(), log, eng, "how broadly is WebGPU supported?")
	if err != nil {
		t.Fatalf("SpawnResearch: %v", err)
	}
	res := drainResearch(t, doneCh, 10*time.Second)
	if res.Err != nil {
		t.Fatalf("run err: %v", res.Err)
	}

	calls, gotID, gotReport := prop.snapshot()
	if calls != 1 {
		t.Fatalf("proposer calls = %d want 1", calls)
	}
	if gotID != agentID {
		t.Errorf("proposer agentID = %q want %q", gotID, agentID)
	}
	if gotReport == nil || gotReport.Synthesis == "" {
		t.Error("proposer should receive the finalized report")
	}
}

// Hook is skipped when the report does NOT clear the quality gate (thin
// synthesis / too few sources), even though the run succeeds.
func TestSkillProposer_SkippedWhenGateFails(t *testing.T) {
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))
	eng := happyEngine(t) // 1 source, short synthesis: below the gate
	prop := &recordingProposer{}
	eng.SkillProposer = prop
	log := &recordingLog{}

	_, doneCh, err := research.SpawnResearch(context.Background(), log, eng, "thin question?")
	if err != nil {
		t.Fatalf("SpawnResearch: %v", err)
	}
	res := drainResearch(t, doneCh, 10*time.Second)
	if res.Err != nil {
		t.Fatalf("run err: %v", res.Err)
	}
	if calls, _, _ := prop.snapshot(); calls != 0 {
		t.Fatalf("proposer should be skipped below the gate, got %d calls", calls)
	}
}

// Hook is skipped when the run FAILS, even if a proposer is wired.
func TestSkillProposer_SkippedOnFailure(t *testing.T) {
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))
	// An engine whose decompose phase yields no provider response -> the
	// run aborts before producing a synthesis. We force failure by
	// giving the search backend an error.
	prov := newScriptedProvider("p1", "sub-a")
	fs := &fakeSearch{err: context.DeadlineExceeded}
	ff := &fakeFetcher{}
	eng := &research.Engine{Provider: prov, Model: "m", Search: fs, Fetcher: ff, SourcesPerQuery: 2}
	prop := &recordingProposer{}
	eng.SkillProposer = prop
	log := &recordingLog{}

	_, doneCh, err := research.SpawnResearch(context.Background(), log, eng, "doomed question?")
	if err != nil {
		t.Fatalf("SpawnResearch: %v", err)
	}
	res := drainResearch(t, doneCh, 10*time.Second)
	if res.Err == nil {
		t.Fatal("expected the run to fail")
	}
	if calls, _, _ := prop.snapshot(); calls != 0 {
		t.Fatalf("proposer must not fire on a failed run, got %d calls", calls)
	}
}

// A nil SkillProposer is a no-op: the run completes exactly as before.
func TestSkillProposer_NilIsNoOp(t *testing.T) {
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))
	eng := inductionEngine(t) // good run, but no proposer wired
	log := &recordingLog{}

	_, doneCh, err := research.SpawnResearch(context.Background(), log, eng, "no proposer wired?")
	if err != nil {
		t.Fatalf("SpawnResearch: %v", err)
	}
	res := drainResearch(t, doneCh, 10*time.Second)
	if res.Err != nil {
		t.Fatalf("nil proposer should not affect the run: %v", res.Err)
	}
	if res.Report == nil || res.Report.Synthesis == "" {
		t.Fatal("run should still complete normally with a nil proposer")
	}
}
