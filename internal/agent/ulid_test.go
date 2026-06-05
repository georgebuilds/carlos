package agent_test

import (
	"crypto/rand"
	"fmt"
	"math/big"
	mrand "math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"
)

// TestULIDSortsLexicographicallyByTime asserts the load-bearing property
// behind picking ULID over UUIDv4: a slice of ULIDs generated at strictly
// increasing timestamps, when sorted as strings, is the same order as when
// sorted by generation time.
//
// This is the property the `agents` table's PRIMARY KEY relies on so that
// `SELECT * FROM agents ORDER BY id` is implicitly a creation-time scan
// (no separate timestamp index needed for the common roster query).
func TestULIDSortsLexicographicallyByTime(t *testing.T) {
	const n = 1000
	entropy := ulid.Monotonic(rand.Reader, 0)

	ids := make([]string, n)
	base := time.Now()
	for i := 0; i < n; i++ {
		// 1ms apart guarantees the timestamp portion of the ULID differs.
		// Monotonic entropy disambiguates same-ms ULIDs anyway.
		ts := uint64(base.Add(time.Duration(i) * time.Millisecond).UnixMilli())
		u, err := ulid.New(ts, entropy)
		if err != nil {
			t.Fatalf("ulid.New: %v", err)
		}
		ids[i] = u.String()
	}

	// Sort a shuffled copy lexicographically and assert == ids (generation order).
	shuffled := make([]string, n)
	copy(shuffled, ids)
	mrand.Shuffle(n, func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	sort.Strings(shuffled)
	for i := range ids {
		if shuffled[i] != ids[i] {
			t.Fatalf("lex sort != gen order at idx %d: %s vs %s", i, shuffled[i], ids[i])
		}
	}
}

// TestULIDMonotonicWithinSameMillisecond is the OTHER property we depend on:
// when many IDs are minted in the same millisecond (perfectly possible at
// our event-append rate of 46k/s), monotonic entropy still produces strictly
// increasing IDs. Without this, "ORDER BY id" briefly inverts within a tick.
func TestULIDMonotonicWithinSameMillisecond(t *testing.T) {
	entropy := ulid.Monotonic(rand.Reader, 0)
	ts := uint64(time.Now().UnixMilli())
	const n = 10_000
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		u, err := ulid.New(ts, entropy)
		if err != nil {
			t.Fatalf("ulid.New: %v", err)
		}
		ids[i] = u.String()
	}
	for i := 1; i < n; i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("monotonic broken at i=%d: %s !> %s", i, ids[i], ids[i-1])
		}
	}
}

// TestULIDGenerationLatency confirms <1µs per ID even with crypto/rand
// entropy (the rate-limiting step in many ULID benchmarks). Writes the
// number to the spike's report file.
func TestULIDGenerationLatency(t *testing.T) {
	entropy := ulid.Monotonic(rand.Reader, 0)
	const n = 100_000
	t0 := time.Now()
	for i := 0; i < n; i++ {
		ts := uint64(time.Now().UnixMilli())
		_, err := ulid.New(ts, entropy)
		if err != nil {
			t.Fatalf("ulid.New: %v", err)
		}
	}
	d := time.Since(t0)
	per := d / n
	report := fmt.Sprintf("ULID generation: %d in %s, avg %s/op (crypto/rand + monotonic)\n", n, d, per)
	t.Log(report)
	out := filepath.Join("..", "..", "ulid_report.txt")
	_ = os.WriteFile(out, []byte(report), 0o644)

	if per > time.Microsecond {
		t.Errorf("ULID generation %s/op exceeds 1µs target", per)
	}
	_ = big.NewInt(0) // silence unused-import lint guard if it kicks in
}
