// library.go — load the active skill set from disk.
//
// Per SPEC § Skill model § Search paths, carlos always loads from
// FIVE directories (in this priority order; later wins on `name`):
//
//  1. ~/.claude/skills/        user-level, Claude convention
//  2. ~/.agents/skills/        user-level, open standard
//  3. <projectRoot>/.claude/skills/   project-level, Claude convention
//  4. <projectRoot>/.agents/skills/   project-level, open standard
//  5. ~/.carlos/skills/        carlos-native (legacy / hand-authored)
//
// The user's `cfg.Skills.Convention` does NOT change WHAT gets read —
// only where carlos WRITES new skills. Everyone's existing library is
// always picked up.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/georgebuilds/carlos/internal/config"
)

// Library is the in-memory active skill set. Active holds every loaded
// skill (dedup applied); Roots remembers the directories we walked.
// Drafts is reserved for a future slice that surfaces _proposals/
// directly in the library shape (today proposals live in the artifact
// store + approval queue, not a directory).
//
// NOTE on the field shape: pre-Phase-6 stub used `Active []Skill`
// (values); we switched to `[]*Skill` because the curator mutates
// Status / Updated in place and downstream callers expect the changes
// to be visible without a re-load. The exported `Root string` from the
// old stub is preserved as Roots []string — every consumer of the old
// stub was internal/agent/agent.go which only references the *Library
// type, not its fields.
type Library struct {
	Roots  []string
	Active []*Skill
	Drafts []*Skill
}

// NewLibrary returns an empty library with no roots.
func NewLibrary() *Library {
	return &Library{}
}

// ByName returns the first active skill with the given name, or nil.
// O(n); fine at carlos scale (hundreds of skills, not thousands).
func (l *Library) ByName(name string) *Skill {
	for _, s := range l.Active {
		if s != nil && s.Name == name {
			return s
		}
	}
	return nil
}

// Descriptions returns the description field of every active skill in
// load order. Convenience for the inducer (which passes existing
// descriptions as "don't propose anything that overlaps these").
func (l *Library) Descriptions() []string {
	out := make([]string, 0, len(l.Active))
	for _, s := range l.Active {
		if s != nil {
			out = append(out, s.Description)
		}
	}
	return out
}

// LoadLibrary walks each rootDir, treats every subdirectory that
// contains a SKILL.md file as one skill, and returns the deduplicated
// active set. Later roots shadow earlier ones on `name` collision
// (SPEC § Skill model: project shadows user).
//
// Roots that don't exist are silently skipped — a user who has never
// created any project-level skills shouldn't see an error. Roots that
// exist but contain malformed SKILL.md files cause LoadLibrary to
// continue past the broken entry; the returned error (if any) is the
// first hard read failure. We log nothing — callers decide whether to
// surface partial-load diagnostics.
func LoadLibrary(rootDirs []string) (*Library, error) {
	lib := &Library{Roots: append([]string(nil), rootDirs...)}
	byName := map[string]int{} // name → index in lib.Active

	for _, root := range rootDirs {
		if root == "" {
			continue
		}
		info, err := os.Stat(root)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return lib, fmt.Errorf("skills: stat %s: %w", root, err)
		}
		if !info.IsDir() {
			continue
		}

		entries, err := os.ReadDir(root)
		if err != nil {
			return lib, fmt.Errorf("skills: readdir %s: %w", root, err)
		}
		// Deterministic order so debugging is stable.
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })

		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			// Skip well-known special directories.
			switch e.Name() {
			case "_proposals", "_archive":
				continue
			}
			skillDir := filepath.Join(root, e.Name())
			// Only treat as a skill if SKILL.md exists.
			if _, err := os.Stat(filepath.Join(skillDir, skillMarkdownFile)); err != nil {
				continue
			}
			s, err := LoadSkill(skillDir)
			if err != nil {
				// Skip but keep loading. A bad SKILL.md should not nuke
				// the whole library.
				continue
			}
			if idx, ok := byName[s.Name]; ok {
				// Later wins (project shadows user).
				lib.Active[idx] = s
				continue
			}
			byName[s.Name] = len(lib.Active)
			lib.Active = append(lib.Active, s)
		}
	}
	return lib, nil
}

// LoadFromConfig resolves the 5 SPEC search paths against the user's
// home dir + projectRoot and calls LoadLibrary. The cfg's
// Skills.Convention is intentionally NOT consulted — it governs writes,
// not reads. Pass projectRoot="" to skip the project-level paths
// (useful for the daemon or for unscoped CLI commands).
func LoadFromConfig(cfg *config.Config, projectRoot string) (*Library, error) {
	_ = cfg // reserved: future "skip this path" flags can be added here
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	roots := DefaultSearchPaths(home, projectRoot)
	return LoadLibrary(roots)
}

// DefaultSearchPaths returns the 5 SPEC paths in priority order. Empty
// strings (missing home or projectRoot) are filtered out by LoadLibrary
// so callers don't have to guard.
func DefaultSearchPaths(home, projectRoot string) []string {
	var out []string
	if home != "" {
		out = append(out, filepath.Join(home, ".claude", "skills"))
		out = append(out, filepath.Join(home, ".agents", "skills"))
	}
	if projectRoot != "" {
		out = append(out, filepath.Join(projectRoot, ".claude", "skills"))
		out = append(out, filepath.Join(projectRoot, ".agents", "skills"))
	}
	if home != "" {
		out = append(out, filepath.Join(home, ".carlos", "skills"))
	}
	return out
}

// WriteRoot returns the absolute directory carlos should write NEW
// skills to, based on the user's convention preference. Project-local
// if projectRoot is non-empty; otherwise user-level.
//
// Per SPEC § Skill model § Convention paths: this is the ONLY place
// where SkillsConfig.Convention takes effect.
func WriteRoot(cfg *config.Config, home, projectRoot string) string {
	convention := config.DefaultSkillsConvention
	if cfg != nil && cfg.Skills.Convention != "" {
		convention = cfg.Skills.Convention
	}
	var subdir string
	switch convention {
	case config.SkillsConventionClaude:
		subdir = filepath.Join(".claude", "skills")
	default:
		subdir = filepath.Join(".agents", "skills")
	}
	if projectRoot != "" {
		return filepath.Join(projectRoot, subdir)
	}
	if home != "" {
		return filepath.Join(home, subdir)
	}
	return subdir
}
