// Phase 5 slice 5e — citations-required check for research artifacts.
//
// SPEC § Manage mode § What the user monitors: "confidently-wrong /
// drifting outputs — verification-failed flags, citation-missing
// markers." This file is the citation-missing detector.
//
// # What it does
//
// CitationAuditor scans an artifact body for two things:
//
//   1. Citations — every URL, file path, and bracketed numeric ref
//      that looks like a source pointer.
//   2. Claims — every sentence that carries factual signal (numbers,
//      dates, "according to" phrases, proper nouns capitalised
//      mid-sentence).
//
// It then matches claims to citations by proximity: a claim is
// "supported" if there is at least one citation within ±1 sentence of
// it. Unsupported claims accumulate in the Audit; a Score = (cited
// claims) / (total claims) gives the caller a one-number summary the
// approval-queue title can show.
//
// # Heuristic discipline (intentionally simple)
//
// The brief explicitly calls for a 70%-ish precision/recall heuristic:
// it's a CHECK, not a gate. Concretely:
//
// CATCHES:
//   - Bare assertions with no link, file path, or numeric ref
//   - "Studies show X" / "research finds Y" patterns with no source
//   - Sentences with concrete numbers / dates and no nearby citation
//   - Author/work/year claims missing a pointer
//
// MISSES (acceptable):
//   - Claims supported by a citation in the SAME paragraph but >1
//     sentence away. The proximity window is intentionally tight; a
//     larger window inflates false-negative rate.
//   - Claims encoded as code snippets the heuristic doesn't recognise
//     as factual (e.g. function names, variable values).
//   - Citations that point at fictional sources — we don't fetch the
//     URL or verify the path exists. Tool-grounded verification is
//     Phase 5d's job.
//
// FALSE POSITIVES we explicitly mitigate:
//   - Shell command output lines ("ls /tmp", "$ npm install") are
//     suppressed by a leading-shell-marker check so command examples
//     don't get flagged as claims.
//   - Code-fence content is excluded from claim detection wholesale
//     (between ``` markers).
//
// # Where it fits in the loop
//
// Called by the foreground integrator AFTER the verifier on artifacts
// flagged requires_citations: true (today: heuristic = kind ==
// ArtifactKindResearch). On a low Score, the artifact is queued via
// ProposeApproval with "citations missing" in the title. This is
// orthogonal to the LLM-as-judge verifier — both can fire on the same
// artifact and produce independent signals.
package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// CitationAuditor is the configurable auditor. The zero value works;
// no construction is required for the default behavior. Knobs are
// exposed for tests that want to exercise edge cases (different
// proximity windows, etc).
type CitationAuditor struct {
	// ProximityWindow is how many sentences before/after a claim count
	// as "nearby" for citation matching. Default (zero) = 1 sentence
	// in each direction. Set higher to be more lenient.
	ProximityWindow int

	// MinClaimWords filters trivial sentences out of claim detection
	// to avoid false-positives on greetings, transitions, headings,
	// etc. Default (zero) = 4 words.
	MinClaimWords int
}

// Audit is what CitationAuditor.Audit returns.
type Audit struct {
	// ClaimCount is the total number of sentences classified as
	// factual claims (after suppressing code-fenced + shell content).
	ClaimCount int

	// Citations is the deduplicated list of every URL / file path /
	// numeric ref found anywhere in the body, in first-appearance
	// order. Useful for the TUI to show "what sources did this
	// artifact cite" at a glance.
	Citations []string

	// Unsupported lists the claim sentences that had no citation
	// within the proximity window. Ordered by appearance.
	Unsupported []string

	// Score is the ratio of supported claims to total claims, in
	// [0.0, 1.0]. Zero claims yields 1.0 (an artifact with no
	// claim-shaped sentences trivially has 100% citation coverage).
	Score float64
}

// urlRE matches http/https URLs. We deliberately keep this loose to
// avoid the regex tarpit; the cost of a false positive (a non-URL
// string flagged as a citation) is zero — the worst case is we say a
// claim IS supported when it isn't, which the verifier or human
// catches.
var urlRE = regexp.MustCompile(`https?://[^\s)\]}>"'` + "`" + `]+`)

// pathRE matches POSIX-ish file paths used as citations. We accept:
//   - absolute paths starting with /
//   - dot-relative paths starting with ./ or ../
// We do NOT accept bare relative names like "foo.txt" because too
// many false positives — names of things in regular prose.
var pathRE = regexp.MustCompile(`(?:^|[\s(\[])((?:/|\.{1,2}/)[A-Za-z0-9_./\-]+)`)

// refRE matches bracketed numeric refs like [1], [12], [42].
var refRE = regexp.MustCompile(`\[\d+\]`)

// codeFenceRE matches a triple-backtick fenced block (non-greedy
// across newlines). We strip these wholesale before claim detection
// so command examples and code don't get flagged as factual prose.
var codeFenceRE = regexp.MustCompile("(?s)```[^\n]*\n.*?```")

// inlineCodeRE matches single-backtick inline code spans — also
// stripped from claim text so `ls /tmp` doesn't read as a claim.
var inlineCodeRE = regexp.MustCompile("`[^`]+`")

