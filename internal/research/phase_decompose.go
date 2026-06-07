package research

import (
	"context"
	"fmt"
	"strings"
)

// decomposeSystem is the system prompt for the planning phase. The
// "ONE per line, no numbering" rule is load-bearing - every other
// shape (bulleted list, JSON, prose paragraphs) is harder to parse
// reliably across models. Plain line-per-query falls out cleanly.
const decomposeSystem = `You decompose research questions into specific sub-queries. Each sub-query is a focused search prompt that, when answered, contributes one piece of the overall picture. Return ONE sub-query per line, no numbering, no bullets, no commentary.`

// decomposeUser is the user-message template; %s is the question.
const decomposeUserTemplate = `Break this question into 3-5 specific sub-queries that, if answered, would together cover the question. Return ONE per line, no numbering.

Question: %s`

// runDecompose calls the LLM to produce up to MaxSubQueries sub-
// queries, parses the line-oriented response, and writes them into
// Report.Query.Sub. If the model returns nothing parseable, the
// engine falls back to using the original question as a single
// sub-query so downstream phases still have something to chew on.
// The fallback is recorded in Concerns so the caller knows the
// decomposition was degenerate.
func (e *Engine) runDecompose(ctx context.Context, report *Report) (err error) {
	t0 := e.beginPhase("decompose")
	defer func() { e.endPhase("decompose", t0, err) }()

	user := fmt.Sprintf(decomposeUserTemplate, report.Question)
	body, err := e.callProvider(ctx, report, decomposeSystem, user)
	if err != nil {
		return err
	}
	subs := parseDecomposeLines(body, e.MaxSubQueries)
	if len(subs) == 0 {
		report.Concerns = append(report.Concerns,
			"decompose: model returned no parseable sub-queries; falling back to original question")
		subs = []string{report.Question}
	}
	report.Query.Sub = subs
	return nil
}

// parseDecomposeLines extracts non-empty trimmed lines, stripping
// leading bullet/numbering markers the model may emit despite the
// prompt. Caps at max. Deduplicates case-insensitively.
func parseDecomposeLines(body string, max int) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(body, "\n") {
		s := strings.TrimSpace(line)
		s = stripBulletPrefix(s)
		s = strings.TrimSpace(s)
		if s == "" {
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

// stripBulletPrefix removes common list markers a model might emit
// despite the "no numbering, no bullets" instruction: "- ", "* ",
// "1. ", "1) ", "• ", etc. Single-pass; no recursion.
func stripBulletPrefix(s string) string {
	if s == "" {
		return s
	}
	// Bullet glyphs.
	for _, p := range []string{"- ", "* ", "• ", "– ", "— "} {
		if strings.HasPrefix(s, p) {
			return s[len(p):]
		}
	}
	// "1. " / "12. " / "1) " / "12) " - strip leading digits + . or ).
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i > 0 && i < len(s) && (s[i] == '.' || s[i] == ')') {
		j := i + 1
		if j < len(s) && s[j] == ' ' {
			return s[j+1:]
		}
	}
	return s
}
