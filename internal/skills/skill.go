// Package skills implements carlos's skill-induction subsystem per
// SPEC § Skill model + § Skill induction.
//
// # Architectural commitments (load-bearing — see vault note
// "2026-06-04 Skill Induction — decisions adopted")
//
//   - Propose, don't publish. Every induced skill is a PROPOSAL until it
//     passes the human gate in the 4h approval queue. There is no path
//     from "model decided" to "skill is live" — see SPEC § Skill
//     induction § Headline.
//   - agentskills.io SKILL.md format. Directory layout with YAML
//     frontmatter + markdown body, optional scripts/ + reference/.
//     Adopted across Claude / OpenAI Codex CLI / Gemini CLI / Copilot /
//     Cursor in December 2025; provider-neutral on purpose.
//   - Description-embedding retrieval (Voyager pattern). The description
//     is the load-bearing field; cosine over description embeddings drives
//     top-k. Bodies load progressively only on relevance match.
//   - Conjunctive online trigger (see trigger.go).
//   - Cross-provider judging (see judge.go).
//   - Curator pattern: active → stale (30d unused) → archived (90d),
//     never hard-deleted (see curator.go).
//
// # Module split (DESIGN § Skill induction § Module split)
//
//   - skill.go       — Skill struct + SKILL.md parse/write (this file)
//   - library.go     — load library from convention paths, dedup
//   - index.go       — description-embedding index + top-k retrieval
//   - trigger.go     — pure conjunctive trigger evaluator
//   - inducer.go     — single-call inducer (LLM)
//   - judge.go       — cross-provider triage judge (LLM)
//   - curator.go     — staleness/archive sweep
//   - metrics.go     — instrumentation: acceptance, reuse, survival
//   - skillwire/wire.go — agent/event-log integration: Propose+Promote
//
// The skillwire/ sub-package exists to avoid an import cycle: agent
// imports skills (for the Library type held in agent.Config), so the
// integration code that imports agent lives one level deeper.
package skills

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/georgebuilds/carlos/internal/miniyaml"
)

// Provenance values for the SKILL.md frontmatter `provenance` field.
type Provenance string

const (
	ProvInduced     Provenance = "induced"
	ProvHandWritten Provenance = "hand-written"
	ProvImported    Provenance = "imported"
)

// Status values for the optional `status` field. Active is the default
// (omitted from frontmatter); stale and archived are set by the curator.
type Status string

const (
	StatusActive   Status = "active"
	StatusStale    Status = "stale"
	StatusArchived Status = "archived"
)

// Body / directory caps per SPEC § Skill model.
// 5,000 tokens — the directory cap is 500 lines per agentskills.io. We
// also cap raw file count per directory at ~50 as a defensive guard
// against a malformed skill exploding the loader. The token cap is
// approximated by character count: ~4 chars/token average → 20_000 chars
// is the rough proxy.
const (
	MaxBodyChars      = 20_000
	MaxFilesPerSkill  = 50
	skillMarkdownFile = "SKILL.md"
)

// Skill mirrors the agentskills.io SKILL.md frontmatter plus the body
// (markdown after the closing `---`). Body is NOT serialized via YAML;
// it is appended below the frontmatter at write time.
//
// Field order in YAML output is governed by the struct tag order. Keep
// this stable — it shows up in user-facing diffs in the approval queue.
type Skill struct {
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	Provenance   Provenance `json:"provenance"`
	InducedFrom  []string   `json:"induced_from,omitempty"`
	InducerModel string     `json:"inducer_model,omitempty"`
	JudgeModel   string     `json:"judge_model,omitempty"`
	Created      time.Time  `json:"created"`
	Updated      time.Time  `json:"updated"`
	ReuseCount   int        `json:"reuse_count"`
	LastUsed     *time.Time `json:"last_used,omitempty"`
	Status       Status     `json:"status,omitempty"`

	// Optional legacy / hand-authored fields kept for forward compat.
	Triggers []string `json:"triggers,omitempty"`
	Tools    []string `json:"tools,omitempty"`

	// Phase C-4: skill loader reads `backend` to pick the right
	// implementation for a capability when the user has multiple
	// backends available (e.g. calendar/ics-file.md vs
	// calendar/caldav.md). Empty means "not part of a capability bundle".
	Backend string `json:"backend,omitempty"`
	// FrameDefault names the frame this skill should bind to when the
	// user has no `capabilities.<name>.<frame>.backend` override. Empty
	// falls through to the active frame at call time.
	FrameDefault string `json:"frame_default,omitempty"`
	// Phase F-20: when set, restrict this skill to the named frames.
	// Empty means "available in every frame" (the default).
	Frames []string `json:"frames,omitempty"`

	// Body is the markdown that follows the frontmatter. Never serialized
	// by miniyaml.MarshalStruct (json:"-"); written separately by WriteSkill.
	Body string `json:"-"`

	// Path is the absolute on-disk path to the skill directory (NOT the
	// SKILL.md file). Set by LoadSkill; ignored by WriteSkill.
	Path string `json:"-"`
}

// Validate enforces SPEC caps and basic structural invariants. Called by
// WriteSkill before any bytes hit the disk; callers may call it directly
// to validate a parsed Skill.
func (s *Skill) Validate() error {
	if s == nil {
		return errors.New("skill: nil")
	}
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("skill: name required")
	}
	if strings.TrimSpace(s.Description) == "" {
		return errors.New("skill: description required (load-bearing field)")
	}
	if len(s.Body) > MaxBodyChars {
		return fmt.Errorf("skill: body length %d exceeds cap %d chars (~5000 tokens)", len(s.Body), MaxBodyChars)
	}
	return nil
}

