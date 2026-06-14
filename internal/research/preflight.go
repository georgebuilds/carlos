package research

// preflight.go - the interactive pre-flight helpers for `/research`
// (roadmap slice 11k). Before the six-phase pipeline fans out, the TUI
// runs a Clarify -> Brief loop so the user can tighten a vague question
// into a focused brief. These helpers own the two model calls that loop
// needs; the TUI owns the state machine that drives them.
//
// Both calls are single-shot, line-oriented, and deterministic given a
// scripted provider. They never touch the engine's ResearchBudget - the
// pre-flight is a TUI affordance that runs before a Run is even
// constructed, so the budget accounting (which lives on a *Report) does
// not apply.

import (
	"context"
	"fmt"
	"strings"

	"github.com/georgebuilds/carlos/internal/providers"
)

// maxClarifyQuestions caps how many clarifying questions the pre-flight
// surfaces. Two is the ceiling the slice spec sets: enough to split a
// question on its two most ambiguous axes (e.g. "which version?" plus
// "which use case?") without turning the pre-flight into an
// interrogation. Extra lines from the model are truncated, not an error.
const maxClarifyQuestions = 2

// clarifySystem instructs the model to propose 0-2 short clarifying
// questions. The "ONE per line" contract mirrors decomposeSystem so the
// parser stays line-oriented; the explicit "return nothing" escape
// hatch is what lets an already-specific question skip the clarify step
// entirely. No em-dashes per house style.
const clarifySystem = `You help a researcher sharpen a question before a deep-research run. Propose at most two SHORT clarifying questions that would most narrow the scope (for example: which versions, which use case, which region, what depth). If the question is already specific enough to research well, return nothing at all. Return ONE clarifying question per line, no numbering, no bullets, no commentary.`

// clarifyUserTemplate is the user-message template; %s is the question.
const clarifyUserTemplate = `Question: %s

Return at most two short clarifying questions, ONE per line. If no clarification is needed, return an empty response.`

// briefSystem instructs the model to fold the question plus the user's
// answers into a single tight paragraph the user can review and edit. No
// em-dashes in the prompt or the requested output.
const briefSystem = `You write a one-paragraph research brief. Fold the original question together with the researcher's clarifying answers into a single focused paragraph that states exactly what to investigate and why it matters. Be concrete and specific. Write ONE paragraph of plain prose, no headings, no bullet points, no preamble, and no em-dashes.`

// Preflight runs the Clarify -> Brief model calls for the interactive
// `/research` pre-flight. It is a thin value over a provider + model id;
// construct one per pre-flight and discard it. All methods are pure
// given a deterministic provider.
type Preflight struct {
	Provider providers.Provider
	Model    string
}

// ClarifyQuestions asks the model for up to two short clarifying
// questions for the given research question. It returns an empty (non
// nil-required) slice when the question is already specific, when the
// model returns nothing parseable, or when the question is blank. A
// provider stream error is surfaced to the caller; a stream that emits
// an EventError mid-flight is treated the same way. Garbage output (no
// parseable lines) degrades to an empty slice, never an error, so the
// pre-flight can fall straight through to the brief.
func (p Preflight) ClarifyQuestions(ctx context.Context, question string) ([]string, error) {
	if p.Provider == nil {
		return nil, fmt.Errorf("research preflight: nil provider")
	}
	if strings.TrimSpace(question) == "" {
		return nil, fmt.Errorf("research preflight: empty question")
	}
	user := fmt.Sprintf(clarifyUserTemplate, strings.TrimSpace(question))
	body, err := p.stream(ctx, clarifySystem, user)
	if err != nil {
		return nil, err
	}
	return parseClarifyLines(body, maxClarifyQuestions), nil
}

// Brief folds the question plus the user's answers into a one-paragraph
// brief. Empty answers are skipped (a user may have pressed Enter past a
// question). The returned brief is whitespace-collapsed to a single
// paragraph; an empty model response degrades to the trimmed original
// question so the brief-edit step always has something to preload. A
// provider error is surfaced to the caller.
func (p Preflight) Brief(ctx context.Context, question string, answers []string) (string, error) {
	if p.Provider == nil {
		return "", fmt.Errorf("research preflight: nil provider")
	}
	if strings.TrimSpace(question) == "" {
		return "", fmt.Errorf("research preflight: empty question")
	}
	body, err := p.stream(ctx, briefSystem, briefUserMessage(question, answers))
	if err != nil {
		return "", err
	}
	brief := collapseParagraph(body)
	if brief == "" {
		// The model gave us nothing usable. Fall back to the question so
		// the brief-edit step is never blank; this keeps the flow moving
		// rather than failing-loud on a degenerate model turn.
		return strings.TrimSpace(question), nil
	}
	return brief, nil
}

