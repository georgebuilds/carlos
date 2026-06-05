package research

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// readSystem instructs the model to emit one JSON object per line
// (JSONL). This is materially easier to parse than a JSON array
// across providers — a malformed trailing brace breaks an array but
// leaves prior lines intact. The "Skip if nothing relevant" escape
// keeps us from forcing extractions when the source is genuinely
// off-topic.
const readSystem = `You extract relevant passages from web sources. Output each passage as one JSON object on its own line: {"text": "<verbatim or near-verbatim excerpt>", "relevance": <1-10 integer>}. Output ONLY JSON lines, no surrounding prose. If the source has nothing relevant, output nothing.`

// readUserTemplate composes the per-source prompt. %s = sub-query
// (which sub-query motivated this fetch); %s = source URL; %s =
// source content.
const readUserTemplate = `Given this source content, extract 1-3 passages most relevant to: %s

Source URL: %s

Source content:
%s

Output JSON lines now.`

// runRead walks every fetched Source and asks the LLM for up to 3
// relevant passages, scored 1-10. Passage IDs ("p1", "p2", …) are
// assigned globally across all sources in extraction order so the
// synthesis prompt has a flat list of pN handles to cite.
//
// Source-content truncation: we send at most readSourceCharCap chars
// of body to the model. This keeps any single read call within sane
// bounds when a source weighs in at the 256 KiB extracted-text
// ceiling.
//
// Per-source read errors are recorded in Concerns and the source is
// skipped (no passages from it). The read phase succeeds as long as
// AT LEAST ONE source yielded a passage; otherwise it fails so the
// synthesize phase doesn't try to write a report from nothing.
func (e *Engine) runRead(ctx context.Context, report *Report) (err error) {
	t0 := e.beginPhase("read")
	defer func() { e.endPhase("read", t0, err) }()

	if len(report.Sources) == 0 {
		return fmt.Errorf("no sources to read")
	}
	// Pair each source with the first sub-query that found it so the
	// model has a focus. For v0 the pairing is approximate — we use
	// the first sub-query for every source since fetch doesn't carry
	// the back-reference. Good enough for v0; the model still sees
	// the original question via the source content.
	primaryFocus := report.Question
	if len(report.Query.Sub) > 0 {
		primaryFocus = report.Query.Sub[0]
	}

	nextID := 1
	for _, src := range report.Sources {
		if err := ctx.Err(); err != nil {
			return err
		}
		body := truncateForRead(src.Content)
		user := fmt.Sprintf(readUserTemplate, primaryFocus, src.URL, body)
		out, err := e.callProvider(ctx, report, readSystem, user)
		if err != nil {
			// Budget exceeded mid-read is graceful — return the
			// partial set, mark the concern.
			if errors.Is(err, ErrBudgetExceeded) {
				return err
			}
			report.Concerns = append(report.Concerns,
				fmt.Sprintf("read: source=%s: %v", src.ID, err))
			continue
		}
		passages := parseReadJSONL(out, src.ID)
		for _, p := range passages {
			p.ID = fmt.Sprintf("p%d", nextID)
			nextID++
			report.Passages = append(report.Passages, p)
		}
	}
	if len(report.Passages) == 0 {
		report.Concerns = append(report.Concerns,
			"read: no passages extracted from any source")
		return fmt.Errorf("no passages extracted")
	}
	return nil
}

// readSourceCharCap bounds how much of a Source.Content we feed to
// the model per read call. The web_fetch tool already caps extracted
// text at 256 KiB; we re-cap at 32 KiB per source so several sources
// at a time don't blow a small model's context. Chosen conservatively
// — relevance lives at the top of most pages anyway.
const readSourceCharCap = 32 * 1024

func truncateForRead(s string) string {
	if len(s) <= readSourceCharCap {
		return s
	}
	return s[:readSourceCharCap] + "\n\n(... source truncated for read phase)"
}

// parseReadJSONL walks the JSONL output. Each non-empty line is
// expected to be a {"text":…,"relevance":…} object. Lines that fail
// to parse are silently skipped — the model occasionally emits a
// blank line or a stray prose comment despite the prompt, and we
// don't want one bad line to nuke a whole source's passages.
//
// Empty text is dropped (no point citing it). Relevance is clamped
// to [1, 10]; a model that hallucinates 11 doesn't get to skew the
// scale.
func parseReadJSONL(body, sourceID string) []Passage {
	var out []Passage
	for _, line := range strings.Split(body, "\n") {
		s := strings.TrimSpace(line)
		if s == "" {
			continue
		}
		// Tolerate code-fence wrapping (rare but seen).
		if strings.HasPrefix(s, "```") {
			continue
		}
		var p struct {
			Text      string `json:"text"`
			Relevance int    `json:"relevance"`
		}
		if err := json.Unmarshal([]byte(s), &p); err != nil {
			continue
		}
		txt := strings.TrimSpace(p.Text)
		if txt == "" {
			continue
		}
		rel := p.Relevance
		if rel < 1 {
			rel = 1
		}
		if rel > 10 {
			rel = 10
		}
		out = append(out, Passage{
			SourceID:  sourceID,
			Text:      txt,
			Relevance: rel,
		})
	}
	return out
}