// shellPrefixRE detects lines that look like shell commands or
// command output. We suppress these from claim detection.
var shellPrefixRE = regexp.MustCompile(`^\s*(?:\$|#|>)\s+`)

// signalWords trigger a claim classification when found in a sentence
// (case-insensitive). The list is the obvious "factual assertion"
// vocabulary — it doesn't have to be exhaustive; numerics + proper
// nouns catch most cases that miss this list.
var signalWords = []string{
	"according to",
	"research shows",
	"studies show",
	"studies have shown",
	"study found",
	"study finds",
	"evidence suggests",
	"reportedly",
	"it is known",
	"it is documented",
	"the data show",
	"the data shows",
	"published",
	"as reported",
}

// numericRE matches any digit run (used as a factual-signal heuristic).
var numericRE = regexp.MustCompile(`\d`)

// Audit returns a citation-coverage report for content.
//
// The function is pure / deterministic. No I/O, no time. Same body
// always produces the same Audit.
func (c *CitationAuditor) Audit(content []byte) Audit {
	proximity := c.ProximityWindow
	if proximity <= 0 {
		proximity = 1
	}
	minWords := c.MinClaimWords
	if minWords <= 0 {
		minWords = 4
	}

	text := string(content)

	// 1. Extract every citation — done on the raw text so URLs inside
	//    code fences still register (they're still sources). Dedupe
	//    while preserving first-appearance order.
	citations := extractCitations(text)

	// 2. Strip code fences + inline code BEFORE claim detection so
	//    command examples don't muddy the prose statistics.
	stripped := stripCode(text)

	// 3. Split into sentences. Naive split on . ! ? at sentence
	//    boundary is enough for v0 — the heuristic is loose by design.
	sentences := splitSentences(stripped)

	// 4. Per sentence: classify as claim / non-claim; record which
	//    sentences have any citation in them; record proximity to
	//    nearest sentence with a citation.
	type info struct {
		text       string
		isClaim    bool
		hasCite    bool
	}
	rows := make([]info, len(sentences))
	for i, s := range sentences {
		rows[i] = info{
			text:    s,
			isClaim: isClaim(s, minWords),
			hasCite: sentenceHasCitation(s),
		}
	}

	// 5. Find unsupported claims: claims with no citation within
	//    proximity sentences in either direction.
	var unsupported []string
	claimCount := 0
	for i, row := range rows {
		if !row.isClaim {
			continue
		}
		claimCount++
		supported := false
		lo := i - proximity
		if lo < 0 {
			lo = 0
		}
		hi := i + proximity
		if hi >= len(rows) {
			hi = len(rows) - 1
		}
		for j := lo; j <= hi; j++ {
			if rows[j].hasCite {
				supported = true
				break
			}
		}
		if !supported {
			unsupported = append(unsupported, row.text)
		}
	}

	score := 1.0
	if claimCount > 0 {
		score = float64(claimCount-len(unsupported)) / float64(claimCount)
	}

	return Audit{
		ClaimCount:  claimCount,
		Citations:   citations,
		Unsupported: unsupported,
		Score:       score,
	}
}

// extractCitations returns the dedup'd, first-appearance-ordered list
// of citations in text. URLs, /-rooted or ./-rooted paths, and [N]
// numeric refs all count.
func extractCitations(text string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimRight(s, ".,;:")
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}
	for _, m := range urlRE.FindAllString(text, -1) {
		add(m)
	}
	for _, m := range pathRE.FindAllStringSubmatch(text, -1) {
		if len(m) >= 2 {
			add(m[1])
		}
	}
	for _, m := range refRE.FindAllString(text, -1) {
		add(m)
	}
	return out
}

// stripCode removes code-fenced blocks and inline code spans from text
// so they don't pollute claim detection. URLs inside code blocks are
// still picked up by extractCitations (which runs against the raw
// text); this strip is only for claim-detection input.
func stripCode(text string) string {
	text = codeFenceRE.ReplaceAllString(text, "")
	text = inlineCodeRE.ReplaceAllString(text, "")
	return text
}