// briefUserMessage assembles the user message for Brief: the original
// question followed by each non-empty answer, paired with its index so
// the model can correlate answers to its own clarifying questions even
// though the questions themselves are not echoed back here.
func briefUserMessage(question string, answers []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Original question: %s\n", strings.TrimSpace(question))
	kept := 0
	for _, a := range answers {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		kept++
		fmt.Fprintf(&b, "Clarifying answer %d: %s\n", kept, a)
	}
	if kept == 0 {
		b.WriteString("(no clarifying answers were provided)\n")
	}
	b.WriteString("\nWrite the one-paragraph research brief now.")
	return b.String()
}

// stream is the single-shot provider call shared by both methods. It
// drains the event channel, accumulating text deltas, and fails-loud on
// the first EventError. Mirrors engine.callProvider's drain loop but
// without the budget accounting, which has no *Report to charge against
// in the pre-flight.
func (p Preflight) stream(ctx context.Context, system, user string) (string, error) {
	req := providers.Request{
		Model:  p.Model,
		System: system,
		Messages: []providers.Message{{
			Role: "user",
			Content: []providers.Block{{
				Kind: "text",
				Text: user,
			}},
		}},
	}
	ch, err := p.Provider.Stream(ctx, req)
	if err != nil {
		return "", fmt.Errorf("research preflight: provider stream: %w", err)
	}
	var buf strings.Builder
	for ev := range ch {
		switch ev.Kind {
		case providers.EventTextDelta:
			buf.WriteString(ev.Text)
		case providers.EventError:
			if ev.Err != nil {
				return buf.String(), fmt.Errorf("research preflight: provider error: %w", ev.Err)
			}
		}
	}
	return buf.String(), nil
}

// parseClarifyLines extracts up to max non-empty clarifying questions
// from the model's line-oriented response. It strips bullet/numbering
// prefixes the model may emit despite the prompt (reusing the package's
// stripBulletPrefix), drops lines that are not actually questions in
// disguise (a bare "none" / "n/a" sentinel some models emit), and
// deduplicates case-insensitively. A response with no parseable lines
// yields an empty slice, which the caller reads as "no clarification
// needed".
func parseClarifyLines(body string, max int) []string {
	seen := map[string]bool{}
	out := make([]string, 0, max)
	for _, line := range strings.Split(body, "\n") {
		s := strings.TrimSpace(line)
		s = stripBulletPrefix(s)
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if isNoClarifySentinel(s) {
			continue
		}
		key := strings.ToLower(s)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
		if len(out) >= max {
			break
		}
	}
	return out
}

// isNoClarifySentinel reports whether s is one of the "nothing to ask"
// sentinels a model emits despite the "return nothing" instruction
// ("none", "n/a", "no clarification needed", etc.). Matched on the
// lower-cased, punctuation-trimmed line so "None." and "none" both hit.
func isNoClarifySentinel(s string) bool {
	t := strings.ToLower(strings.TrimSpace(s))
	t = strings.Trim(t, ".!:")
	t = strings.TrimSpace(t)
	switch t {
	case "none", "n/a", "na", "no clarification needed",
		"no clarifications needed", "no clarifying questions",
		"no clarifying questions needed", "no questions",
		"nothing to clarify", "no clarification required":
		return true
	}
	return false
}

// collapseParagraph flattens a possibly multi-line model response into a
// single trimmed paragraph: each line is trimmed, blank lines are
// dropped, and the survivors are joined with single spaces. This keeps
// the brief one paragraph even when the model emits soft-wrapped lines,
// and makes the result safe to preload into a single-line composer for
// editing.
func collapseParagraph(body string) string {
	var parts []string
	for _, line := range strings.Split(body, "\n") {
		s := strings.TrimSpace(line)
		if s == "" {
			continue
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}
