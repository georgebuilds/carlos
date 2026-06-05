// Phase 5 slice 5c — separate-model verifier pass.
//
// Empirical context (see /Volumes/nas/vault/personal/projects/carlos/
// research/2026-06-04 Supervisor — decisions adopted.md):
//
//   - MAST: ~32.3% of multi-agent failures cascade through
//     verification/termination failures. A verifier is necessary.
//   - "Too Consistent to Detect" (arXiv:2505.17656): a model judging
//     its own output produces self-consistent errors that are stable
//     or *increase* with scale. A verifier is necessary but the
//     verifier MUST be a different model.
//   - "Verifier is necessary but not sufficient" (MAST) — the verifier
//     is one of several layers; human-in-the-loop remains the final
//     check. Verifier output is fed into the existing approval queue
//     (see verifier_hook.go) rather than gating silently.
//   - Verifier on cheap read tasks INVERTS the cost math (research
//     notes, deferred bucket). Today: opt-in per artifact kind; v0 fires
//     on kind == ArtifactKindAgentFinal and on artifacts marked
//     requires_verification (a future flag — for v0 just the kind
//     selection in verifier_hook.go).
//
// Bias controls in the prompt:
//
//   - Pointwise grading (one artifact, one score) rather than pairwise
//     (which one is better). Pairwise inflates positional bias.
//   - Explicit "score independently of length" and "score independently
//     of presentation order" — the LLM-as-judge bias literature.
//   - Decision is structured (accept | needs_revision | reject) so a
//     wandering verifier still produces something parseable; freeform
//     verdicts produce mushy data the approval queue can't act on.
//
// The verifier prompt is intentionally short. The full artifact body
// follows verbatim — we trust the judge model to read it. No
// preprocessing beyond a UTF-8 byte cap; carlos artifacts at v0 are
// well under context window for any modern judge model.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/georgebuilds/carlos/internal/providers"
)

// VerificationDecision is the structured verdict the judge returns.
// Kept as a string (not an int enum) so the JSON the judge emits maps
// directly without a translation table.
type VerificationDecision string

const (
	VerificationAccept         VerificationDecision = "accept"
	VerificationNeedsRevision  VerificationDecision = "needs_revision"
	VerificationReject         VerificationDecision = "reject"
)

// VerificationReport is the post-verification record. It is itself
// persistable as JSON; verifier_hook.go embeds the concerns into the
// approval queue's title when the decision is non-accept.
type VerificationReport struct {
	// Score is the judge's 1-10 confidence in the artifact's quality.
	// 10 = high confidence accept, 1 = should be rejected. Threshold
	// for "needs revision" is judge-side; we just store the number.
	Score int `json:"score"`

	// Concerns is the judge's free-text list of specific issues. May
	// be empty on a clean accept. The approval queue's title shows the
	// first concern truncated if non-empty.
	Concerns []string `json:"concerns,omitempty"`

	// Decision is the structured verdict — see VerificationDecision
	// constants.
	Decision VerificationDecision `json:"decision"`

	// JudgeModel is the provider:model identifier of the judge for
	// audit. Format: "<provider.Name()>:<model>" (e.g.
	// "anthropic:claude-opus-4", "openai:gpt-5"). Lets the runaway-
	// cost view attribute verifier spend to the right provider.
	JudgeModel string `json:"judge_model"`

	// Raw is the judge's full response body (the JSON it returned).
	// Stored verbatim so a post-mortem can inspect malformed verdicts
	// without re-querying.
	Raw string `json:"raw,omitempty"`
}

// Sentinels for verifier failure modes the caller (verifier_hook.go,
// future runHeadless wiring) needs to distinguish.
var (
	// ErrNoJudgeAvailable is returned by SelectJudgeProvider when only
	// one provider is configured. Callers fall back to human-only
	// review (skip verifier; surface artifact unverified).
	ErrNoJudgeAvailable = errors.New("verifier: no separate-provider judge available")

	// ErrMalformedJudgeResponse is returned when the judge's output
	// can't be parsed as the expected JSON shape. Surfaced as an
	// infra error so the caller doesn't silently accept a wandering
	// verifier — per the "necessary but not sufficient" rule, a
	// broken verifier should be loud, not silent.
	ErrMalformedJudgeResponse = errors.New("verifier: malformed judge response")
)

