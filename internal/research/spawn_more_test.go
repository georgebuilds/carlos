package research_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/research"
)

// Cover the "prevStart != nil" + "prevDone != nil" branches of
// runResearchSession: if the engine already had phase callbacks set,
// the spawn helper must invoke them through.
func TestSpawnResearch_ExistingPhaseCallbacksAreComposed(t *testing.T) {
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))
	log := &recordingLog{}
	eng := happyEngine(t)

	var mu sync.Mutex
	var preStarts []string
	var preDones []string
	eng.OnPhaseStart = func(p string) {
		mu.Lock()
		preStarts = append(preStarts, p)
		mu.Unlock()
	}
	eng.OnPhaseDone = func(p string, _ time.Duration, _ error) {
		mu.Lock()
		preDones = append(preDones, p)
		mu.Unlock()
	}

	_, doneCh, err := research.SpawnResearch(context.Background(), log, eng, "q")
	if err != nil {
		t.Fatalf("SpawnResearch: %v", err)
	}
	res := drainResearch(t, doneCh, 5*time.Second)
	if res.Err != nil {
		t.Fatalf("err: %v", res.Err)
	}

	mu.Lock()
	gotStarts := append([]string(nil), preStarts...)
	gotDones := append([]string(nil), preDones...)
	mu.Unlock()

	if len(gotStarts) != 6 {
		t.Errorf("pre-existing OnPhaseStart fired %d times, want 6", len(gotStarts))
	}
	if len(gotDones) != 6 {
		t.Errorf("pre-existing OnPhaseDone fired %d times, want 6", len(gotDones))
	}
}

// artifactWriteFailLog fails InsertArtifact so SpawnResearch's artifact
// persistence step errors, exercising the "report ran but couldn't
// save" branch (line ~222 of spawn.go).
type artifactWriteFailLog struct {
	mu     sync.Mutex
	events []agent.Event
	rows   []agent.AgentRow
}

func (l *artifactWriteFailLog) Append(_ context.Context, ev agent.Event) (int64, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, ev)
	return int64(len(l.events)), nil
}

func (l *artifactWriteFailLog) InsertAgent(_ context.Context, r agent.AgentRow) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rows = append(l.rows, r)
	return nil
}

func (l *artifactWriteFailLog) InsertArtifact(_ context.Context, _ agent.Artifact) error {
	return errors.New("artifact insert refused")
}

func TestSpawnResearch_ArtifactWriteFailureSurfaces(t *testing.T) {
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))
	log := &artifactWriteFailLog{}
	eng := happyEngine(t)
	_, doneCh, err := research.SpawnResearch(context.Background(), log, eng, "q")
	if err != nil {
		t.Fatalf("SpawnResearch: %v", err)
	}
	res := drainResearch(t, doneCh, 5*time.Second)
	// Engine succeeded but artifact write failed; runErr should be non-nil
	// with the wrapped artifact-write error.
	if res.Err == nil {
		t.Error("expected non-nil Err on artifact write failure")
	}
}
