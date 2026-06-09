// bundled.go - embedded starter skills.
//
// The repo's `skills/` directory ships in the binary via go:embed.
// Without this, users who installed carlos via brew or a tarball
// would never see the bundled calendar / starter skills — the
// skill loader only walked on-disk paths, and a fresh install has
// nothing under ~/.carlos/skills/.
//
// The embedded set is merged into Library LAST (after the five
// SPEC disk paths) so a user-installed skill with the same Name
// still shadows the bundled version. That preserves the "user
// installs override built-ins" rule without forcing users to
// hand-copy files just to get the starter pack.

package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"

	"github.com/georgebuilds/carlos/internal/miniyaml"
)

//go:embed bundled/*
var bundledFS embed.FS

// BundledRoot is the synthetic "Path" we tag every bundled skill
// with so callers can recognize them. The chat surface uses this
// to label skills as "(bundled)" in /skills list output.
const BundledRoot = "<embedded>"

// LoadBundled walks the embedded skill bundles and returns the
// flattened []*Skill list. The embedded layout mirrors the on-disk
// bundle format (one .md file per skill, frontmatter required).
// Returns an empty slice (not nil) when the embed has no files so
// the merge step is uniform.
//
// Failures decoding any one skill are skipped (not returned) so a
// malformed embed doesn't nuke the whole bundle. The caller can't
// fix a binary-embedded skill anyway; surfacing the error would
// just be noise.
func LoadBundled() []*Skill {
	out := []*Skill{}
	// The embed root is "bundled/" — walk one directory deeper to
	// find each <namespace>/*.md file (matching the on-disk
	// loadSkillsAt bundle layout).
	entries, err := bundledFS.ReadDir("bundled")
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		bundle := path.Join("bundled", e.Name())
		out = append(out, loadBundledBundle(bundle)...)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// loadBundledBundle reads every *.md file under bundleDir from the
// embedded FS. Skips files that don't carry frontmatter, that
// can't be unmarshalled, or that fail Validate — same defensive
// posture as the on-disk loader.
func loadBundledBundle(bundleDir string) []*Skill {
	entries, err := bundledFS.ReadDir(bundleDir)
	if err != nil {
		return nil
	}
	var out []*Skill
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) != ".md" {
			continue
		}
		raw, err := fs.ReadFile(bundledFS, path.Join(bundleDir, e.Name()))
		if err != nil {
			continue
		}
		s, err := decodeBundleSkillBytes(raw)
		if err != nil {
			continue
		}
		// Tag the on-disk Path as the synthetic bundled root + the
		// virtual path so /skills list can show provenance.
		s.Path = BundledRoot + "/" + bundleDir + "/" + e.Name()
		out = append(out, s)
	}
	return out
}

// decodeBundleSkillBytes is the embed-friendly twin of
// LoadBundleSkill: takes raw bytes (no filesystem touch) and returns
// a populated *Skill or an error. Pulled out so the embed loader
// and tests can exercise the parse rules without touching disk.
func decodeBundleSkillBytes(raw []byte) (*Skill, error) {
	fm, body, found, err := miniyaml.SplitFrontmatter(raw)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("skills: embedded skill missing frontmatter")
	}
	var s Skill
	if err := miniyaml.UnmarshalStruct(fm, &s); err != nil {
		return nil, fmt.Errorf("skills: embedded yaml: %w", err)
	}
	s.Body = string(body)
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("skills: embedded validate: %w", err)
	}
	return &s, nil
}
