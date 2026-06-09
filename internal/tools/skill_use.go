package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/georgebuilds/carlos/internal/skills"
)

// SkillUseTool exposes the loaded skill library so the model can
// retrieve a skill's body (the markdown instructions following the
// frontmatter) and follow it. Without this seam the sysprompt's
// "Available skills" block was a dead-end pointer — the model knew
// names + descriptions but had no way to actually run them.
//
// Two modes:
//
//   - input {"name": "calendar-caldav"} → returns the full body of
//     the named skill so the model can read + follow the
//     instructions. This is the read-and-do path the calendar /
//     scheduling flow uses.
//   - input {} or {"name": ""} → returns the catalog of available
//     skills (name + description) for the active frame. Same shape
//     as the /skills list slash echo. Useful when the model wants
//     to discover what's wired before committing.
//
// Auto-approved via DefaultBuiltinAllow because reading a skill is
// pure: no network egress, no file mutation. The skill body may
// then instruct the model to call other (gated) tools; those still
// route through the approver as usual.
type SkillUseTool struct {
	lib   *skills.Library
	frame string
}

// NewSkillUseTool wires the tool to a loaded library + the active
// frame's name (used to scope the catalog list via Library.ForFrame).
// A nil library makes Execute return a friendly "no skills loaded"
// error so test/dev-aid runs without a library don't crash.
func NewSkillUseTool(lib *skills.Library, frame string) *SkillUseTool {
	return &SkillUseTool{lib: lib, frame: frame}
}

func (*SkillUseTool) Name() string { return "skill_use" }

func (*SkillUseTool) Description() string {
	return "Load a skill's full body (instructions) so you can follow it. Skills are runbooks for capability-shaped requests like calendars, code review, daily digests. Call with {\"name\": \"<skill-name>\"} to get the body, or {} to list every skill available in the active frame. The names + one-line descriptions also appear in your system prompt; this tool fetches the actual body when you decide to act."
}

func (*SkillUseTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"name": {
				"type": "string",
				"description": "Skill name (e.g. \"calendar-caldav\"). Omit or pass empty to list every available skill in the active frame."
			}
		}
	}`)
}

type skillUseInput struct {
	Name string `json:"name"`
}

// skillEntry is the shape returned for the list mode + the no-match
// fall-back. Kept tiny so the JSON tool result the model sees is
// scannable in a single render.
type skillEntry struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Backend     string `json:"backend,omitempty"`
}

type skillUseListResponse struct {
	Frame  string       `json:"frame,omitempty"`
	Skills []skillEntry `json:"skills"`
}

type skillUseBodyResponse struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Backend     string `json:"backend,omitempty"`
	Body        string `json:"body"`
}

// Execute parses the optional name and either returns the catalog
// or the requested skill's body. Errors are returned as a
// tool-error envelope so the chat layer surfaces them as a warn
// card instead of crashing the loop.
func (t *SkillUseTool) Execute(_ context.Context, input []byte) ([]byte, error) {
	var in skillUseInput
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return nil, fmt.Errorf("skill_use: parse input: %w", err)
		}
	}
	if t.lib == nil {
		return nil, errors.New("skill_use: no skill library loaded for this session")
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return t.list()
	}
	s := t.lookup(name)
	if s == nil {
		// Surface the catalog alongside the not-found error so the
		// model can self-correct: maybe they typed the wrong name.
		// Marshal both branches as a structured tool error.
		avail := t.availableNames()
		return nil, fmt.Errorf("skill_use: skill %q not found in this frame; available: %s", name, strings.Join(avail, ", "))
	}
	resp := skillUseBodyResponse{
		Name:        s.Name,
		Description: s.Description,
		Backend:     s.Backend,
		Body:        strings.TrimSpace(s.Body),
	}
	return json.Marshal(resp)
}

func (t *SkillUseTool) list() ([]byte, error) {
	out := skillUseListResponse{Frame: t.frame}
	for _, s := range t.lib.ForFrame(t.frame) {
		if s == nil {
			continue
		}
		out.Skills = append(out.Skills, skillEntry{
			Name:        s.Name,
			Description: s.Description,
			Backend:     s.Backend,
		})
	}
	sort.Slice(out.Skills, func(i, j int) bool {
		return out.Skills[i].Name < out.Skills[j].Name
	})
	return json.Marshal(out)
}

func (t *SkillUseTool) lookup(name string) *skills.Skill {
	for _, s := range t.lib.ForFrame(t.frame) {
		if s != nil && s.Name == name {
			return s
		}
	}
	return nil
}

func (t *SkillUseTool) availableNames() []string {
	names := make([]string, 0)
	for _, s := range t.lib.ForFrame(t.frame) {
		if s != nil {
			names = append(names, s.Name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return []string{"(none)"}
	}
	return names
}