// Verifier is the separate-model check pass. The Judge provider is
// supplied at construction time; callers (the foreground integrator)
// pick a Judge via SelectJudgeProvider so the verifier is guaranteed
// not to be the inducer.
type Verifier struct {
	Judge providers.Provider

	// JudgeModel is the model id passed to the judge provider. Empty
	// = let the provider pick its default. The full provider:model
	// string is recorded in every VerificationReport.JudgeModel for
	// audit.
	JudgeModel string

	// MaxArtifactBytes caps how much of the artifact body we feed to
	// the judge. Zero = no cap. Default at the call site is
	// DefaultMaxArtifactBytes — 128KiB, comfortably under any modern
	// model's context window with prompt+system included.
	MaxArtifactBytes int
}

// DefaultMaxArtifactBytes is the soft cap applied if a Verifier is
// constructed with MaxArtifactBytes == 0. 128KiB; see Verifier docs.
const DefaultMaxArtifactBytes = 128 * 1024

// verifierSystemPrompt is the system message sent to the judge. The
// bias-control language is load-bearing — see the file header for the
// empirical justification. Kept short so the judge's attention stays
// on the artifact body.
//
// "Pointwise" / "score independently of length" / "score independently
// of presentation order" are the three bias dimensions the LLM-as-judge
// literature flags as most consequential.
const verifierSystemPrompt = `You are a verification judge. You will be shown one artifact produced by another AI agent. Your job is to assess whether the artifact matches its stated purpose, whether factual claims are supported, and whether there are obvious errors.

Grading discipline:
- This is a POINTWISE evaluation (one artifact, one score). Do not compare against a hypothetical "better" version.
- Score INDEPENDENTLY of length. A short artifact may be excellent; a long one may be padded.
- Score INDEPENDENTLY of presentation order or formatting flourish. Judge content.

Return STRICT JSON with this exact shape and no surrounding prose:
{"score": <integer 1-10>, "concerns": ["<string>", ...], "decision": "accept" | "needs_revision" | "reject"}

Scoring guide:
- 9-10: clean accept; no concerns worth surfacing.
- 6-8: accept with minor concerns; list them in "concerns".
- 4-5: needs revision; list the specific issues.
- 1-3: reject; the artifact does not fulfill its stated purpose.

If you cannot evaluate the artifact (truncated, unreadable, off-topic to its purpose), set decision="reject" and explain in concerns.`

// composeJudgePrompt assembles the user-message text the judge sees.
// We label sections plainly so the judge model doesn't have to infer
// boundaries — the same flat-headings format as composeInitialPrompt
// in spawn.go.
func composeJudgePrompt(ref ArtifactRef, content []byte) string {
	var b strings.Builder
	b.WriteString("# Artifact metadata\n")
	fmt.Fprintf(&b, "- kind: %s\n", ref.Kind)
	fmt.Fprintf(&b, "- size: %d bytes\n", ref.Size)
	fmt.Fprintf(&b, "- producer: %s\n", ref.AgentID)
	if ref.SHA256 != "" {
		fmt.Fprintf(&b, "- sha256: %s\n", ref.SHA256)
	}
	b.WriteString("\n# Artifact body\n```\n")
	b.Write(content)
	if len(content) > 0 && content[len(content)-1] != '\n' {
		b.WriteByte('\n')
	}
	b.WriteString("```\n")
	return b.String()
}

// Verify runs one judge call against the artifact and returns the
// parsed VerificationReport. Returns ErrMalformedJudgeResponse if the
// judge's output can't be parsed; otherwise propagates provider /
// transport errors.
//
// The function is a single LLM call — no retries, no loop. The caller
// (verifier_hook.go) decides whether to surface the report into the
// approval queue.
func (v *Verifier) Verify(ctx context.Context, ref ArtifactRef, content []byte) (VerificationReport, error) {
	if v == nil || v.Judge == nil {
		return VerificationReport{}, errors.New("verifier: nil verifier or judge")
	}

	cap := v.MaxArtifactBytes
	if cap == 0 {
		cap = DefaultMaxArtifactBytes
	}
	if len(content) > cap {
		content = content[:cap]
	}

	req := providers.Request{
		Model:  v.JudgeModel,
		System: verifierSystemPrompt,
		Messages: []providers.Message{{
			Role: "user",
			Content: []providers.Block{{
				Kind: "text",
				Text: composeJudgePrompt(ref, content),
			}},
		}},
	}
	stream, err := v.Judge.Stream(ctx, req)
	if err != nil {
		return VerificationReport{}, fmt.Errorf("verifier: stream: %w", err)
	}
	body, _, err := collectJudgeText(stream)
	if err != nil {
		return VerificationReport{}, fmt.Errorf("verifier: collect: %w", err)
	}

	report, err := parseJudgeResponse(body)
	if err != nil {
		// Keep the raw body so callers can inspect what went wrong.
		report.Raw = body
		report.JudgeModel = formatJudgeModelID(v.Judge.Name(), v.JudgeModel)
		return report, fmt.Errorf("%w: %v", ErrMalformedJudgeResponse, err)
	}
	report.Raw = body
	report.JudgeModel = formatJudgeModelID(v.Judge.Name(), v.JudgeModel)
	return report, nil
}

