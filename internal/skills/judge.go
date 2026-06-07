// judge.go - cross-provider triage judge.
//
// # Why cross-provider
//
// Zheng et al. NeurIPS 2023 ("Judging LLM-as-a-Judge") confirmed
// self-preference bias is real: a model judging its own output rates
// it higher than an outside judge does. carlos's multi-provider stack
// makes the mitigation free - induce with Anthropic, judge with
// OpenAI; induce with Ollama, judge with OpenRouter; etc.
//
// # Bias controls in the prompt
//
// Two well-documented biases creep in even cross-provider:
//
//   - Verbosity bias: longer outputs score higher absent prompting.
//   - Position bias: first-listed item scores higher in pairwise
//     prompts.
//
// We mitigate both: the prompt explicitly tells the judge to score
// INDEPENDENTLY OF LENGTH and uses POINTWISE grading (no comparator
// item, so position is moot). The 1-10 quality scale is integer to
// resist score-anchor drift.
//
// # Cost
//
// Per call (mid-tier model): ~1,000 input + ~200 output ≈ $0.005. The
// judge runs once per proposal, so this is the price floor for the
// cross-provider gate.
package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/georgebuilds/carlos/internal/providers"
)

// JudgePromptTemplate is the system prompt. Embeds {{PROPOSAL_NAME}},
// {{PROPOSAL_DESC}}, {{PROPOSAL_BODY}}. Reviewed in the godoc; edits
// here are user-visible.
const JudgePromptTemplate = `You are an INDEPENDENT reviewer of a candidate agent skill. The candidate was induced by a different provider; do not assume any particular author or system created it.

A skill is worth saving iff a future agent doing a similar task would benefit from following the same steps. Generic knowledge, single-source lookups, and procedures completable in 1-2 reasoning turns are NOT worth saving.

Score INDEPENDENTLY OF LENGTH. A terse, correct, executable skill is BETTER than a verbose, prose-heavy one. Do NOT reward longer bodies.

Output FORMAT: a single JSON object, no code fence, no commentary:

  {
    "quality": <integer 1-10; 1=worthless, 5=mediocre, 10=excellent>,
    "decision": "accept" | "reject" | "needs_revision",
    "concerns": ["<short bullet>", "<short bullet>", ...]
  }

CALIBRATION:
  - "accept" requires quality >= 7 AND no critical concerns.
  - "needs_revision" is for skills with a real kernel but fixable issues (vague description, missing trigger, body too prose-heavy).
  - "reject" is for not-actually-reusable, duplicative, or harmful candidates.

CANDIDATE:
  name:        {{PROPOSAL_NAME}}
  description: {{PROPOSAL_DESC}}
  body:
{{PROPOSAL_BODY}}
`

// Decision values returned by the judge. Stable strings - they land
// in the SKILL.md frontmatter / artifact metadata.
const (
	DecisionAccept        = "accept"
	DecisionReject        = "reject"
	DecisionNeedsRevision = "needs_revision"
)

// Score is the structured output of one Judge.Score call.
type Score struct {
	Quality    int      `json:"quality"`
	Decision   string   `json:"decision"`
	Concerns   []string `json:"concerns,omitempty"`
	JudgeModel string   `json:"judge_model"`
	// RawResponse preserved for the approval-queue UX (same rationale
	// as Proposal.RawResponse).
	RawResponse string `json:"raw_response,omitempty"`
}

// Judge wraps a provider used as the cross-provider scorer.
type Judge struct {
	Provider providers.Provider
}

// NewJudge is a convenience.
func NewJudge(p providers.Provider) *Judge {
	return &Judge{Provider: p}
}

// JudgeOptions controls per-call knobs.
type JudgeOptions struct {
	Model string
}

// Score runs the judge on a proposal. Returns the parsed Score; the
// Decision field tells the caller whether to propose the artifact for
// human review.
func (j *Judge) Score(ctx context.Context, p *Proposal, opts JudgeOptions) (*Score, error) {
	if j == nil || j.Provider == nil {
		return nil, errors.New("judge: nil provider")
	}
	if p == nil {
		return nil, errors.New("judge: nil proposal")
	}

	system := buildJudgePrompt(p)
	req := providers.Request{
		Model:  opts.Model,
		System: system,
		Messages: []providers.Message{
			{
				Role:    "user",
				Content: []providers.Block{{Kind: "text", Text: "Score."}},
			},
		},
	}

	raw, err := streamText(ctx, j.Provider, req)
	if err != nil {
		return nil, fmt.Errorf("judge: stream: %w", err)
	}

	parsed, err := parseJudgeOutput(raw)
	if err != nil {
		return nil, fmt.Errorf("judge: parse: %w (raw=%q)", err, truncateForError(raw, 200))
	}
	parsed.JudgeModel = providerLabel(j.Provider, opts.Model)
	parsed.RawResponse = raw
	return parsed, nil
}

// buildJudgePrompt substitutes the three placeholders. The body is
// indented one level so it sits visually inside the YAML-ish layout
// the template uses.
func buildJudgePrompt(p *Proposal) string {
	out := strings.ReplaceAll(JudgePromptTemplate, "{{PROPOSAL_NAME}}", p.Name)
	out = strings.ReplaceAll(out, "{{PROPOSAL_DESC}}", p.Description)
	body := indent(p.Body, "    ")
	out = strings.ReplaceAll(out, "{{PROPOSAL_BODY}}", body)
	return out
}

func indent(s, prefix string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}

func parseJudgeOutput(raw string) (*Score, error) {
	s := strings.TrimSpace(raw)
	s = stripCodeFence(s)
	if s == "" {
		return nil, errors.New("empty response")
	}
	var sc Score
	if err := json.Unmarshal([]byte(s), &sc); err != nil {
		return nil, err
	}
	if sc.Quality < 1 || sc.Quality > 10 {
		return nil, fmt.Errorf("quality %d out of range 1-10", sc.Quality)
	}
	switch sc.Decision {
	case DecisionAccept, DecisionReject, DecisionNeedsRevision:
	default:
		return nil, fmt.Errorf("unknown decision %q", sc.Decision)
	}
	return &sc, nil
}

// SelectJudgeProvider picks the first provider in `available` that is
// NOT the inducer's. Returns an error if no different provider is
// available - the caller should fall back to "human-only review" in
// that case (the proposal still queues for the user; it just lacks
// the automated score).
//
// `available` is typically the list of providers configured in
// cfg.Providers; carlos's onboarding requires at least one but the
// cross-provider mitigation only kicks in at >=2.
func SelectJudgeProvider(induceProvider string, available []string) (string, error) {
	if induceProvider == "" {
		return "", errors.New("judge: empty inducer name")
	}
	for _, p := range available {
		if p != "" && p != induceProvider {
			return p, nil
		}
	}
	return "", fmt.Errorf("judge: no provider other than %q configured (need at least 2 for cross-provider bias mitigation)", induceProvider)
}
