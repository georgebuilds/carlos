// Package fuzzy is a small, dependency-free fuzzy matcher shared by the
// Ctrl+P command palette (slash-command names + descriptions) and @file
// mention autocomplete (file paths).  It implements subsequence matching
// with fzy-style affine-gap scoring, tuned so that the rankings a 2026
// palette user expects fall out naturally:
//
//   - Case-insensitive by default with smartcase: an uppercase rune in
//     the pattern matches case-sensitively; lowercase runes match either
//     case.
//   - Bonus for matches at word boundaries: start of string, after '-',
//     '_', ' ', after '.', after '/' (path separators), and at lower-to-
//     upper camelCase transitions.
//   - Bonus for consecutive runs; penalties for gaps (inner gaps cost
//     more than leading/trailing ones).
//   - Strong bonus for prefix matches (the start-of-string boundary
//     outranks every other boundary).
//   - Filename-over-directory weighting for path-like candidates: a
//     match in the last '/'-segment beats the same match in an earlier
//     segment, and a match that covers more of the basename beats one
//     that covers less ("chat" ranks internal/tui/chat/chat.go above
//     cmd/carlos/chat_helpers.go).
//   - An exact match (pattern == candidate under the smartcase fold)
//     outranks everything else.
//
// The matcher is Unicode-correct: it operates on runes, not bytes, and
// all reported positions are RUNE indices into the candidate.  Callers
// that highlight matches must index []rune(candidate), not the raw
// string.
//
// Scoring is integer-only and fully deterministic: the same inputs
// always produce the same scores, positions, and order.  [Rank] is
// stable, so candidates with equal scores keep their input order.
package fuzzy

import (
	"sort"
	"unicode"
)

// Scoring constants.  Relative magnitudes are what matter: a consecutive
// run beats any single boundary except start-of-string, boundaries beat
// bare matches, and gap penalties are small enough that they only break
// ties between otherwise-similar matches.
const (
	scoreMatchConsecutive = 200 // each match directly after the previous one
	bonusBoundaryStart    = 250 // match at rune 0 (prefix matches win)
	bonusBoundarySlash    = 180 // match right after a '/'
	bonusBoundaryWord     = 160 // match right after '-', '_', or ' '
	bonusCamel            = 140 // lower-to-upper transition (fooBar)
	bonusBoundaryDot      = 120 // match right after '.'
	bonusBasenameChar     = 40  // each matched rune inside the last '/'-segment
	bonusBasenameFit      = 100 // scaled by matched-runes/basename-length
	bonusExact            = 10000

	penaltyGapLeading  = 1 // per rune skipped before the first match
	penaltyGapTrailing = 1 // per rune skipped after the last match
	penaltyGapInner    = 2 // per rune skipped between matches

	// Guard rails: pathological inputs fall back to "no match" rather
	// than allocating an enormous DP table.  Real palette queries and
	// repo paths sit far below these.
	maxPatternLen   = 256
	maxCandidateLen = 4096

	// negInf is the absorbing "unreachable" score.  Anything at or
	// below negInf/2 is treated as unreachable so that adding small
	// bonuses to an unreachable cell can never resurrect it.
	negInf = -1 << 40
)

// Result is one ranked candidate from [Rank].
type Result struct {
	Index     int    // position in the input candidates slice
	Candidate string // the candidate text, verbatim
	Score     int    // higher is better; comparable only within one Rank call
	Positions []int  // rune indices of matched pattern runes, ascending
}

// Match reports whether pattern is a fuzzy (subsequence) match for
// candidate.  On success it returns the score and the rune indices of
// the matched pattern runes (one per pattern rune, ascending), chosen to
// maximize the score so highlights land where a human would expect.
//
// An empty pattern matches everything with score 0 and nil positions.
// Patterns longer than the candidate (in runes), longer than 256 runes,
// or candidates longer than 4096 runes never match.
func Match(pattern, candidate string) (score int, positions []int, ok bool) {
	if pattern == "" {
		return 0, nil, true
	}
	p := []rune(pattern)
	c := []rune(candidate)
	if len(p) > len(c) || len(p) > maxPatternLen || len(c) > maxCandidateLen {
		return 0, nil, false
	}
	// Cheap greedy pre-check so non-matching candidates (the common
	// case when ranking a whole file tree) skip the O(m*n) scoring.
	if !isSubsequence(p, c) {
		return 0, nil, false
	}
	score, positions = scoreMatch(p, c)
	if len(p) == len(c) {
		// The subsequence check passed with no slack, so every rune
		// matched in place: this is an exact match under smartcase.
		score += bonusExact
	}
	return score, positions, true
}

// Rank fuzzy-matches pattern against every candidate and returns the
// matches sorted best-first.  The sort is stable: equal scores keep
// input order.  An empty pattern returns ALL candidates, in input
// order, with zero scores and nil positions (the palette's "nothing
// typed yet" state).  Non-matching candidates are omitted.
func Rank(pattern string, candidates []string) []Result {
	out := make([]Result, 0, len(candidates))
	if pattern == "" {
		for i, c := range candidates {
			out = append(out, Result{Index: i, Candidate: c})
		}
		return out
	}
	for i, c := range candidates {
		if s, pos, ok := Match(pattern, c); ok {
			out = append(out, Result{Index: i, Candidate: c, Score: s, Positions: pos})
		}
	}
	sort.SliceStable(out, func(a, b int) bool { return out[a].Score > out[b].Score })
	return out
}

