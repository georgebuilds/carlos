package tools

import (
	"context"
	"errors"
	"testing"
)

// TestRunSerialBatch_PerQueryErrorIsolated — a failing query records its
// error in that block without aborting the rest.
func TestRunSerialBatch_PerQueryErrorIsolated(t *testing.T) {
	fn := func(_ context.Context, q string) ([]SearchResult, error) {
		if q == "bad" {
			return nil, errors.New("kaboom")
		}
		return []SearchResult{mkResult(1, "https://x/"+q, q)}, nil
	}
	blocks := runSerialBatch(context.Background(), []string{"a", "bad", "c"}, fn)
	if len(blocks) != 3 {
		t.Fatalf("want 3 blocks, got %d", len(blocks))
	}
	if blocks[0].Error != "" || blocks[2].Error != "" {
		t.Errorf("good queries should have no error: %+v", blocks)
	}
	if blocks[1].Error == "" || len(blocks[1].Results) != 0 {
		t.Errorf("bad query block should carry an error and no results: %+v", blocks[1])
	}
}

// TestRunSerialBatch_CancelledCtx — a pre-cancelled ctx records a ctx
// error for every query without calling fn.
func TestRunSerialBatch_CancelledCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	fn := func(_ context.Context, q string) ([]SearchResult, error) {
		called = true
		return nil, nil
	}
	blocks := runSerialBatch(ctx, []string{"a", "b"}, fn)
	if called {
		t.Error("fn should not be called once ctx is cancelled")
	}
	for _, b := range blocks {
		if b.Error == "" {
			t.Errorf("cancelled block should carry an error: %+v", b)
		}
	}
}

// TestRunConcurrentBatch_OrderAndConcurrencyFloor — results return in
// input order, and a sub-1 concurrency is clamped to serial (still works).
func TestRunConcurrentBatch_OrderAndFloor(t *testing.T) {
	fn := func(_ context.Context, q string) ([]SearchResult, error) {
		return []SearchResult{mkResult(1, "https://x/"+q, q)}, nil
	}
	queries := []string{"q0", "q1", "q2"}
	blocks := runConcurrentBatch(context.Background(), queries, 0, fn) // 0 -> clamp to 1
	if len(blocks) != 3 {
		t.Fatalf("want 3 blocks, got %d", len(blocks))
	}
	for i, q := range queries {
		if blocks[i].Query != q {
			t.Errorf("block[%d].Query = %q, want %q (input order)", i, blocks[i].Query, q)
		}
	}
}

// TestRunConcurrentBatch_PerQueryError — one failing query records its
// error in the right slot.
func TestRunConcurrentBatch_PerQueryError(t *testing.T) {
	fn := func(_ context.Context, q string) ([]SearchResult, error) {
		if q == "x1" {
			return nil, errors.New("down")
		}
		return []SearchResult{mkResult(1, "https://x/"+q, q)}, nil
	}
	blocks := runConcurrentBatch(context.Background(), []string{"x0", "x1"}, 2, fn)
	if blocks[1].Error == "" {
		t.Errorf("x1 block should carry an error: %+v", blocks[1])
	}
	if blocks[0].Error != "" {
		t.Errorf("x0 block should be clean: %+v", blocks[0])
	}
}
