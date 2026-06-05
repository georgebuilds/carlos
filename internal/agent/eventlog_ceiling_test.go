package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// TestEventLogWriteCeiling reports the unconstrained single-writer ceiling
// (no inter-op sleep). This is the "how bad does it get if the agent loop
// stops coalescing" worst case, and we want to confirm it is still
// comfortably above 1000 ev/s.
func TestEventLogWriteCeiling(t *testing.T) {
	if testing.Short() {
		t.Skip("ceiling test takes ~2s under load")
	}
	dir := t.TempDir()
	log, err := agent.OpenSQLiteEventLog(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer log.Close()

	ctx := context.Background()
	created, _ := agent.NewStateChangeCreated(agent.AgentCreated{
		ID: "c", RootID: "c", Title: "ceiling", Model: "x",
	})
	_, _ = log.Append(ctx, agent.Event{AgentID: "c", TS: time.Now().UTC(), Type: agent.EvtStateChange, Payload: created})

	const N = 5000
	payload, _ := json.Marshal(agent.TokenUsage{DeltaOut: 1})

	t0 := time.Now()
	for i := 0; i < N; i++ {
		if _, err := log.Append(ctx, agent.Event{AgentID: "c", TS: time.Now().UTC(), Type: agent.EvtTokenUsage, Payload: payload}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	d := time.Since(t0)
	rate := float64(N) / d.Seconds()
	avg := d / N

	report := fmt.Sprintf("single-writer unconstrained ceiling: %d events in %s, %.0f ev/s, avg %s\n", N, d, rate, avg)
	t.Log(report)
	out := os.Getenv("CARLOS_CEILING_OUT")
	if out == "" {
		out = filepath.Join("..", "..", "ceiling_report.txt")
	}
	_ = os.WriteFile(out, []byte(report), 0o644)

	if rate < 1000 {
		t.Errorf("write ceiling %.0f ev/s is below the 1000 ev/s burst target", rate)
	}
}
