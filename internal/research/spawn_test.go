package research_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/research"
	"github.com/georgebuilds/carlos/internal/tools"
)

// --- Fake ResearchLog ------------------------------------------------------

// recordingLog implements research.ResearchLog by recording every
// event the spawn helper appends + every artifact insert. Threadsafe.
type recordingLog struct {
	mu        sync.Mutex
	events    []agent.Event
	rows      []agent.AgentRow
	artifacts []agent.Artifact

	// failAppend controls per-event-type failures; nil = never fail.
	failAppend map[agent.EventType]error
	// failInsertAgent triggers an InsertAgent error on the next call.
	failInsertAgent error
}

func (l *recordingLog) Append(_ context.Context, ev agent.Event) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err, ok := l.failAppend[ev.Type]; ok && err != nil {
		return 0, err
	}
	l.events = append(l.events, ev)
	return int64(len(l.events)), nil
}

func (l *recordingLog) InsertAgent(_ context.Context, r agent.AgentRow) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.failInsertAgent; err != nil {
		l.failInsertAgent = nil
		return err
	}
	l.rows = append(l.rows, r)
	return nil
}

func (l *recordingLog) InsertArtifact(_ context.Context, a agent.Artifact) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.artifacts = append(l.artifacts, a)
	return nil
}

func (l *recordingLog) snapshotEvents() []agent.Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]agent.Event, len(l.events))
	copy(out, l.events)
	return out
}

func (l *recordingLog) snapshotArtifacts() []agent.Artifact {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]agent.Artifact, len(l.artifacts))
	copy(out, l.artifacts)
	return out
}

// happyEngine assembles a research.Engine wired against scripted
// fakes that take a question through all six phases cleanly. The
// scripts are sized for one sub-query and one source so the test
// stays fast.
func happyEngine(t *testing.T) *research.Engine {
	t.Helper()
	prov := newScriptedProvider("p1",
		"sub1",
		`{"text":"x","relevance":7}`,
		"synth body cites [p1]",
	)
	fs := &fakeSearch{defaultResults: []tools.SearchResult{{Rank: 1, URL: "https://a.example.com"}}}
	ff := &fakeFetcher{bodies: map[string]string{"https://a.example.com": "alpha"}}
	return &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff, SourcesPerQuery: 1,
	}
}

// drainResearch waits for the done channel with a generous timeout
// so a stuck engine doesn't hang the test forever.
func drainResearch(t *testing.T, ch <-chan research.ResearchResult, timeout time.Duration) research.ResearchResult {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for ResearchResult")
		return research.ResearchResult{}
	}
}

// --- Happy path ------------------------------------------------------------

