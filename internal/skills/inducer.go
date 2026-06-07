// inducer.go - single-call online skill inducer.
//
// # Contract
//
// Given a summarized transcript + the descriptions of every already-
// active skill, the inducer makes ONE provider call that either:
//
//   - Returns a Proposal with name + description + body, OR
//   - Returns nil, nil - meaning "this is not a generalizable skill".
//
// The inducer is intentionally a single call. Multi-step refinement
// loops were ruled out by SkillsBench's finding that elaborated self-
// generated skills don't outperform simple ones; the cost ceiling
// (~$0.02/proposal per DESIGN § Cost model) only holds for one call.
//
// # Prompt template (load-bearing)
//
// The template below is embedded verbatim in the package godoc so it
// shows up in `go doc` and in PR diffs without anyone needing to grep.
// It encodes the four hard constraints from the decisions doc:
//
//  1. Induce ONLY if reusable. Return null if not.
//  2. Output must follow agentskills.io SKILL.md frontmatter shape.
//  3. Description must contain explicit "Use when ..." trigger
//     conditions (Anthropic skill spec; the description is the
//     load-bearing always-resident field).
//  4. Body prefers executable scripts over prose for deterministic
//     sub-tasks (SPEC § Skill model).
//
// # Cost expectation
//
// Per call (mid-tier model ~$3 in / $15 out per 1M tokens, June 2026):
// ~3,000 input tokens (summarized transcript + top-k descriptions +
// template) + ~600 output tokens ≈ $0.02. The DESIGN cost model is the
// authoritative source - keep this in sync.
package skills

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/georgebuilds/carlos/internal/providers"
)

// InducerPromptTemplate is the system prompt sent to the inducer. The
// template embeds two placeholders: {{TRANSCRIPT}} (the conversation
// summary) and {{EXISTING}} (a newline-bulleted list of existing skill
// descriptions). They are substituted via plain string.Replace - no
// text/template engine, no escaping concerns (the inputs are
// already-trusted strings owned by carlos).
//
// Reviewers: this prompt is the single source of truth for what the
// model is asked to produce. Edits here are user-visible behavior
// changes; treat them as you would a code change to a hot path.
const InducerPromptTemplate = `You are carlos's skill-induction subsystem. You watch a finished, verified-successful conversation and decide whether the agent learned a reusable PROCEDURE worth saving as a skill.

A skill is reusable iff a future agent doing a similar task would benefit from following the same steps. NOT a skill: one-off answers, single-source lookups, anything completable in 1-2 reasoning turns, restatements of general knowledge.

Output FORMAT: a single JSON object. Either:
  {"skill": null, "reason": "<one line on why not reusable>"}
OR:
  {"skill": {
    "name": "<kebab-case, 3-6 words, action-oriented>",
    "description": "<one line; MUST start with 'Use when ' to encode the trigger; <=200 chars>",
    "body": "<markdown procedure; prefer executable scripts over prose for deterministic sub-tasks; <=4000 chars>"
  }}

HARD RULES:
  - Output ONLY the JSON object. No code fence, no commentary, no preamble.
  - "Use when ..." in the description is non-negotiable - it is the trigger that retrieval matches against.
  - If the procedure overlaps with an existing skill below, return null. Do not produce near-duplicates.
  - Bodies prefer "run this script" over "follow these prose steps" when the sub-task is deterministic.

EXISTING SKILLS (do not duplicate; descriptions only):
{{EXISTING}}

CONVERSATION SUMMARY:
{{TRANSCRIPT}}
`

// Proposal is the structured output of a single Induce call. ID is the
// SHA-derived dedup handle the caller mints; InducedFrom is the list
// of conversation/agent IDs that fed the summary. Created is set by
// the inducer at call time.
type Proposal struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Body        string    `json:"body"`
	InducerName string    `json:"inducer"` // provider name + model, "anthropic:claude-3-5-sonnet"
	InducedFrom []string  `json:"induced_from"`
	Created     time.Time `json:"created"`
	// RawResponse is the unparsed model output - preserved so the
	// approval-queue UX can show "what the model actually said" if the
	// user wants to debug a reject decision.
	RawResponse string `json:"raw_response,omitempty"`
}

// IntoSkill converts a Proposal into a fully-populated Skill ready for
// WriteSkill. judgeModel is the provider:model string of whatever
// judge scored the proposal (empty if no judge ran).
func (p *Proposal) IntoSkill(judgeModel string) *Skill {
	now := time.Now().UTC()
	created := p.Created
	if created.IsZero() {
		created = now
	}
	return &Skill{
		Name:         p.Name,
		Description:  p.Description,
		Provenance:   ProvInduced,
		InducedFrom:  append([]string(nil), p.InducedFrom...),
		InducerModel: p.InducerName,
		JudgeModel:   judgeModel,
		Created:      created,
		Updated:      now,
		Body:         p.Body,
	}
}

// modelOutput is the JSON shape the inducer prompt asks for. The outer
// `skill` key is nullable; absent / null => not reusable.
type modelOutput struct {
	Skill *struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Body        string `json:"body"`
	} `json:"skill"`
	Reason string `json:"reason,omitempty"`
}

