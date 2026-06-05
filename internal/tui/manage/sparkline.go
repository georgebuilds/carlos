package manage

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/agent"
)

// sparkBlocks are the eight half-step elevation glyphs used by the
// burn-rate mini-bar. Index 0 is "near zero"; index 7 is the bucket's
// maximum. We avoid the empty " " for a zero bucket because the
// invariant "12 cells wide" keeps row width steady regardless of
// activity.
var sparkBlocks = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// numSparkBuckets is the number of cells rendered in the mini-bar.
// Twelve is the brief's spec: 60 ring buckets, 5 buckets per rendered
// cell.
const numSparkBuckets = 12

// ringSize is the underlying ring buffer width used by the focus
// pane's tokens-per-bucket tracker (60 buckets total). Each rendered
// cell averages numRingPerCell consecutive ring slots.
const (
	ringSize       = 60
	numRingPerCell = ringSize / numSparkBuckets
)

// TokenRing is a 60-slot circular buffer of tokens-out deltas. Slot 0
// is the oldest; the buffer advances forward as new deltas arrive.
// Indexed mod ringSize on writes; full Read() returns the snapshot in
// chronological order so the sparkline renders left=oldest.
//
// The buffer lives next to the focus pane's subscription pump (one
// per focused agent). We promote it to a top-level type so the test
// can drive it directly without spinning up a Model.
type TokenRing struct {
	buf [ringSize]int64
	// nextIdx is the slot we'll write to next; the slot at nextIdx-1
	// (mod ringSize) is the most-recent. Always in [0, ringSize).
	nextIdx int
}

// Add records a tokens-out delta in the most-recent slot. Per DESIGN
// "coalesce streaming token deltas in memory" — we accumulate within
// the current slot rather than always advancing, because real flushes
// land at 250–500ms cadence and we want the sparkline's resolution to
// match clock-time, not flush-event-count. The Advance method moves
// us to the next slot (typically called by a 1s tick).
func (r *TokenRing) Add(delta int64) {
	if delta < 0 {
		return
	}
	// Most-recent slot is nextIdx-1 (mod ringSize). We initialize
	// nextIdx=1 on the first Add so the first delta lands in slot 0.
	if r.nextIdx == 0 {
		r.nextIdx = 1
	}
	cur := (r.nextIdx - 1 + ringSize) % ringSize
	r.buf[cur] += delta
}

// Advance moves the write cursor forward by one slot (zeroing the new
// slot). Typically called on a 1s tick so the 60-bucket ring spans
// one minute of activity.
func (r *TokenRing) Advance() {
	r.nextIdx = (r.nextIdx + 1) % ringSize
	r.buf[r.nextIdx%ringSize] = 0
}

// Snapshot returns the ring in chronological order (left=oldest).
func (r *TokenRing) Snapshot() []int64 {
	out := make([]int64, ringSize)
	// nextIdx is the slot we'll write to next, so it's the oldest
	// slot (it's about to be overwritten). We copy starting there.
	for i := 0; i < ringSize; i++ {
		out[i] = r.buf[(r.nextIdx+i)%ringSize]
	}
	return out
}

// RenderSparkline returns a 12-character string that visually encodes
// the ring's most-recent minute of activity. The mapping is:
//
//   - Bucket each rendered cell from numRingPerCell ring slots.
//   - Compute the cell's average tokens-out (rounded down).
//   - Find the row max; scale to the eight sparkBlocks levels.
//
// Color follows the row's state so a runaway-cost row's spark blares
// in amber, while a paused agent's spark stays cyan. We pass the row
// state in instead of a color so callers don't need to know the
// palette.
func RenderSparkline(ring *TokenRing, state agent.State) string {
	if ring == nil {
		return strings.Repeat(string(sparkBlocks[0]), numSparkBuckets)
	}
	snap := ring.Snapshot()
	cells := make([]int64, numSparkBuckets)
	for i := 0; i < numSparkBuckets; i++ {
		var sum int64
		for j := 0; j < numRingPerCell; j++ {
			sum += snap[i*numRingPerCell+j]
		}
		cells[i] = sum / int64(numRingPerCell)
	}

	// Scale by the row's own peak so a low-throughput agent still
	// shows visible variation. A row that has literally zero tokens
	// across all buckets renders as the baseline ▁ everywhere.
	var peak int64
	for _, v := range cells {
		if v > peak {
			peak = v
		}
	}

	var b strings.Builder
	for _, v := range cells {
		var idx int
		if peak > 0 {
			idx = int((int64(len(sparkBlocks)-1) * v) / peak)
			if idx < 0 {
				idx = 0
			}
			if idx >= len(sparkBlocks) {
				idx = len(sparkBlocks) - 1
			}
		}
		b.WriteRune(sparkBlocks[idx])
	}
	return lipgloss.NewStyle().Foreground(stateColor(state)).Render(b.String())
}

// formatTokens renders an int64 token count compactly: 0–999 as-is,
// 1.0k–9.9k with one decimal, 10k+ with no decimal, 1M+ as "1.2M".
func formatTokens(n int64) string {
	switch {
	case n < 1_000:
		return intStr(n)
	case n < 10_000:
		return decStr1k(n)
	case n < 1_000_000:
		return intStr(n/1_000) + "k"
	default:
		return decStr1M(n)
	}
}

// formatTokensColumn renders the <in>/<out> pair as a single string
// constrained to a small budget — 11 chars covers up to "1.2M/1.2M".
func formatTokensColumn(in, out int64) string {
	return formatTokens(in) + "/" + formatTokens(out)
}

// formatCost renders cost_cents as $N.NN. Negative or zero costs
// render as $0.00 — we never present a negative number to the user.
func formatCost(cents int64) string {
	if cents < 0 {
		cents = 0
	}
	dollars := cents / 100
	rem := cents % 100
	return "$" + intStr(dollars) + "." + zeropad2(int(rem))
}

func intStr(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func zeropad2(n int) string {
	if n < 0 {
		n = 0
	}
	if n < 10 {
		return "0" + intStr(int64(n))
	}
	return intStr(int64(n))
}

// decStr1k renders 1000..9999 as "1.2k" (truncated, not rounded — the
// roster value is a running counter, so we'd rather under-report than
// claim a tick we haven't seen).
func decStr1k(n int64) string {
	whole := n / 1_000
	frac := (n % 1_000) / 100
	return intStr(whole) + "." + intStr(frac) + "k"
}

// decStr1M renders 1_000_000..n as "1.2M" with one decimal (same
// truncation policy).
func decStr1M(n int64) string {
	whole := n / 1_000_000
	frac := (n % 1_000_000) / 100_000
	return intStr(whole) + "." + intStr(frac) + "M"
}