// runesMatch reports whether pattern rune p matches candidate rune c
// under smartcase: an uppercase p must match exactly; anything else
// matches its own value or the lowercase fold of c.
func runesMatch(p, c rune) bool {
	if p == c {
		return true
	}
	if unicode.IsUpper(p) {
		return false
	}
	return unicode.ToLower(c) == p
}

// isSubsequence is the greedy feasibility check: can every pattern rune
// be matched, in order, somewhere in the candidate?
func isSubsequence(p, c []rune) bool {
	i := 0
	for _, r := range c {
		if i < len(p) && runesMatch(p[i], r) {
			i++
			if i == len(p) {
				return true
			}
		}
	}
	return false
}

// boundaryBonus returns the bonus earned by matching the rune at index j
// (whose predecessor is prev).  Index 0 is the strongest boundary so
// that prefix matches dominate.
func boundaryBonus(prev, cur rune, j int) int {
	if j == 0 {
		return bonusBoundaryStart
	}
	switch prev {
	case '/':
		return bonusBoundarySlash
	case '-', '_', ' ':
		return bonusBoundaryWord
	case '.':
		return bonusBoundaryDot
	}
	if unicode.IsLower(prev) && unicode.IsUpper(cur) {
		return bonusCamel
	}
	return 0
}

// scoreMatch runs the fzy-style dynamic program over the rune slices and
// backtracks the best path.  Callers must have verified feasibility via
// isSubsequence first; the final score is then guaranteed reachable.
//
// D[i][j] = best score for pattern[..i] with p[i] matched exactly at
// c[j]; M[i][j] = best score for pattern[..i] using only c[..j].  Both
// are stored in flat row-major slices.
func scoreMatch(p, c []rune) (int, []int) {
	m, n := len(p), len(c)

	// Per-position boundary bonuses, computed once per candidate.
	bonus := make([]int, n)
	var prev rune
	for j, r := range c {
		bonus[j] = boundaryBonus(prev, r, j)
		prev = r
	}

	// Basename start: rune index just past the last '/'.  When the
	// candidate has no '/', the whole string counts as the basename,
	// which keeps path and non-path candidates on the same scale.
	baseStart := 0
	for j := n - 1; j >= 0; j-- {
		if c[j] == '/' {
			baseStart = j + 1
			break
		}
	}

	D := make([]int, m*n)
	M := make([]int, m*n)
	for i := 0; i < m; i++ {
		gap := penaltyGapInner
		if i == m-1 {
			gap = penaltyGapTrailing
		}
		prevM := negInf // M[i][j-1]
		for j := 0; j < n; j++ {
			idx := i*n + j
			gain := 0
			if j >= baseStart {
				gain = bonusBasenameChar
			}
			d := negInf
			if runesMatch(p[i], c[j]) {
				switch {
				case i == 0:
					d = gain + bonus[j] - j*penaltyGapLeading
				case j > 0:
					// Either extend the best partial match across a
					// gap (paying the boundary bonus here), or extend
					// a consecutive run from the diagonal.
					best := negInf
					if up := M[idx-n-1]; up > negInf/2 {
						best = up + bonus[j]
					}
					if diag := D[idx-n-1]; diag > negInf/2 && diag+scoreMatchConsecutive > best {
						best = diag + scoreMatchConsecutive
					}
					if best > negInf/2 {
						d = best + gain
					}
				}
			}
			D[idx] = d
			mm := d
			if prevM > negInf/2 && prevM-gap > mm {
				mm = prevM - gap
			}
			M[idx] = mm
			prevM = mm
		}
	}

	// Backtrack the positions.  matchRequired is set when the cell we
	// just consumed was scored via the consecutive branch, which pins
	// the previous pattern rune to the diagonal neighbour.
	positions := make([]int, m)
	matchRequired := false
	j := n - 1
	for i := m - 1; i >= 0; i-- {
		for ; j >= 0; j-- {
			idx := i*n + j
			if D[idx] > negInf/2 && (matchRequired || D[idx] == M[idx]) {
				matchRequired = false
				if i > 0 && j > 0 {
					gain := 0
					if j >= baseStart {
						gain = bonusBasenameChar
					}
					matchRequired = D[idx] == D[idx-n-1]+scoreMatchConsecutive+gain
				}
				positions[i] = j
				j--
				break
			}
		}
	}

	score := M[m*n-1]

	// Filename fit: reward matches that cover a larger fraction of the
	// basename, so chat -> chat.go beats chat -> chat_helpers.go even
	// though both are boundary-anchored 4-rune runs.  Applied after the
	// DP as a whole-match heuristic; integer division keeps it
	// deterministic.
	if baseLen := n - baseStart; baseLen > 0 {
		inBase := 0
		for _, pos := range positions {
			if pos >= baseStart {
				inBase++
			}
		}
		score += bonusBasenameFit * inBase / baseLen
	}
	return score, positions
}