// InducerOptions controls per-call knobs the caller cares about.
// All fields are optional; zero values are sane.
type InducerOptions struct {
	// Model overrides the provider's default model selection. Empty =
	// provider picks.
	Model string
	// InducedFrom is forwarded to the resulting Proposal so downstream
	// has the lineage. Typically a list of agent IDs that fed the
	// summary.
	InducedFrom []string
}

// Inducer wraps a Provider with the canned prompt. Construct one per
// call site; the struct holds no per-call state.
type Inducer struct {
	Provider providers.Provider
}

// NewInducer is a convenience.
func NewInducer(p providers.Provider) *Inducer {
	return &Inducer{Provider: p}
}

// Induce runs the single-call inducer. transcript is a free-form
// summary of the conversation; existingDescriptions is the
// already-active skill set (used in the dedup-prevention block of the
// prompt). Returns (nil, nil) when the model judges the conversation
// not-reusable - this is the expected common case, NOT an error.
func (i *Inducer) Induce(ctx context.Context, transcript string, existingDescriptions []string, opts InducerOptions) (*Proposal, error) {
	if i == nil || i.Provider == nil {
		return nil, errors.New("inducer: nil provider")
	}
	if strings.TrimSpace(transcript) == "" {
		return nil, errors.New("inducer: empty transcript")
	}

	system := buildInducerPrompt(transcript, existingDescriptions)
	req := providers.Request{
		Model:  opts.Model,
		System: system,
		Messages: []providers.Message{
			{
				Role:    "user",
				Content: []providers.Block{{Kind: "text", Text: "Induce."}},
			},
		},
	}

	raw, err := streamText(ctx, i.Provider, req)
	if err != nil {
		return nil, fmt.Errorf("inducer: stream: %w", err)
	}

	parsed, err := parseInducerOutput(raw)
	if err != nil {
		return nil, fmt.Errorf("inducer: parse: %w (raw=%q)", err, truncateForError(raw, 200))
	}
	if parsed == nil {
		return nil, nil
	}

	parsed.InducerName = providerLabel(i.Provider, opts.Model)
	parsed.InducedFrom = append([]string(nil), opts.InducedFrom...)
	parsed.Created = time.Now().UTC()
	parsed.RawResponse = raw
	return parsed, nil
}

// buildInducerPrompt substitutes the two placeholders. existing may be
// empty (cold-start library) - we render an explicit "(none)" so the
// model doesn't see a stray empty bullet.
func buildInducerPrompt(transcript string, existing []string) string {
	var existingBlock string
	if len(existing) == 0 {
		existingBlock = "  (none)"
	} else {
		var sb strings.Builder
		for _, d := range existing {
			sb.WriteString("  - ")
			sb.WriteString(d)
			sb.WriteByte('\n')
		}
		existingBlock = strings.TrimRight(sb.String(), "\n")
	}
	out := strings.ReplaceAll(InducerPromptTemplate, "{{EXISTING}}", existingBlock)
	out = strings.ReplaceAll(out, "{{TRANSCRIPT}}", strings.TrimSpace(transcript))
	return out
}

// parseInducerOutput tolerates: a raw JSON object, a JSON object
// wrapped in a code fence, or leading/trailing whitespace. Anything
// else is a parse error. Returns nil, nil for `{"skill": null}`.
func parseInducerOutput(raw string) (*Proposal, error) {
	s := strings.TrimSpace(raw)
	s = stripCodeFence(s)
	if s == "" {
		return nil, errors.New("empty response")
	}
	var out modelOutput
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil, err
	}
	if out.Skill == nil {
		return nil, nil
	}
	name := strings.TrimSpace(out.Skill.Name)
	desc := strings.TrimSpace(out.Skill.Description)
	body := out.Skill.Body
	if name == "" || desc == "" {
		return nil, errors.New("skill missing name or description")
	}
	return &Proposal{
		Name:        name,
		Description: desc,
		Body:        body,
	}, nil
}

// stripCodeFence pulls the body out of a ```json ... ``` fence if the
// model wrapped its output despite our instructions. Generous: any
// triple-backtick fenced block returns the inner text.
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	// Drop the first line (the opening fence with optional language tag).
	nl := strings.IndexByte(s, '\n')
	if nl < 0 {
		return s
	}
	rest := s[nl+1:]
	// Find the closing fence.
	end := strings.LastIndex(rest, "```")
	if end < 0 {
		return rest
	}
	return strings.TrimSpace(rest[:end])
}

// streamText drains a provider stream into a single string. We discard
// tool-use events - the inducer prompt forbids tool calls; if the
// model emits one anyway, we treat the conversation as a parse miss.
func streamText(ctx context.Context, p providers.Provider, req providers.Request) (string, error) {
	ch, err := p.Stream(ctx, req)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for ev := range ch {
		switch ev.Kind {
		case providers.EventTextDelta:
			sb.WriteString(ev.Text)
		case providers.EventError:
			return "", ev.Err
		}
	}
	return sb.String(), nil
}

// providerLabel produces a "<provider>:<model>" tag for the Proposal's
// inducer field. If model is empty (provider default), we just use the
// provider name.
func providerLabel(p providers.Provider, model string) string {
	if p == nil {
		return model
	}
	name := p.Name()
	if model == "" {
		return name
	}
	return name + ":" + model
}

// truncateForError returns at most n chars of s, with an ellipsis if
// truncated. Used to keep parse-error messages short.
func truncateForError(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