// LoadSkill reads <dir>/SKILL.md and returns the parsed Skill. The
// returned Skill's Path is set to dir (absolute via filepath.Abs).
//
// Format: an optional YAML frontmatter block delimited by `---` lines on
// their own; if absent, the entire file is treated as the body and the
// caller-supplied Name (basename of dir) is used as a fallback. Errors
// always include the file path for diagnosability.
func LoadSkill(dir string) (*Skill, error) {
	if dir == "" {
		return nil, errors.New("skills: LoadSkill called with empty dir")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("skills: abs %s: %w", dir, err)
	}
	path := filepath.Join(absDir, skillMarkdownFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("skills: read %s: %w", path, err)
	}

	// Enforce the file-count cap as part of the loader so users can't
	// trip us on a malformed bundle of 500 reference files.
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, fmt.Errorf("skills: scan %s: %w", absDir, err)
	}
	if len(entries) > MaxFilesPerSkill {
		return nil, fmt.Errorf("skills: %s has %d top-level entries, exceeds cap %d",
			absDir, len(entries), MaxFilesPerSkill)
	}

	fm, body, found, err := miniyaml.SplitFrontmatter(raw)
	if err != nil {
		return nil, fmt.Errorf("skills: parse %s: %w", path, err)
	}

	var s Skill
	if found && len(fm) > 0 {
		if err := miniyaml.UnmarshalStruct(fm, &s); err != nil {
			return nil, fmt.Errorf("skills: yaml %s: %w", path, err)
		}
	}
	if s.Name == "" {
		s.Name = filepath.Base(absDir)
	}
	s.Body = string(body)
	s.Path = absDir

	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("skills: validate %s: %w", path, err)
	}
	return &s, nil
}

// LoadBundleSkill reads a single .md file as a skill. Used by the
// bundle-directory layout (skills/calendar/ics-file.md, …) where one
// directory hosts a namespace of related skills rather than a single
// agentskills.io-shaped skill. The file's frontmatter must carry at
// minimum `name` + `description`; absent frontmatter returns an error
// rather than guessing because bundle skills have no defensible
// fallback name (the file's basename includes the namespace prefix).
func LoadBundleSkill(filePath string) (*Skill, error) {
	if filePath == "" {
		return nil, errors.New("skills: LoadBundleSkill called with empty path")
	}
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return nil, fmt.Errorf("skills: abs %s: %w", filePath, err)
	}
	raw, err := os.ReadFile(absPath)
	if err != nil {
		return nil, fmt.Errorf("skills: read %s: %w", absPath, err)
	}
	fm, body, found, err := miniyaml.SplitFrontmatter(raw)
	if err != nil {
		return nil, fmt.Errorf("skills: parse %s: %w", absPath, err)
	}
	if !found {
		return nil, fmt.Errorf("skills: %s has no frontmatter — bundle skills require name + description", absPath)
	}
	var s Skill
	if err := miniyaml.UnmarshalStruct(fm, &s); err != nil {
		return nil, fmt.Errorf("skills: yaml %s: %w", absPath, err)
	}
	s.Body = string(body)
	s.Path = absPath
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("skills: validate %s: %w", absPath, err)
	}
	return &s, nil
}

// WriteSkill writes <dir>/SKILL.md atomically. The directory is created
// at mode 0700 if absent; the file is written at mode 0600 (matches
// config.go — skill content may reference private paths/contents).
//
// Atomic recipe: open <dir>/SKILL.md.tmp, write, fsync, rename. On any
// error mid-write the temp file is removed; the destination remains in
// its prior state (or absent).
//
// Validation runs before any IO so we never half-write an oversized
// body. The caller's *Skill is not mutated.
func WriteSkill(dir string, s *Skill) error {
	if s == nil {
		return errors.New("skills: WriteSkill called with nil skill")
	}
	if err := s.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("skills: mkdir %s: %w", dir, err)
	}
	// Enforce file-count cap on the destination dir, in case a prior
	// write left a bunch of stray files.
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > MaxFilesPerSkill {
		return fmt.Errorf("skills: %s already has %d entries, exceeds cap %d",
			dir, len(entries), MaxFilesPerSkill)
	}

	rendered, err := renderSkill(s)
	if err != nil {
		return err
	}

	path := filepath.Join(dir, skillMarkdownFile)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("skills: open tmp %s: %w", tmp, err)
	}
	if _, err := f.Write(rendered); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("skills: write tmp %s: %w", tmp, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("skills: fsync tmp %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("skills: close tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("skills: rename %s -> %s: %w", tmp, path, err)
	}
	return nil
}

// renderSkill produces the `---\n<yaml>\n---\n<body>\n` byte stream.
// Field ordering is alphabetical (miniyaml.MarshalStruct's invariant),
// which keeps the on-disk diff stable across writes.  We previously
// hand-rendered to honor the struct-tag order; that requirement was
// dropped when we moved to miniyaml because the smaller surface area
// is worth more than tag-order fidelity for a skill frontmatter that
// users rarely read top-to-bottom.
func renderSkill(s *Skill) ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString("---\n")

	data, err := miniyaml.MarshalStruct(s)
	if err != nil {
		return nil, fmt.Errorf("skills: yaml encode: %w", err)
	}
	buf.Write(data)

	buf.WriteString("---\n")
	if s.Body != "" {
		buf.WriteString(s.Body)
		if !strings.HasSuffix(s.Body, "\n") {
			buf.WriteByte('\n')
		}
	}
	return buf.Bytes(), nil
}