// Happy path: SpawnResearch returns immediately, the done channel
// fires with a populated Report + Artifact, and the event stream
// contains the expected created → running → 6×(phase-start +
// phase-done) → artifact_ref → done sequence.
func TestSpawnResearch_HappyPath(t *testing.T) {
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))

	log := &recordingLog{}
	eng := happyEngine(t)

	agentID, doneCh, err := research.SpawnResearch(context.Background(), log, eng, "what is WebGPU?")
	if err != nil {
		t.Fatalf("SpawnResearch: %v", err)
	}
	if !strings.HasPrefix(agentID, "r-") {
		t.Errorf("agentID = %q want r-* prefix", agentID)
	}

	res := drainResearch(t, doneCh, 10*time.Second)
	if res.Err != nil {
		t.Fatalf("result err: %v", res.Err)
	}
	if res.AgentID != agentID {
		t.Errorf("result AgentID = %q want %q", res.AgentID, agentID)
	}
	if res.Report == nil {
		t.Fatal("Report nil on happy path")
	}
	if res.Report.Synthesis == "" {
		t.Fatal("Synthesis empty")
	}
	if res.Artifact.SHA256 == "" {
		t.Fatalf("Artifact ref empty: %+v", res.Artifact)
	}
	if res.Artifact.Kind != agent.ArtifactKindResearch {
		t.Errorf("Artifact.Kind = %q want %q", res.Artifact.Kind, agent.ArtifactKindResearch)
	}

	// Walk the event stream and assert the expected sequence (start
	// + done per phase, plus the framing state_change + artifact_ref).
	evs := log.snapshotEvents()
	if len(evs) < 1+1+12+1+1 {
		t.Fatalf("event count = %d, want >= 16 (created + running + 12 phase events + artifact_ref + done)", len(evs))
	}

	// 1. Created.
	if evs[0].Type != agent.EvtStateChange {
		t.Errorf("ev[0] type = %q want state_change(created)", evs[0].Type)
	}
	var pl0 agent.StateChangePayload
	if err := json.Unmarshal(evs[0].Payload, &pl0); err != nil {
		t.Fatalf("ev[0] unmarshal: %v", err)
	}
	if pl0.Kind != agent.StateChangeCreated {
		t.Errorf("ev[0] kind = %q want created", pl0.Kind)
	}

	// 2. Transition → running.
	if evs[1].Type != agent.EvtStateChange {
		t.Errorf("ev[1] type = %q want state_change(running)", evs[1].Type)
	}

	// Last event should be the terminal transition (done).
	last := evs[len(evs)-1]
	if last.Type != agent.EvtStateChange {
		t.Errorf("last ev type = %q want state_change(done)", last.Type)
	}
	var plLast agent.StateChangePayload
	if err := json.Unmarshal(last.Payload, &plLast); err != nil {
		t.Fatalf("last unmarshal: %v", err)
	}
	if plLast.Kind != agent.StateChangeTransition || plLast.To == nil || *plLast.To != agent.StateDone {
		t.Errorf("last state_change = %+v want transition→done", plLast)
	}

	// Phase events: 6 starts (Done=false) + 6 dones (Done=true), in
	// matched start/done order, all err-free.
	var starts, dones []string
	for _, ev := range evs {
		if ev.Type != agent.EvtResearchPhase {
			continue
		}
		var pl agent.ResearchPhasePayload
		if err := json.Unmarshal(ev.Payload, &pl); err != nil {
			t.Fatalf("phase ev unmarshal: %v", err)
		}
		if pl.Done {
			if pl.Err != "" {
				t.Errorf("phase %s done with err = %q on happy path", pl.Phase, pl.Err)
			}
			dones = append(dones, pl.Phase)
		} else {
			starts = append(starts, pl.Phase)
		}
	}
	want := []string{"decompose", "search", "fetch", "read", "synthesize", "verify"}
	if !equalStringSlice(starts, want) {
		t.Errorf("phase starts = %v want %v", starts, want)
	}
	if !equalStringSlice(dones, want) {
		t.Errorf("phase dones = %v want %v", dones, want)
	}

	// Exactly one artifact_ref event, after the phase events, before
	// the terminal transition.
	var artifactRefIdx, doneIdx int = -1, -1
	for i, ev := range evs {
		switch ev.Type {
		case agent.EvtArtifactRef:
			artifactRefIdx = i
		case agent.EvtStateChange:
			var pl agent.StateChangePayload
			_ = json.Unmarshal(ev.Payload, &pl)
			if pl.Kind == agent.StateChangeTransition && pl.To != nil && *pl.To == agent.StateDone {
				doneIdx = i
			}
		}
	}
	if artifactRefIdx < 0 {
		t.Error("no EvtArtifactRef found")
	}
	if doneIdx < 0 {
		t.Error("no terminal done state_change found")
	}
	if artifactRefIdx > 0 && doneIdx > 0 && artifactRefIdx >= doneIdx {
		t.Errorf("artifact_ref (idx=%d) must precede terminal done (idx=%d)", artifactRefIdx, doneIdx)
	}

	// Projection-cache row was inserted.
	if len(log.rows) != 1 {
		t.Errorf("InsertAgent calls = %d want 1", len(log.rows))
	}
	if len(log.rows) == 1 && log.rows[0].ID != agentID {
		t.Errorf("InsertAgent row ID = %q want %q", log.rows[0].ID, agentID)
	}

	// Artifact row was inserted via the log's artifactWriter contract.
	arts := log.snapshotArtifacts()
	if len(arts) != 1 {
		t.Errorf("InsertArtifact calls = %d want 1", len(arts))
	}
	if len(arts) == 1 && arts[0].Kind != agent.ArtifactKindResearch {
		t.Errorf("artifact kind = %q want %q", arts[0].Kind, agent.ArtifactKindResearch)
	}
}

// --- Failure path ----------------------------------------------------------

// Failure path: engine's verify phase reports the judge failure (but
// verify itself succeeds because the audit always runs); to actually
// trigger a failed terminal state we drop the search backend to make
// the search phase return zero results. The terminal state ends up
// as failed; done channel carries a non-nil Err; the phase events
// still flow up to (and including) the failing phase.
func TestSpawnResearch_FailurePath_SearchEmpty(t *testing.T) {
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))

	prov := newScriptedProvider("p1", "sub1")
	fs := &fakeSearch{defaultResults: nil} // no results → search fails
	ff := &fakeFetcher{}
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff,
	}
	log := &recordingLog{}

	_, doneCh, err := research.SpawnResearch(context.Background(), log, eng, "q")
	if err != nil {
		t.Fatalf("SpawnResearch: %v", err)
	}
	res := drainResearch(t, doneCh, 5*time.Second)
	if res.Err == nil {
		t.Fatal("expected non-nil Err on failure path")
	}

	// Last state_change must be transition→failed.
	evs := log.snapshotEvents()
	var lastSC agent.StateChangePayload
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == agent.EvtStateChange {
			if err := json.Unmarshal(evs[i].Payload, &lastSC); err != nil {
				t.Fatalf("unmarshal last state_change: %v", err)
			}
			break
		}
	}
	if lastSC.Kind != agent.StateChangeTransition || lastSC.To == nil || *lastSC.To != agent.StateFailed {
		t.Errorf("terminal state_change = %+v want transition→failed", lastSC)
	}

	// No artifact_ref (synthesis never ran).
	for _, ev := range evs {
		if ev.Type == agent.EvtArtifactRef {
			t.Errorf("unexpected artifact_ref on failure path")
		}
	}
}

