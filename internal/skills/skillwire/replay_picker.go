// Phase 6 slice 6f - replay-eval picker.
//
// The picker maps a candidate skill (and the historical transcripts it
// was induced from) to ONE tool-grounded verifier kind. If no kind fits,
// the picker returns "" and the caller (ReplayEvaluator.Evaluate) marks
// the replay as Skipped - the proposal then goes straight to human
// review, same as it did before slice 6f existed.
//
// # Architectural commitment (from the slice-6f brief)
//
//   - One verifier per replay. Picking multiple would dilute the signal
//     and complicate the win/loss accounting in ReplayReport.Score.
//   - Heuristic, not learned. False negatives (skip when we could have
//     replayed) are fine; false positives (replay with the WRONG
//     verifier) are worse - they produce noise that downstream
//     thresholds can't distinguish from real signal.
//   - Picker decision is recorded on the report so a post-mortem can
//     answer "why did this skill skip the replay step?".
//
// # Heuristics, ranked
//
// The picker walks the proposal in this order and returns the first
// kind that matches. Earlier rules dominate later ones - that's why the
// ordering is documented inline at each branch.
//
//  1. Body mentions a test invocation ("go test", "cargo test",
//     "npm test", "pytest"). High-precision signal that the skill is
//     about running tests; route to the "diff" kind (Compiler+Tests).
//  2. Body mentions a build invocation ("go build", "cargo build",
//     "npm run build"). Also routes to "diff" - Compiler alone is
//     enough to score build-only skills.
//  3. Any transcript message contained an artifact_ref to a
//     research-kind artifact OR the skill description / body talks
//     about "fetch", "URL", "research", "summarize source". Route to
//     "research" → URLRefetcher.
//  4. Otherwise: return "" → Skipped.
//
// The phrase set for each rule is intentionally narrow. If the brief
// later adds new verifiers, extend the rule table rather than
// re-arranging existing rules; downstream tests pin the ordering.
package skillwire

import (
	"strings"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/providers"
	"github.com/georgebuilds/carlos/internal/skills"
)

// pickerDecision is what PickVerifierKind returns. Kind is the empty
// string when no verifier fits. Reason is a short human-readable
// explanation surfaced on ReplayReport.SkippedReason - keep it terse,
// it lands in user-visible logs.
type pickerDecision struct {
	Kind   string
	Reason string
}

// PickVerifierKind inspects the candidate skill and the transcripts it
// was induced from, and returns the verifier kind to run during replay
// - or ("", reason) when nothing fits. Pure function: safe to test in
// isolation and safe to call from anywhere.
//
// The set of returned kinds is the same set agent.Dispatcher's default
// mapping registers: "diff" (Compiler+TestRunner) and "research"
// (URLRefetcher). The "plan" kind exists in the Dispatcher defaults
// but no skill heuristic targets it directly - plan artifacts are
// produced by the spawn flow, not by skill bodies, so a replay round
// is unlikely to surface a plan kind organically.
func PickVerifierKind(p *skills.Proposal, transcripts [][]providers.Message) pickerDecision {
	if p == nil {
		return pickerDecision{Kind: "", Reason: "nil proposal"}
	}

	body := strings.ToLower(p.Body)
	desc := strings.ToLower(p.Description)

	// Rule 1: test invocation in the body → diff (Compiler+Tests).
	for _, needle := range testInvocationNeedles {
		if strings.Contains(body, needle) {
			return pickerDecision{Kind: agent.ArtifactKindDiff, Reason: "body mentions test invocation: " + needle}
		}
	}

	// Rule 2: build invocation in the body → diff (Compiler alone is
	// enough; Dispatcher's "diff" kind also includes TestRunner which
	// will run cleanly if the project has tests).
	for _, needle := range buildInvocationNeedles {
		if strings.Contains(body, needle) {
			return pickerDecision{Kind: agent.ArtifactKindDiff, Reason: "body mentions build invocation: " + needle}
		}
	}

	// Rule 3: research-style content. Either the transcripts produced
	// a research-kind artifact (we sniff for the substring "research"
	// in tool_result bodies - see transcriptsMentionResearch below) OR
	// the skill description / body uses research vocabulary.
	if transcriptsMentionResearch(transcripts) {
		return pickerDecision{Kind: agent.ArtifactKindResearch, Reason: "transcript referenced a research artifact"}
	}
	for _, needle := range researchVocabularyNeedles {
		if strings.Contains(body, needle) || strings.Contains(desc, needle) {
			return pickerDecision{Kind: agent.ArtifactKindResearch, Reason: "research vocabulary: " + needle}
		}
	}

	return pickerDecision{Kind: "", Reason: "no tool-grounded verifier fits this skill"}
}

// testInvocationNeedles is the set of substrings that signal "the skill
// is about running tests". Lower-cased; we lower the body before
// matching. Order matters only for the Reason string (the first match
// wins) - not for the kind decision.
var testInvocationNeedles = []string{
	"go test",
	"cargo test",
	"npm test",
	"pytest",
	"jest",
	"phpunit",
}

// buildInvocationNeedles is the build-only analog. Kept separate from
// testInvocationNeedles so a body that does BOTH ("go build ./... &&
// go test ./...") matches the test rule first and the Reason string
// stays informative.
var buildInvocationNeedles = []string{
	"go build",
	"cargo build",
	"npm run build",
	"make build",
	"python -m compileall",
}

// researchVocabularyNeedles is the URLRefetcher trigger. Conservative
// on purpose - "fetch" and "URL" are common in non-research skills too,
// so we require they appear in the SKILL body or description, not in
// the transcript.
var researchVocabularyNeedles = []string{
	"refetch",
	"http get",
	"curl -s",
	"summarize source",
	"cite source",
}

// transcriptsMentionResearch scans every message in every transcript
// for a substring that looks like a research-kind artifact reference.
// Conservative match: the kind constant value "research" appearing
// inside an artifact-ref tool_result body. This is intentionally a
// substring sniff - the eventlog projection has structured artifact
// records, but the transcripts the caller hands us are raw provider
// messages from agent.Run and don't carry the same shape.
func transcriptsMentionResearch(transcripts [][]providers.Message) bool {
	const needle = `"kind":"research"`
	for _, msgs := range transcripts {
		for _, m := range msgs {
			for _, b := range m.Content {
				if len(b.ToolResult) > 0 && strings.Contains(string(b.ToolResult), needle) {
					return true
				}
				if b.Kind == "text" && strings.Contains(strings.ToLower(b.Text), "research artifact") {
					return true
				}
			}
		}
	}
	return false
}
