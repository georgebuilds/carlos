// curator.go - staleness sweep over the active skill library.
//
// # Lifecycle (SPEC § Skill induction § Instrumentation)
//
//   - active   - default. LastUsed within 30 days (or never used but
//                still within its own 30-day grace window since
//                Created).
//   - stale    - no use in 30 days. Still in the library; description
//                still in the startup index, BUT the SKILL.md
//                frontmatter is marked status: stale so the inducer's
//                "existing descriptions" prompt can deprioritize.
//   - archived - no use in 90 days (counted from LastUsed; if never
//                used, from Created). Directory moves to
//                <root>/_archive/<name>/, removed from active rotation.
//
// Never hard-delete - archived directories are restorable, and the
// telemetry slice will compute survival curves from them.
//
// # Clock seam
//
// The sweep takes an explicit time.Time so tests can simulate a future
// clock without sleeping. Production callers pass time.Now().UTC().
package skills

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Default staleness thresholds per SPEC § Instrumentation.
const (
	DefaultStaleAfter   = 30 * 24 * time.Hour
	DefaultArchiveAfter = 90 * 24 * time.Hour
	archiveSubdirectory = "_archive"
)

// Curator runs lifecycle transitions. Cadence is a caller concern
// (carlos's supervisor scheduler will tick this; tests call SweepOnce
// directly). The struct is configuration only - no state lives here.
type Curator struct {
	StaleAfter   time.Duration
	ArchiveAfter time.Duration
}

// NewCurator returns a Curator with default thresholds.
func NewCurator() *Curator {
	return &Curator{
		StaleAfter:   DefaultStaleAfter,
		ArchiveAfter: DefaultArchiveAfter,
	}
}

// SweepReport summarizes one pass. Counts are post-sweep totals: how
// many skills now sit in each bucket.
type SweepReport struct {
	Active      int
	Stale       int
	Archived    int
	Transitions []SweepTransition
}

// SweepTransition records one skill's bucket change during the sweep.
// Useful for the event log so post-hoc dashboards can replay the
// lifecycle.
type SweepTransition struct {
	SkillName string
	From      Status
	To        Status
	Path      string // skill dir AFTER the transition (relevant for archive moves)
}

// SweepOnce runs a single sweep over lib. now is the wall-clock
// reference (UTC); tests pass a fixed time to drive transitions
// deterministically.
//
// Mutations:
//   - active → stale: rewrite SKILL.md frontmatter with status: stale.
//     The skill stays in lib.Active so callers don't have to reload.
//   - stale → archived: move the entire skill directory to
//     <root>/_archive/<name>/. The skill is REMOVED from lib.Active
//     and an entry appears in the returned transitions list.
//
// Errors during one skill don't abort the sweep - the per-skill error
// is wrapped into the SweepReport's first-error return (callers can
// surface it via event log). A nil error means "every skill processed
// cleanly."
func (c *Curator) SweepOnce(ctx context.Context, lib *Library, now time.Time) (*SweepReport, error) {
	if lib == nil {
		return nil, errors.New("curator: nil library")
	}
	staleAfter := c.StaleAfter
	if staleAfter <= 0 {
		staleAfter = DefaultStaleAfter
	}
	archiveAfter := c.ArchiveAfter
	if archiveAfter <= 0 {
		archiveAfter = DefaultArchiveAfter
	}

	report := &SweepReport{}
	var firstErr error
	survivors := make([]*Skill, 0, len(lib.Active))

	for _, s := range lib.Active {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if s == nil {
			continue
		}
		age := skillIdleAge(s, now)
		current := s.Status
		if current == "" {
			current = StatusActive
		}

		switch {
		case age >= archiveAfter && current != StatusArchived:
			// Move to <skillRoot>/_archive/<name>/ - preserves
			// telemetry and is restorable.
			newPath, moveErr := archiveSkill(s)
			if moveErr != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("curator: archive %q: %w", s.Name, moveErr)
				}
				// Keep in library; caller will see the error.
				survivors = append(survivors, s)
				report.Active++
				continue
			}
			s.Status = StatusArchived
			s.Path = newPath
			report.Transitions = append(report.Transitions, SweepTransition{
				SkillName: s.Name,
				From:      current,
				To:        StatusArchived,
				Path:      newPath,
			})
			report.Archived++
			// Drop from active set.

		case age >= staleAfter && current == StatusActive:
			// Mark stale: rewrite SKILL.md frontmatter.
			s.Status = StatusStale
			s.Updated = now
			if err := WriteSkill(s.Path, s); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("curator: mark stale %q: %w", s.Name, err)
				}
			}
			report.Transitions = append(report.Transitions, SweepTransition{
				SkillName: s.Name,
				From:      current,
				To:        StatusStale,
				Path:      s.Path,
			})
			report.Stale++
			survivors = append(survivors, s)

		default:
			// No transition; tally by current bucket.
			switch current {
			case StatusStale:
				report.Stale++
			case StatusArchived:
				report.Archived++
			default:
				report.Active++
			}
			survivors = append(survivors, s)
		}
	}
	lib.Active = survivors
	return report, firstErr
}

// skillIdleAge returns how long the skill has been "idle" - time since
// LastUsed if set, else time since Created. This is the metric both
// staleness thresholds compare against.
func skillIdleAge(s *Skill, now time.Time) time.Duration {
	ref := s.Created
	if s.LastUsed != nil && !s.LastUsed.IsZero() {
		ref = *s.LastUsed
	}
	if ref.IsZero() {
		// Defensive: a skill with no Created and no LastUsed is treated
		// as fresh (zero age), so we don't accidentally archive
		// hand-crafted skills whose frontmatter omits timestamps.
		return 0
	}
	if now.Before(ref) {
		return 0
	}
	return now.Sub(ref)
}

// archiveSkill moves s.Path to its parent's _archive/<basename>/ dir.
// Returns the new path. If the destination already exists, returns an
// error - never overwrite an existing archive entry (a duplicate
// transition is a bug worth surfacing).
func archiveSkill(s *Skill) (string, error) {
	if s == nil || s.Path == "" {
		return "", errors.New("archiveSkill: missing path")
	}
	parent := filepath.Dir(s.Path)
	base := filepath.Base(s.Path)
	archDir := filepath.Join(parent, archiveSubdirectory)
	if err := os.MkdirAll(archDir, 0o700); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", archDir, err)
	}
	dest := filepath.Join(archDir, base)
	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("%s already exists; refusing to overwrite", dest)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat %s: %w", dest, err)
	}
	if err := os.Rename(s.Path, dest); err != nil {
		return "", fmt.Errorf("rename %s -> %s: %w", s.Path, dest, err)
	}
	return dest, nil
}
