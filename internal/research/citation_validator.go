package research

// 11i - CitationValidator: deterministic validation of [pN] passage
// citations in a synthesis body.
//
// # Why this exists
//
// The synthesize phase emits a markdown report whose inline [pN]
// citations are supposed to point at passages in Report.Passages.
// Nothing checked that the cited IDs actually exist: the model can
// write [p9] when only p1..p5 were extracted, and the hallucinated
// reference shipped silently. This validator walks the synthesis,
// pulls out every [pN] reference, and flags any whose pN is not a real
// passage ID.
//
// # Discipline
//
// Pure / deterministic / I/O-free. Same (synthesis, passages) always
// yields the same result. No provider call - the hallucinated-ID check
// is fully decidable from the two inputs.
//
// # What it does NOT do (out of scope for 11i)
//
//   - Misattribution: whether the cited passage actually supports the
//     claim it is attached to. That needs an LLM judge and is left for
//     a later slice.
//   - Citation coverage / unsupported-claim scoring: that is the
//     agent.CitationAuditor's job (Report.Citations); this validator is
//     additive and orthogonal.
//
// # Choices
//
//   - Code fences: [pN] tokens inside triple-backtick fenced blocks and
//     single-backtick inline spans are NOT treated as citations. A code
//     example that happens to contain "[p9]" should not be flagged as a
//     hallucinated source. We strip fenced + inline code before matching
//     (mirrors agent.stripCode's intent).
//   - Duplicates: the same [pN] cited many times counts once toward the
//     distinct sets (Valid / Unknown) but every occurrence is summed in
//     TotalCitations, so callers can see both "how many references" and
//     "how many distinct IDs".

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// passageCiteRE matches an inline passage citation: an opening bracket,
// a literal lowercase "p", one or more digits, and a closing bracket -
// e.g. "[p1]", "[p42]". It deliberately does NOT match:
//
//   - numeric-only refs like "[1]" / "[12]" (those are the agent
//     CitationAuditor's [N] refs, a different namespace)
//   - "[pX]" / "[p]" (no digit run; malformed)
//   - URLs or markdown links (no leading "[p<digits>]" shape)
//
// The capture group is the digits, so "[p7]" yields "7"; we rebuild the
// canonical ID as "p"+digits for set membership.
var passageCiteRE = regexp.MustCompile(`\[p(\d+)\]`)

// fenceStripRE and inlineStripRE remove code so [pN] inside code does
// not register as a citation. Kept local to this file - the agent
// package's equivalents are unexported.
var (
	fenceStripRE  = regexp.MustCompile("(?s)```[^\n]*\n.*?```")
	inlineStripRE = regexp.MustCompile("`[^`]+`")
)

// CitationValidation is the structured result of validating the [pN]
// citations in a synthesis against the known passage set. It is stored
// on Report.CitationValidation during the verify phase.
//
// The zero value is meaningful: it describes a synthesis with no
// passage citations at all (everything empty, Valid == true).
type CitationValidation struct {
	// TotalCitations is the number of [pN] occurrences found (with
	// duplicates counted), excluding any inside code spans/fences.
	TotalCitations int

	// Cited is the distinct set of cited IDs, sorted by passage number
	// ascending (so "p2" precedes "p10").
	Cited []string

	// Valid is the distinct cited IDs that ARE the ID of a real passage,
	// sorted ascending by passage number.
	Valid []string

	// Unknown is the distinct cited IDs that are NOT the ID of any known
	// passage - hallucinated references. Sorted ascending by passage
	// number. Empty means every citation resolves.
	Unknown []string
}

// Valid reports whether the synthesis cited only real passages (no
// hallucinated IDs). True when Unknown is empty, including the
// no-citations case.
func (v CitationValidation) OK() bool { return len(v.Unknown) == 0 }

// validatePassageCitations is the pure core check. It scans synthesis
// for [pN] references (ignoring code spans/fences), resolves each
// against the IDs in passages, and returns the structured result.
//
// Deterministic and I/O-free. No provider call.
func validatePassageCitations(synthesis string, passages []Passage) CitationValidation {
	known := make(map[string]bool, len(passages))
	for _, p := range passages {
		if p.ID != "" {
			known[p.ID] = true
		}
	}

	scanText := stripCodeForCitations(synthesis)

	var result CitationValidation
	citedSet := map[string]bool{}
	for _, m := range passageCiteRE.FindAllStringSubmatch(scanText, -1) {
		// m[0] = "[p7]", m[1] = "7".
		id := "p" + m[1]
		result.TotalCitations++
		if citedSet[id] {
			continue
		}
		citedSet[id] = true
	}

	result.Cited = sortPassageIDs(citedSet)
	validSet := map[string]bool{}
	unknownSet := map[string]bool{}
	for id := range citedSet {
		if known[id] {
			validSet[id] = true
		} else {
			unknownSet[id] = true
		}
	}
	result.Valid = sortPassageIDs(validSet)
	result.Unknown = sortPassageIDs(unknownSet)
	return result
}

// stripCodeForCitations removes fenced + inline code so [pN] tokens
// inside code examples are not counted as citations.
func stripCodeForCitations(text string) string {
	text = fenceStripRE.ReplaceAllString(text, "")
	text = inlineStripRE.ReplaceAllString(text, "")
	return text
}

// sortPassageIDs returns the keys of set as a slice ordered by the
// numeric suffix ascending, so "p2" sorts before "p10". IDs always have
// the form "p"+digits here (they came from passageCiteRE / passage IDs
// we matched), but we fall back to string order if a parse ever fails.
func sortPassageIDs(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool {
		ni, oki := passageNum(out[i])
		nj, okj := passageNum(out[j])
		if oki && okj {
			return ni < nj
		}
		return out[i] < out[j]
	})
	return out
}

// passageNum parses the numeric suffix of a "pN" ID. Returns (n, true)
// on success, (0, false) otherwise.
func passageNum(id string) (int, bool) {
	if !strings.HasPrefix(id, "p") {
		return 0, false
	}
	var n int
	if _, err := fmt.Sscanf(id[1:], "%d", &n); err != nil {
		return 0, false
	}
	return n, true
}
