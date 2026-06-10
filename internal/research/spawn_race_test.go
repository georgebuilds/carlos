package research_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/research"
)

// TestSpawnResearch_Concurrent_NoCallbackRace fires many SpawnResearch
// calls in parallel against engines that each carry a pre-existing
// OnPhaseStart / OnPhaseDone capture callback. Under -race this
// detects unsynchronized access to engine.OnPhaseStart / OnPhaseDone,
// and the assertions detect the more subtle order-dependent stale-
// restore bug where a later spawn's deferred restore clobbers an
// earlier spawn's wrapper.
//
// Invariants asserted:
//
//  1. Each engine's pre-existing callbacks fire 6 times each (start
//     and done) - never cross-contaminated by another engine's spawn.
//  2. Each agent's phase events in the shared log all match the
//     expected six-phase sequence (12 events: 6 starts + 6 dones).
//  3. After all spawns return, every engine's OnPhaseStart and
//     OnPhaseDone is still the original capture callback the test
//     installed before spawning - NOT a wrapper closure left over
//     from the spawn helper's mechanics.
func TestSpawnResearch_Concurrent_NoCallbackRace(t *testing.T) {
	t.Setenv("CARLOS_ARTIFACT_BASE", filepath.Join(t.TempDir(), "artifacts"))

	const N = 8
	log := &recordingLog{}

	// Per-engine capture state. Each engine gets its own engine
	// instance + its own callbacks that record into its own slice;
	// if the spawn helper races on the engine's callback fields or
	// clobbers a pre-existing callback, the capture slice will end
	// up with either too few entries (clobbered before fire) or
	// entries belonging to a different engine (cross-talk).
	type capture struct {
		mu     sync.Mutex
		starts []string
		dones  []string
	}
	engines := make([]*research.Engine, N)
	caps := make([]*capture, N)
	// Hold the user-installed callback values so we can verify they
	// are still installed unchanged after every spawn returns.
	type cbPair struct {
		start func(string)
		done  func(string, time.Duration, error)
	}
	originals := make([]cbPair, N)

	for i := 0; i < N; i++ {
		c := &capture{}
		caps[i] = c
		eng := happyEngine(t)
		startCB := func(phase string) {
			c.mu.Lock()
			c.starts = append(c.starts, phase)
			c.mu.Unlock()
		}
		doneCB := func(phase string, _ time.Duration, _ error) {
			c.mu.Lock()
			c.dones = append(c.dones, phase)
			c.mu.Unlock()
		}
		eng.OnPhaseStart = startCB
		eng.OnPhaseDone = doneCB
		engines[i] = eng
		originals[i] = cbPair{start: startCB, done: doneCB}
	}

	// Fire all spawns concurrently, then wait on every done channel.
	type spawned struct {
		id   string
		done <-chan research.ResearchResult
	}
	started := make(chan spawned, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		i := i
		go func() {
			defer wg.Done()
			id, doneCh, err := research.SpawnResearch(context.Background(), log, engines[i], "q")
			if err != nil {
				t.Errorf("SpawnResearch[%d]: %v", i, err)
				return
			}
			started <- spawned{id: id, done: doneCh}
		}()
	}
	wg.Wait()
	close(started)

	ids := make([]string, 0, N)
	for s := range started {
		res := drainResearch(t, s.done, 15*time.Second)
		if res.Err != nil {
			t.Errorf("spawn %s: %v", s.id, res.Err)
		}
		ids = append(ids, s.id)
	}
	if len(ids) != N {
		t.Fatalf("collected %d ids, want %d", len(ids), N)
	}

	want := []string{"decompose", "route", "search", "fetch", "read", "synthesize", "verify"}

	// 1. Each engine's pre-existing callback saw exactly the seven
	//    phases (one start + one done each) - no cross-engine
	//    leakage, no missing fires from clobbering.
	for i, c := range caps {
		c.mu.Lock()
		gotStarts := append([]string(nil), c.starts...)
		gotDones := append([]string(nil), c.dones...)
		c.mu.Unlock()

		if !sameSetSorted(gotStarts, want) {
			t.Errorf("engine[%d] starts = %v want %v (some phase fired more than once, or a phase was lost)",
				i, gotStarts, want)
		}
		if !sameSetSorted(gotDones, want) {
			t.Errorf("engine[%d] dones = %v want %v", i, gotDones, want)
		}
	}

	// 2. Per-agent phase events in the shared log: each spawned
	//    agentID must have exactly 12 EvtResearchPhase events
	//    (6 starts + 6 dones), and no phase event ever carries a
	//    foreign agentID.
	evs := log.snapshotEvents()
	phaseCounts := make(map[string]int)
	expectIDs := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		expectIDs[id] = struct{}{}
	}
	for _, ev := range evs {
		if ev.Type != agent.EvtResearchPhase {
			continue
		}
		if _, ok := expectIDs[ev.AgentID]; !ok {
			t.Errorf("phase event carries unknown agentID %q", ev.AgentID)
			continue
		}
		phaseCounts[ev.AgentID]++
	}
	for id := range expectIDs {
		// 7 phases × 2 (start+done) = 14 events
		if phaseCounts[id] != 14 {
			t.Errorf("agent %s: %d phase events, want 14", id, phaseCounts[id])
		}
	}

	// 3. Pre-existing callbacks survive the spawn (no stale-restore
	//    overwriting them with a foreign wrapper, and no leftover
	//    wrapper in place because we never touched the original).
	//
	//    We can't compare func values directly with == in Go, but we
	//    CAN fire the callback once and observe whether it routes
	//    into the original capture slice. If a spawn left a wrapper
	//    closure on the engine, calling OnPhaseStart would also
	//    re-emit a phase event into the shared log, which we then
	//    detect.
	preCheckEvts := len(log.snapshotEvents())
	for i, eng := range engines {
		if eng.OnPhaseStart == nil || eng.OnPhaseDone == nil {
			t.Errorf("engine[%d] callbacks nil after spawn", i)
			continue
		}
		eng.OnPhaseStart("probe-start")
		eng.OnPhaseDone("probe-done", 0, nil)
	}
	// The N synthetic invocations should each have added one
	// "probe-start" + one "probe-done" entry to that engine's
	// capture; assert exactly that and nothing else.
	for i, c := range caps {
		c.mu.Lock()
		var probeStarts, probeDones int
		for _, p := range c.starts {
			if p == "probe-start" {
				probeStarts++
			}
		}
		for _, p := range c.dones {
			if p == "probe-done" {
				probeDones++
			}
		}
		c.mu.Unlock()
		if probeStarts != 1 || probeDones != 1 {
			t.Errorf("engine[%d] probe: starts=%d dones=%d want 1/1 (callback was clobbered or composed with foreign wrapper)",
				i, probeStarts, probeDones)
		}
	}
	// And no new phase events leaked into the shared log - if the
	// spawn had left a wrapper closure on the engine, probing
	// OnPhaseStart would have called emitResearchPhase, growing the
	// event log.
	postCheckEvts := len(log.snapshotEvents())
	if postCheckEvts != preCheckEvts {
		t.Errorf("probing engines emitted %d new events to the shared log; the spawn helper left a wrapper closure on the caller's engine",
			postCheckEvts-preCheckEvts)
	}
}

// sameSetSorted reports whether a and b contain the same elements,
// regardless of order. The phase events are recorded in goroutine
// order (start/done interleave across phases via deferred funcs in
// the phase bodies), so the assertion is membership-based rather
// than positional.
func sameSetSorted(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := map[string]int{}
	for _, s := range a {
		counts[s]++
	}
	for _, s := range b {
		counts[s]--
	}
	for _, v := range counts {
		if v != 0 {
			return false
		}
	}
	return true
}