// --- Cancellation path -----------------------------------------------------

// Cancellation: a parent ctx cancelled mid-decompose unwinds the
// engine, the goroutine returns cleanly, and the terminal state is
// failed with a context.Canceled err.
func TestSpawnResearch_Cancellation(t *testing.T) {
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))

	// A blocking provider holds Stream open until ctx.Done(), so the
	// engine's decompose phase will block until we cancel.
	prov := &blockingProvider{name: "blocker"}
	fs := &fakeSearch{}
	ff := &fakeFetcher{}
	eng := &research.Engine{
		Provider: prov, Model: "m",
		Search: fs, Fetcher: ff,
	}
	log := &recordingLog{}

	ctx, cancel := context.WithCancel(context.Background())
	_, doneCh, err := research.SpawnResearch(ctx, log, eng, "q")
	if err != nil {
		t.Fatalf("SpawnResearch: %v", err)
	}
	// Give the goroutine a moment to enter decompose, then cancel.
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	res := drainResearch(t, doneCh, 5*time.Second)
	if res.Err == nil {
		t.Fatal("expected non-nil Err on cancellation")
	}
	if !errors.Is(res.Err, context.Canceled) {
		t.Errorf("Err = %v want context.Canceled", res.Err)
	}

	// Terminal state must be failed.
	evs := log.snapshotEvents()
	var lastSC agent.StateChangePayload
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == agent.EvtStateChange {
			_ = json.Unmarshal(evs[i].Payload, &lastSC)
			break
		}
	}
	if lastSC.Kind != agent.StateChangeTransition || lastSC.To == nil || *lastSC.To != agent.StateFailed {
		t.Errorf("terminal state_change = %+v want transition→failed", lastSC)
	}
}

// --- Artifact persistence --------------------------------------------------

// Targeted assertion that the artifact's bytes ARE the rendered
// markdown (so the chat-side / future viewer rendering doesn't
// silently lose information at the spawn boundary).
func TestSpawnResearch_ArtifactBytesMatchRenderedMarkdown(t *testing.T) {
	base := filepath.Join(t.TempDir(), "artifacts")
	t.Setenv("CARLOS_ARTIFACT_BASE", base)

	log := &recordingLog{}
	eng := happyEngine(t)
	_, doneCh, err := research.SpawnResearch(context.Background(), log, eng, "q")
	if err != nil {
		t.Fatalf("SpawnResearch: %v", err)
	}
	res := drainResearch(t, doneCh, 10*time.Second)
	if res.Err != nil {
		t.Fatalf("result err: %v", res.Err)
	}
	want := research.RenderMarkdown(res.Report)
	got, err := agent.ReadArtifact(base, res.Artifact.SHA256)
	if err != nil {
		t.Fatalf("ReadArtifact: %v", err)
	}
	if string(got) != want {
		t.Errorf("artifact bytes != rendered markdown\nwant:\n%s\n\ngot:\n%s", want, string(got))
	}
}

// --- Concurrent spawns -----------------------------------------------------

// Two concurrent spawns produce two distinct agent IDs and two
// independent event streams (no cross-talk in the recordingLog).
func TestSpawnResearch_ConcurrentSpawns_Independent(t *testing.T) {
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))

	log := &recordingLog{}
	eng1 := happyEngine(t)
	eng2 := happyEngine(t)

	id1, done1, err := research.SpawnResearch(context.Background(), log, eng1, "q1")
	if err != nil {
		t.Fatalf("spawn1: %v", err)
	}
	id2, done2, err := research.SpawnResearch(context.Background(), log, eng2, "q2")
	if err != nil {
		t.Fatalf("spawn2: %v", err)
	}
	if id1 == id2 {
		t.Errorf("agent IDs collided: %q", id1)
	}

	res1 := drainResearch(t, done1, 10*time.Second)
	res2 := drainResearch(t, done2, 10*time.Second)
	if res1.Err != nil || res2.Err != nil {
		t.Fatalf("results: %v / %v", res1.Err, res2.Err)
	}

	// Each agent's event stream is self-consistent: phase events all
	// carry the right AgentID, terminal state is done.
	evs := log.snapshotEvents()
	counts := map[string]int{id1: 0, id2: 0}
	for _, ev := range evs {
		if ev.Type == agent.EvtResearchPhase {
			if _, ok := counts[ev.AgentID]; !ok {
				t.Errorf("unexpected agent id on phase event: %q", ev.AgentID)
				continue
			}
			counts[ev.AgentID]++
		}
	}
	if counts[id1] != 12 || counts[id2] != 12 {
		t.Errorf("phase event counts = %v want both 12", counts)
	}
}