// formatJudgeModelID returns "<provider>:<model>" with empty parts
// dropped. Used for VerificationReport.JudgeModel.
func formatJudgeModelID(provider, model string) string {
	switch {
	case provider == "" && model == "":
		return ""
	case provider == "":
		return model
	case model == "":
		return provider
	default:
		return provider + ":" + model
	}
}

// collectJudgeText is a verifier-side trimmed version of
// loop.go's collectAssistant — we only need the text body and the
// stop reason, never tool_use. Errors surfaced through the stream
// are returned to the caller wrapped.
func collectJudgeText(stream <-chan providers.Event) (string, string, error) {
	var buf strings.Builder
	var stopReason string
	for ev := range stream {
		switch ev.Kind {
		case providers.EventTextDelta:
			buf.WriteString(ev.Text)
		case providers.EventStopReason:
			stopReason = ev.Stop
		case providers.EventError:
			return buf.String(), stopReason, ev.Err
		}
	}
	return buf.String(), stopReason, nil
}

// parseJudgeResponse extracts the JSON verdict from the judge's body.
// We tolerate the judge wrapping its JSON in a ```json fence or a few
// stray surrounding chars — but if no balanced { ... } can be found,
// we return an error so the caller can mark this as a malformed
// verdict rather than silently accepting.
func parseJudgeResponse(body string) (VerificationReport, error) {
	if body == "" {
		return VerificationReport{}, errors.New("empty body")
	}
	raw := extractJSON(body)
	if raw == "" {
		return VerificationReport{}, errors.New("no JSON object found in response")
	}
	var parsed struct {
		Score    int      `json:"score"`
		Concerns []string `json:"concerns"`
		Decision string   `json:"decision"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return VerificationReport{}, fmt.Errorf("json: %w", err)
	}
	if parsed.Score < 1 || parsed.Score > 10 {
		return VerificationReport{}, fmt.Errorf("score %d out of range 1-10", parsed.Score)
	}
	dec := VerificationDecision(strings.ToLower(strings.TrimSpace(parsed.Decision)))
	switch dec {
	case VerificationAccept, VerificationNeedsRevision, VerificationReject:
	default:
		return VerificationReport{}, fmt.Errorf("decision %q is not accept|needs_revision|reject", parsed.Decision)
	}
	return VerificationReport{
		Score:    parsed.Score,
		Concerns: parsed.Concerns,
		Decision: dec,
	}, nil
}

// extractJSON finds the first balanced JSON object substring in body.
// Counts braces; returns "" if no balanced object is found. We don't
// attempt to repair broken JSON — the judge is expected to produce
// strict JSON per the system prompt, and a parse failure is itself
// signal that the judge is misbehaving.
func extractJSON(body string) string {
	start := strings.IndexByte(body, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	inStr := false
	escaped := false
	for i := start; i < len(body); i++ {
		c := body[i]
		if inStr {
			if escaped {
				escaped = false
				continue
			}
			if c == '\\' {
				escaped = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return body[start : i+1]
			}
		}
	}
	return ""
}

// SelectJudgeProvider returns the first provider from `available`
// whose Name() differs from induceProviderName. If every available
// provider shares the inducer's name (or available is empty/nil),
// ErrNoJudgeAvailable is returned — the caller falls back to
// human-only review.
//
// Selection is "first different", not "best different". Adapters that
// want cost-aware judging (cheap judge for cheap inducer) can layer a
// preference on top of the available list before calling this.
func SelectJudgeProvider(induceProviderName string, available []providers.Provider) (providers.Provider, error) {
	for _, p := range available {
		if p == nil {
			continue
		}
		if p.Name() != induceProviderName {
			return p, nil
		}
	}
	return nil, ErrNoJudgeAvailable
}
