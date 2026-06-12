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
const synthesizeSystem = `You write structured research reports from supplied passages. Every factual claim must be cited inline as [pN] where N matches a passage ID from the supplied list. Use ONLY information from the supplied passages - do not introduce facts the passages don't support. If the passages don't cover an aspect of the question, say so explicitly rather than guessing. Output clean markdown.`

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
// The format is "[pN] from <URL>: <text>" - same shape as the
// citation auditor's heuristic so a model that copies it gets the
// citation right by reflex.
func formatPassageManifest(passages []Passage, sources []Source) string {
	urlByID := map[string]string{}
	for _, s := range sources {
		urlByID[s.ID] = s.URL
	}
	var b strings.Builder
	for _, p := range passages {
		url := urlByID[p.SourceID]
		if url == "" {
			url = "(source URL unavailable)"
		}
		fmt.Fprintf(&b, "[%s] from %s: %s\n", p.ID, url, p.Text)
	}
	return b.String()
}