// --- Validation ------------------------------------------------------------

func TestSpawnResearch_RejectsNilLog(t *testing.T) {
	_, _, err := research.SpawnResearch(context.Background(), nil, happyEngine(t), "q")
	if err == nil || !strings.Contains(err.Error(), "nil log") {
		t.Errorf("err = %v want nil-log", err)
	}
}

func TestSpawnResearch_RejectsNilEngine(t *testing.T) {
	_, _, err := research.SpawnResearch(context.Background(), &recordingLog{}, nil, "q")
	if err == nil || !strings.Contains(err.Error(), "nil engine") {
		t.Errorf("err = %v want nil-engine", err)
	}
}

func TestSpawnResearch_RejectsEmptyQuestion(t *testing.T) {
	_, _, err := research.SpawnResearch(context.Background(), &recordingLog{}, happyEngine(t), "   ")
	if err == nil || !strings.Contains(err.Error(), "empty question") {
		t.Errorf("err = %v want empty question", err)
	}
}

// SpawnResearch surfaces an error if the created-event append fails
// (the projection-cache row would otherwise dangle).
func TestSpawnResearch_CreatedAppendFails(t *testing.T) {
	log := &recordingLog{
		failAppend: map[agent.EventType]error{
			agent.EvtStateChange: errors.New("disk full"),
		},
	}
	_, _, err := research.SpawnResearch(context.Background(), log, happyEngine(t), "q")
	if err == nil || !strings.Contains(err.Error(), "append created") {
		t.Errorf("err = %v want append-created error", err)
	}
}

// InsertAgent failure surfaces too (don't write the created event but
// fail to install the row — caller must see both halves succeed).
func TestSpawnResearch_InsertAgentFails(t *testing.T) {
	log := &recordingLog{
		failInsertAgent: errors.New("table locked"),
	}
	_, _, err := research.SpawnResearch(context.Background(), log, happyEngine(t), "q")
	if err == nil || !strings.Contains(err.Error(), "insert agent") {
		t.Errorf("err = %v want insert-agent error", err)
	}
}

// --- RenderMarkdown smoke --------------------------------------------------

// Quick guard that RenderMarkdown (the artifact-side renderer) emits
// the same prose as the chat-side equivalent. We assert structurally
// (section headers, sub-query bullets, source IDs) rather than byte-
// for-byte against an external renderer — those byte-for-byte
// assertions live in the chat package's own test suite.
func TestRenderMarkdown_StructuralSmoke(t *testing.T) {
	r := &research.Report{
		Question:  "q?",
		Query:     research.Query{Sub: []string{"sub a"}},
		Synthesis: "body cites [p1]",
		Sources:   []research.Source{{ID: "s1", URL: "https://a.example.com", Title: "A"}},
		Passages:  []research.Passage{{ID: "p1", SourceID: "s1", Text: "x", Relevance: 7}},
	}
	out := research.RenderMarkdown(r)
	for _, marker := range []string{
		"# Research report: q?",
		"## Sub-queries",
		"- sub a",
		"## Synthesis",
		"body cites [p1]",
		"## Sources",
		"**s1**",
		"## Passages",
		"**[p1]**",
		"## Budget",
	} {
		if !strings.Contains(out, marker) {
			t.Errorf("RenderMarkdown missing %q\n--- output ---\n%s", marker, out)
		}
	}
}

func TestRenderMarkdown_NilReport(t *testing.T) {
	if got := research.RenderMarkdown(nil); got != "" {
		t.Errorf("RenderMarkdown(nil) = %q want \"\"", got)
	}
}

// --- Compile-time interface check ------------------------------------------

// SQLiteEventLog must satisfy ResearchLog so the production wire-up
// in cmd/carlos can pass *agent.SQLiteEventLog directly without
// adapter glue. This is a compile-only assertion; if the surface ever
// diverges the build breaks here rather than at the call site.
var _ research.ResearchLog = (*agent.SQLiteEventLog)(nil)
