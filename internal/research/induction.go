// induction.go - slice 11h. Pure helpers that decide whether a finished
// research run is worth offering to the skill inducer, and that build
// the transcript the inducer summarizes.
//
// These two functions are intentionally dependency-free (they touch
// only *Report) so the quality gate and the transcript shape can be
// unit-tested without a provider, a log, or the skillwire adapter. The
// adapter (internal/skills/skillwire) composes them: gate first, then
// summarize, then Induce + queue.
package research

import (
	"fmt"
	"sort"
	"strings"
)

// Quality-gate thresholds for CanInduceFromResearch. Exposed as named
// constants so tests reference them and a change here is a visible
// red/green rather than a silent drift.
//
// Rationale:
//   - minInductionSources: a single-source answer is, almost by
//     definition, a lookup - the inducer prompt itself lists
//     "single-source lookup" as the canonical NON-skill. Requiring two
//     distinct sources keeps one-off fact retrieval out of the queue.
//   - minSynthesisLen: a synthesis shorter than this is a one-liner, not
//     a procedure worth generalizing. 200 runes is roughly two to three
//     sentences - below that there is no reusable arc to extract.
//   - maxFatalConcerns: any concern whose text reads as a hard failure
//     (budget exhausted, phase aborted/failed, citation collapse) means
//     the run did not actually produce a trustworthy report; we do not
//     induce from a degraded run regardless of length.
const (
	minInductionSources = 2
	minSynthesisLen     = 200
	maxFatalConcerns    = 0
)

// fatalConcernMarkers are substrings that mark a concern as
// run-degrading rather than merely advisory. Matched case-insensitively
// against each Report.Concerns entry. The engine writes concerns like
// "phase=search failed: ...", "phase=fetch aborted: ...", and "provider
// -call budget exhausted at N calls"; those are the failures that must
// veto induction even when the partial synthesis is long enough.
var fatalConcernMarkers = []string{
	"aborted",
	"failed",
	"budget exhausted",
	"budget exceeded",
}

// CanInduceFromResearch is the quality gate for slice 11h. It returns
// true only when the report is substantive enough that a reusable
// research procedure could plausibly be extracted from it. The inducer
// still makes the final reusable/not-reusable call; this gate is the
// cheap, deterministic pre-filter that keeps thin or degraded runs from
// ever reaching a provider call.
//
// The checks, in order (all must pass):
//
//  1. report is non-nil.
//  2. The synthesis, trimmed, is at least minSynthesisLen runes.
//  3. At least minInductionSources DISTINCT sources were fetched
//     (distinct by URL; the engine already dedupes, but we count
//     defensively).
//  4. No concern reads as a fatal/degrading failure
//     (see fatalConcernMarkers); the budget is for at most
//     maxFatalConcerns such entries.
func CanInduceFromResearch(report *Report) bool {
	if report == nil {
		return false
	}
	if len([]rune(strings.TrimSpace(report.Synthesis))) < minSynthesisLen {
		return false
	}
	if distinctSourceCount(report.Sources) < minInductionSources {
		return false
	}
	if countFatalConcerns(report.Concerns) > maxFatalConcerns {
		return false
	}
	return true
}

// distinctSourceCount counts sources with a unique, non-empty URL.
func distinctSourceCount(sources []Source) int {
	seen := make(map[string]struct{}, len(sources))
	for _, s := range sources {
		u := strings.TrimSpace(s.URL)
		if u == "" {
			continue
		}
		seen[u] = struct{}{}
	}
	return len(seen)
}

// countFatalConcerns returns how many concerns match a fatal marker.
func countFatalConcerns(concerns []string) int {
	n := 0
	for _, c := range concerns {
		lc := strings.ToLower(c)
		for _, m := range fatalConcernMarkers {
			if strings.Contains(lc, m) {
				n++
				break
			}
		}
	}
	return n
}

// SummarizeResearchSession builds the free-form transcript fed to the
// skill inducer. The inducer reads a summary, not the raw report, so
// this compresses the run into the parts that signal a reusable
// procedure: the question, the sub-query decomposition (the "axes" the
// agent chose), a compact synthesis digest, and the source tiers the
// run leaned on (so a learned skill can say "prefer sources Z").
//
// The output is plain text with no em-dashes (house style). It is
// deterministic for a given report so the summary diffs cleanly across
// runs.
func SummarizeResearchSession(report *Report) string {
	if report == nil {
		return ""
	}
	var b strings.Builder

	b.WriteString("Research session summary\n\n")

	q := strings.TrimSpace(report.Question)
	if q == "" {
		q = strings.TrimSpace(report.Query.Question)
	}
	fmt.Fprintf(&b, "Question: %s\n\n", q)

	if len(report.Query.Sub) > 0 {
		b.WriteString("Decomposed along these sub-queries:\n")
		for _, s := range report.Query.Sub {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			fmt.Fprintf(&b, "  - %s\n", s)
		}
		b.WriteString("\n")
	}

	tiers := sourceTierBreakdown(report.Sources)
	if len(tiers) > 0 {
		b.WriteString("Source tiers used:\n")
		for _, t := range tiers {
			fmt.Fprintf(&b, "  - %s: %d\n", t.tier, t.count)
		}
		b.WriteString("\n")
	}

	digest := synthesisDigest(report.Synthesis, 800)
	if digest != "" {
		b.WriteString("Synthesis digest:\n")
		b.WriteString(digest)
		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), "\n") + "\n"
}

// tierCount pairs a tier label with how many sources fell into it.
type tierCount struct {
	tier  string
	count int
}

// sourceTierBreakdown groups sources by their reputation tier so the
// inducer can learn "prefer trusted sources" from a run that leaned on
// them. It reuses the canonical reputation classifier (the same one the
// search-ranking and synthesis-skepticism passes use) rather than a
// parallel host heuristic, so the vocabulary stays trusted/medium/low
// across the whole pipeline. Tiers are returned sorted by label so the
// summary is stable.
func sourceTierBreakdown(sources []Source) []tierCount {
	counts := make(map[string]int)
	for _, s := range sources {
		counts[classify(s.URL, s.Title).Tier.String()]++
	}
	out := make([]tierCount, 0, len(counts))
	for tier, n := range counts {
		out = append(out, tierCount{tier: tier, count: n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].tier < out[j].tier })
	return out
}

// synthesisDigest returns at most maxRunes runes of the synthesis,
// trimmed, with a trailing ellipsis marker when truncated. Runes (not
// bytes) so a multibyte synthesis isn't cut mid-character.
func synthesisDigest(synthesis string, maxRunes int) string {
	s := strings.TrimSpace(synthesis)
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return strings.TrimSpace(string(r[:maxRunes])) + " [...]"
}