// splitSentences chops text into sentence-shaped chunks. We split on
// . ! ? followed by whitespace + capital letter OR end-of-string. Not
// perfect — "Mr. Smith" splits — but at v0 the loose split is fine.
func splitSentences(text string) []string {
	// First, normalise newlines. A bare newline inside a paragraph
	// is one space; a blank line (paragraph break) acts as a hard
	// sentence boundary.
	text = strings.ReplaceAll(text, "\r\n", "\n")
	paragraphs := strings.Split(text, "\n\n")
	var sentences []string
	for _, p := range paragraphs {
		p = strings.TrimSpace(strings.ReplaceAll(p, "\n", " "))
		if p == "" {
			continue
		}
		// Per-paragraph sentence split.
		start := 0
		for i := 0; i < len(p); i++ {
			c := p[i]
			if c != '.' && c != '!' && c != '?' {
				continue
			}
			// Lookahead: end-of-paragraph, OR whitespace + capital.
			end := i + 1
			if end >= len(p) {
				sentences = append(sentences, strings.TrimSpace(p[start:end]))
				start = end
				break
			}
			// Skip trailing punctuation like "?!" or ".)" — extend i
			// to include them.
			for end < len(p) && (p[end] == '.' || p[end] == '!' || p[end] == '?' || p[end] == ')' || p[end] == ']' || p[end] == '"' || p[end] == '\'') {
				end++
			}
			if end >= len(p) {
				sentences = append(sentences, strings.TrimSpace(p[start:end]))
				start = end
				break
			}
			if !unicode.IsSpace(rune(p[end])) {
				continue
			}
			// Find next non-space.
			j := end
			for j < len(p) && unicode.IsSpace(rune(p[j])) {
				j++
			}
			if j >= len(p) {
				sentences = append(sentences, strings.TrimSpace(p[start:end]))
				start = end
				break
			}
			if unicode.IsUpper(rune(p[j])) || p[j] == '[' || p[j] == '"' {
				sentences = append(sentences, strings.TrimSpace(p[start:end]))
				start = j
				i = j - 1
			}
		}
		if start < len(p) {
			tail := strings.TrimSpace(p[start:])
			if tail != "" {
				sentences = append(sentences, tail)
			}
		}
	}
	return sentences
}

// isClaim heuristically decides whether a sentence carries a factual
// assertion worth checking for citation. Returns true when ANY of:
//   - sentence contains a signal word/phrase (case-insensitive)
//   - sentence contains a numeric digit AND has enough words to look
//     like a statement (not a header / list marker)
//   - sentence has 2+ capitalised mid-sentence words (proper-noun
//     density above noise)
//
// Returns false on shell-prefix lines and on short fragments.
func isClaim(sentence string, minWords int) bool {
	s := strings.TrimSpace(sentence)
	if s == "" {
		return false
	}
	if shellPrefixRE.MatchString(s) {
		return false
	}
	// Word count guard.
	words := strings.Fields(s)
	if len(words) < minWords {
		return false
	}
	low := strings.ToLower(s)
	for _, w := range signalWords {
		if strings.Contains(low, w) {
			return true
		}
	}
	if numericRE.MatchString(s) {
		return true
	}
	// Proper-noun density: count words that are capitalized AND not the
	// first word of the sentence AND not all-caps acronyms-only.
	properMid := 0
	for i, w := range words {
		if i == 0 {
			continue
		}
		if len(w) == 0 {
			continue
		}
		r := rune(w[0])
		if unicode.IsUpper(r) {
			properMid++
		}
		if properMid >= 2 {
			return true
		}
	}
	return false
}

// sentenceHasCitation reports whether s contains any URL, path, or
// numeric ref. Used to mark a sentence as "carrying its own citation"
// so the proximity check can credit it.
func sentenceHasCitation(s string) bool {
	if urlRE.MatchString(s) {
		return true
	}
	if pathRE.MatchString(s) {
		return true
	}
	if refRE.MatchString(s) {
		return true
	}
	return false
}

// ErrCitationCheckFailed is the wrap returned by the integration helper
// when the caller wants to gate on a minimum Score. Sentinels mirror
// the budget package's shape for symmetry.
var ErrCitationCheckFailed = errors.New("citations: coverage below threshold")

// AuditAndQueue runs CitationAuditor.Audit on content and, if the
// score is below threshold, queues the artifact for human review with
// "citations missing" in the title. Returns the Audit either way; the
// returned error wraps ErrCitationCheckFailed on a low-score queue.
//
// Parallel to VerifyAndQueue (slice 5c) — same hook shape so the
// foreground integrator can call them back-to-back on the same
// artifact and get two independent signals.
//
// threshold is the minimum acceptable Score in [0.0, 1.0]. Zero means
// "any coverage at all is acceptable" — only artifacts with all-
// unsupported claims get queued. 1.0 means "every claim must be
// cited" (strict; will queue any artifact with even one unsupported
// claim).
func AuditAndQueue(ctx context.Context, log *SQLiteEventLog, ref ArtifactRef, content []byte, threshold float64) (Audit, error) {
	if log == nil {
		return Audit{}, errors.New("citations: nil log")
	}
	a := (&CitationAuditor{}).Audit(content)
	if a.Score >= threshold {
		return a, nil
	}
	title := composeCitationTitle(ref, a)
	if _, err := ProposeApproval(ctx, log, ref.AgentID, title, ref); err != nil {
		return a, fmt.Errorf("citations: propose: %w", err)
	}
	return a, fmt.Errorf("%w: score %.2f < %.2f, %d/%d claims unsupported", ErrCitationCheckFailed, a.Score, threshold, len(a.Unsupported), a.ClaimCount)
}

// composeCitationTitle renders the queue title for a citation-deficient
// audit. Title shape:
//
//	"(citations missing: N/M unsupported) <kind> artifact from <agentID>"
func composeCitationTitle(ref ArtifactRef, a Audit) string {
	return fmt.Sprintf("(citations missing: %d/%d unsupported) %s artifact from %s",
		len(a.Unsupported), a.ClaimCount, ref.Kind, ref.AgentID)
}
