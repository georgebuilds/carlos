package research

import (
	"context"
	"fmt"
	"strings"
)

// synthesizeSystem is the system prompt for the writing phase. Three
// constraints, in order of importance:
//
//  1. "Cite by ID using [pN] notation" - the citation auditor in the
//     verify phase looks for [pN] patterns; without this the
//     coverage score is meaningless.
//  2. "Use ONLY information from the supplied passages" - bounds
//     hallucination; the verifier can spot violations later but the
//     prompt-side rule discourages them up front.
//  3. "If the passages don't cover an aspect, say so explicitly" -
//     ensures gaps in the research surface in the report rather than
//     being smoothed over.
const synthesizeSystem = `You write structured research reports from supplied passages. Every factual claim must be cited inline as [pN] where N matches a passage ID from the supplied list. Use ONLY information from the supplied passages - do not introduce facts the passages don't support. If the passages don't cover an aspect of the question, say so explicitly rather than guessing.

Each passage line carries a trust marker in the form (trust=<tier>, single-source) where tier is trusted, medium, or low and "single-source" appears only when no other source corroborates that passage. When a claim rests on a passage tagged low or medium trust, or tagged single-source, explicitly caveat it in the prose (for example: "according to a single low-trust source" or "this is not independently corroborated"). Do not present a weak or single-sourced claim with the same confidence as one backed by multiple trusted sources. Output clean markdown.`

// synthesizeUserTemplate is the user-message scaffold. %s = the
// question; %s = the passage manifest (one per line in the format the
// citation auditor expects).
const synthesizeUserTemplate = `Write a structured report answering: %s

Cite passages by ID using [p1], [p2] notation. Use ONLY information from the supplied passages. If the passages don't cover an aspect, say so explicitly rather than guessing.

Passages:
%s`

// runSynthesize asks the LLM to compose the report. The passage list
// is rendered as "[pN] from <URL>: <text>" lines so the model sees
// the same shape its citations will reference.
func (e *Engine) runSynthesize(ctx context.Context, report *Report) (err error) {
	t0 := e.beginPhase("synthesize")
	defer func() { e.endPhase("synthesize", t0, err) }()

	if len(report.Passages) == 0 {
		return fmt.Errorf("no passages to synthesize from")
	}
	flagWeakSources(report)
	user := fmt.Sprintf(synthesizeUserTemplate, report.Question,
		formatPassageManifest(report.Passages, report.Sources))
	body, err := e.callProvider(ctx, report, synthesizeSystem, user)
	if err != nil {
		return err
	}
	body = strings.TrimSpace(body)
	if body == "" {
		report.Concerns = append(report.Concerns,
			"synthesize: model returned empty body")
		return fmt.Errorf("empty synthesis body")
	}
	report.Synthesis = body
	return nil
}

// formatPassageManifest renders passages as the model will see them.
// The format is "[pN] from <URL> (trust=<tier>[, single-source]): <text>"
// - the "[pN] from <URL>: …" prefix matches the citation auditor's
// heuristic so a model that copies it gets the citation right by
// reflex, and the parenthesized trust marker (item 11g) feeds the
// skepticism instruction in synthesizeSystem.
func formatPassageManifest(passages []Passage, sources []Source) string {
	srcByID := map[string]Source{}
	for _, s := range sources {
		srcByID[s.ID] = s
	}
	single := singleSourcedIDs(passages, sources)
	var b strings.Builder
	for _, p := range passages {
		src, ok := srcByID[p.SourceID]
		url := src.URL
		if !ok || url == "" {
			url = "(source URL unavailable)"
		}
		marker := "trust=" + src.Reputation.Tier.String()
		if single[p.SourceID] {
			marker += ", single-source"
		}
		fmt.Fprintf(&b, "[%s] from %s (%s): %s\n", p.ID, url, marker, p.Text)
	}
	return b.String()
}

// singleSourcedIDs returns the set of Source IDs whose passages are NOT
// corroborated by any other source. Cross-reference heuristic (item
// 11g): two sources corroborate each other when they are DISTINCT
// sources (different URLs) both contributing passages to the report.
// A source is therefore "single-sourced" when it is the only distinct
// source URL backing the cited passage pool. This is a deliberately
// simple, defensible proxy: it does not attempt sub-topic clustering
// (we have no reliable sub-topic label per passage at synthesis time),
// so it flags the unambiguous case - a report resting on one source -
// rather than guessing at finer-grained corroboration.
func singleSourcedIDs(passages []Passage, sources []Source) map[string]bool {
	urlByID := map[string]string{}
	for _, s := range sources {
		urlByID[s.ID] = s.URL
	}
	// Count distinct source URLs that actually contributed a passage.
	urlsCited := map[string]bool{}
	for _, p := range passages {
		if u := urlByID[p.SourceID]; u != "" {
			urlsCited[u] = true
		}
	}
	single := map[string]bool{}
	// Only when the entire pool rests on a single distinct URL is any
	// passage single-sourced. With >= 2 distinct URLs there is at least
	// some cross-source breadth, so we don't over-flag.
	if len(urlsCited) <= 1 {
		for _, p := range passages {
			single[p.SourceID] = true
		}
	}
	return single
}

// flagWeakSources records a Concern for each cited source that is both
// low-or-medium trust AND single-sourced - the weakest evidentiary
// footing (item 11g). It also records a summary concern when the whole
// report rests on a single distinct source. Concerns are deduped by
// source ID so a source with several passages is reported once.
func flagWeakSources(report *Report) {
	srcByID := map[string]Source{}
	for _, s := range report.Sources {
		srcByID[s.ID] = s
	}
	single := singleSourcedIDs(report.Passages, report.Sources)

	distinctURLs := map[string]bool{}
	for _, s := range report.Sources {
		if s.URL != "" {
			distinctURLs[s.URL] = true
		}
	}
	if len(distinctURLs) == 1 {
		report.Concerns = append(report.Concerns,
			"synthesize: entire report rests on a single source; claims are not independently corroborated")
	}

	seen := map[string]bool{}
	for _, p := range report.Passages {
		if seen[p.SourceID] || !single[p.SourceID] {
			continue
		}
		src := srcByID[p.SourceID]
		if src.Reputation.Tier == TierTrusted {
			continue // single-sourced but high-authority: not flagged weak
		}
		seen[p.SourceID] = true
		report.Concerns = append(report.Concerns,
			fmt.Sprintf("synthesize: weak source %s (trust=%s, single-source) %s; caveat any claim that rests on it",
				p.SourceID, src.Reputation.Tier.String(), src.URL))
	}
}
