package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// BenchmarkSteadyAppend is the carlos-realistic write workload: 1 writer
// goroutine, append a token_usage event per tick at a target rate.
// Reports nanoseconds/op via the standard testing benchmark loop.
//
// Run with: go test -bench BenchmarkSteadyAppend -benchtime=2s ./internal/agent/
func BenchmarkSteadyAppend(b *testing.B) {
	dir := b.TempDir()
	log, err := agent.OpenSQLiteEventLog(filepath.Join(dir, "state.db"))
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	defer log.Close()

	// seed agent row
	ctx := context.Background()
	created, _ := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: "a", RootID: "a", Title: "bench", Model: "x",
	})
	_, _ = log.Append(ctx, agent.Event{AgentID: "a", TS: time.Now().UTC(), Type: agent.EvtStateChange, Payload: created})

	payload, _ := json.Marshal(agent.TokenUsage{DeltaOut: 1})
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := log.Append(ctx, agent.Event{AgentID: "a", TS: time.Now().UTC(), Type: agent.EvtTokenUsage, Payload: payload})
		if err != nil {
			b.Fatalf("append: %v", err)
		}
	}
}

// TestEventLogLoadProfile is NOT a benchmark — it's a measured, end-to-end
// load test that produces the numbers required by the preflight notes:
//
//   - p50 / p99 append latency under sustained 100 ev/s + a 1000 ev/s burst
//   - reader stall: max time a reader's roster query takes during the burst
//   - all measured with N=4 reader goroutines doing the projection SELECT
//
// Output goes to t.Log so it's easy to copy into the notes file.
func TestEventLogLoadProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("load profile takes ~3s; skipped under -short")
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	log, err := agent.OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer log.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Seed one agent.
	created, _ := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: "loadtest", RootID: "loadtest", Title: "load", Model: "x",
	})
	_, _ = log.Append(ctx, agent.Event{AgentID: "loadtest", TS: time.Now().UTC(), Type: agent.EvtStateChange, Payload: created})

	// --- Workload definition --------------------------------------------------
	const (
		steadyRatePerSec = 100               // sustained
		steadyDuration   = 2 * time.Second   // 200 events
		burstSize        = 1000              // simulates 1000 token coalescing flushes back-to-back
		burstCadence     = 1 * time.Millisecond // ~1000/s
		numReaders       = 4
	)

	// --- Latency capture ------------------------------------------------------
	var (
		appendLatencies []time.Duration
		appendMu        sync.Mutex
		readerMaxStall  int64 // atomic; nanoseconds
		readerOps       int64 // atomic
	)
	recordAppend := func(d time.Duration) {
		appendMu.Lock()
		appendLatencies = append(appendLatencies, d)
		appendMu.Unlock()
	}

	// --- Reader goroutines ----------------------------------------------------
	var readersWG sync.WaitGroup
	for r := 0; r < numReaders; r++ {
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				t0 := time.Now()
				row := log.DB().QueryRowContext(ctx, `SELECT id, state, tokens_out FROM agents WHERE id = ?`, "loadtest")
				var id, state string
				var tOut int64
				// it's fine if the row doesn't exist yet at the very start
				_ = row.Scan(&id, &state, &tOut)
				d := time.Since(t0)
				for {
					cur := atomic.LoadInt64(&readerMaxStall)
					if int64(d) <= cur || atomic.CompareAndSwapInt64(&readerMaxStall, cur, int64(d)) {
						break
					}
				}
				atomic.AddInt64(&readerOps, 1)
				time.Sleep(2 * time.Millisecond) // realistic roster refresh cadence
			}
		}()
	}

	// --- Phase 1: sustained 100 ev/s for 2s ----------------------------------
	tick := time.NewTicker(time.Second / steadyRatePerSec)
	steadyEnd := time.Now().Add(steadyDuration)
	steadyCount := 0
	payload, _ := json.Marshal(agent.TokenUsage{DeltaOut: 1})
	for time.Now().Before(steadyEnd) {
		<-tick.C
		t0 := time.Now()
		_, err := log.Append(ctx, agent.Event{AgentID: "loadtest", TS: time.Now().UTC(), Type: agent.EvtTokenUsage, Payload: payload})
		if err != nil {
			t.Fatalf("steady append: %v", err)
		}
		recordAppend(time.Since(t0))
		steadyCount++
	}
	tick.Stop()
	steadyP50, steadyP99 := percentiles(appendLatencies)
	steadyAppendCount := len(appendLatencies)

	// --- Phase 2: 1000-event burst at ~1ms cadence (simulating coalesced flushes) ---
	// Reset the latency capture so burst numbers don't drown in steady noise.
	appendMu.Lock()
	appendLatencies = appendLatencies[:0]
	appendMu.Unlock()
	atomic.StoreInt64(&readerMaxStall, 0)

	burstStart := time.Now()
	for i := 0; i < burstSize; i++ {
		t0 := time.Now()
		_, err := log.Append(ctx, agent.Event{AgentID: "loadtest", TS: time.Now().UTC(), Type: agent.EvtTokenUsage, Payload: payload})
		if err != nil {
			t.Fatalf("burst append: %v", err)
		}
		recordAppend(time.Since(t0))
		// Back off briefly to keep the loop bounded; with no sleep the
		// rate is higher than 1000/s on this hardware, which is fine —
		// we're after worst-case latency under load.
		if burstCadence > 0 {
			time.Sleep(burstCadence)
		}
	}
	burstDuration := time.Since(burstStart)
	burstP50, burstP99 := percentiles(appendLatencies)
	burstReaderStall := time.Duration(atomic.LoadInt64(&readerMaxStall))
	burstReaderOps := atomic.LoadInt64(&readerOps)

	// stop readers and let them drain
	cancel()
	readersWG.Wait()

	// --- Report ---------------------------------------------------------------
	t.Logf("=== SQLite WAL load profile ===")
	t.Logf("steady phase: %d events over %s (target %d/s)", steadyAppendCount, steadyDuration, steadyRatePerSec)
	t.Logf("  append latency p50=%s p99=%s", steadyP50, steadyP99)
	t.Logf("burst phase:  %d events in %s (effective %.0f ev/s)", burstSize, burstDuration, float64(burstSize)/burstDuration.Seconds())
	t.Logf("  append latency p50=%s p99=%s", burstP50, burstP99)
	t.Logf("  reader max stall during burst = %s (across %d reader queries)", burstReaderStall, burstReaderOps)

	// Also export to a file so the human can grep the numbers without
	// re-running.
	report := fmt.Sprintf(
		"SQLite WAL load profile (modernc.org/sqlite, single writer + %d readers)\n"+
			"steady:  %d events / %s, p50=%s p99=%s\n"+
			"burst:   %d events in %s (%.0f ev/s), p50=%s p99=%s\n"+
			"reader max stall during burst: %s across %d ops\n",
		numReaders,
		steadyAppendCount, steadyDuration, steadyP50, steadyP99,
		burstSize, burstDuration, float64(burstSize)/burstDuration.Seconds(), burstP50, burstP99,
		burstReaderStall, burstReaderOps,
	)
	t.Logf("\n%s", report)

	out := os.Getenv("CARLOS_LOADPROFILE_OUT")
	if out == "" {
		// Write into the test's source-tree-adjacent dir so the spike
		// runner can read it back without escaping the sandbox.
		out = filepath.Join("..", "..", "loadprofile_report.txt")
	}
	if err := os.WriteFile(out, []byte(report), 0o644); err != nil {
		t.Logf("write report to %s: %v", out, err)
	} else {
		t.Logf("wrote report to %s", out)
	}

	// Soft assertions: surface a STOP if numbers are catastrophically bad.
	// "Catastrophic" means worse than the SPEC tolerance (250-500ms TUI flush
	// cadence) by an order of magnitude.
	if burstP99 > 50*time.Millisecond {
		t.Errorf("burst p99 append latency %s exceeds 50ms (would block 250ms TUI flush)", burstP99)
	}
	if burstReaderStall > 100*time.Millisecond {
		t.Errorf("reader stall %s exceeds 100ms — TUI roster refresh would visibly hitch", burstReaderStall)
	}
}

// percentiles returns p50 and p99 of the input. Caller-owned slice; we copy
// before sorting so the original capture order is preserved if needed.
func percentiles(in []time.Duration) (p50, p99 time.Duration) {
	if len(in) == 0 {
		return 0, 0
	}
	xs := make([]time.Duration, len(in))
	copy(xs, in)
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	p50 = xs[len(xs)*50/100]
	p99Idx := len(xs) * 99 / 100
	if p99Idx >= len(xs) {
		p99Idx = len(xs) - 1
	}
	p99 = xs[p99Idx]
	return p50, p99
}
