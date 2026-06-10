package chat

import (
	"strings"
	"testing"

	"github.com/georgebuilds/carlos/internal/tui/slash"
)

// TestRenderHelpBox_RowsAreAlphabetical pins the user-facing rule:
// /help lists slash verbs in alphabetical order. slash.Builtins
// itself stays in curated reading order (autocomplete priority,
// status hint) — only the at-a-glance /help panel re-sorts, since
// users scan A→Z faster than they scan a curated list when looking
// up a verb.
//
// Verbs are matched by LINE INDEX (not byte offset) since some
// descriptions cross-reference other verbs (e.g. /exit's description
// mentions /quit), and a naive substring scan would hit the
// cross-reference instead of the verb's own row.
func TestRenderHelpBox_RowsAreAlphabetical(t *testing.T) {
	out := renderHelpBox(100)
	lines := strings.Split(stripANSIForTest(out), "\n")

	wantNames := make([]string, len(slash.Builtins))
	for i, b := range slash.Builtins {
		wantNames[i] = b.Name
	}
	sortStrings(wantNames)

	// Build a name → first-line-where-the-line-STARTS-with-"/<name>"
	// map so cross-references in descriptions don't fool us. Each
	// rendered row is wrapped in the box border + padding ("│ "),
	// so trim those before looking for the "/<name>" verb token.
	rowFor := make(map[string]int, len(wantNames))
	for li, ln := range lines {
		trim := strings.TrimLeft(ln, "│ ")
		if !strings.HasPrefix(trim, "/") {
			continue
		}
		body := trim[1:]
		end := strings.IndexAny(body, " ")
		if end == -1 {
			end = len(body)
		}
		name := body[:end]
		if _, dup := rowFor[name]; !dup {
			rowFor[name] = li
		}
	}

	prev := -1
	for _, name := range wantNames {
		li, ok := rowFor[name]
		if !ok {
			t.Errorf("rendered help missing row for verb %q", name)
			continue
		}
		if li <= prev {
			t.Errorf("verb %q on line %d violates alphabetical order (prev line %d)", name, li, prev)
		}
		prev = li
	}
}

// stripANSIForTest is a CSI-only stripper used by the alphabetical
// test so line-position arithmetic counts visible cells, not raw
// styling bytes. Local to the test file; production strippers (e.g.
// stripVisibleLeadingMargin) live in markdown.go.
func stripANSIForTest(s string) string {
	var out strings.Builder
	i := 0
	for i < len(s) {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && s[j] != 'm' {
				j++
			}
			if j < len(s) {
				i = j + 1
				continue
			}
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}

// TestRenderHelpBox_PreservesEveryBuiltin guards against an
// accidental drop: every verb in slash.Builtins must appear in the
// rendered help.
func TestRenderHelpBox_PreservesEveryBuiltin(t *testing.T) {
	out := renderHelpBox(100)
	for _, b := range slash.Builtins {
		if !strings.Contains(out, "/"+b.Name) {
			t.Errorf("help box missing /%s\n%s", b.Name, out)
		}
	}
}

// TestRenderHelpBox_DoesNotMutatePackageSlice guards the alphabetic
// sort against a regression that would reorder slash.Builtins in
// place — the package slice is the curated order used elsewhere
// (autocomplete, status hint) and must stay untouched.
func TestRenderHelpBox_DoesNotMutatePackageSlice(t *testing.T) {
	before := make([]string, len(slash.Builtins))
	for i, b := range slash.Builtins {
		before[i] = b.Name
	}
	_ = renderHelpBox(100)
	for i, b := range slash.Builtins {
		if b.Name != before[i] {
			t.Errorf("slash.Builtins[%d] mutated: was %q, now %q", i, before[i], b.Name)
		}
	}
}

func sortStrings(ss []string) {
	for i := 1; i < len(ss); i++ {
		for j := i; j > 0 && ss[j-1] > ss[j]; j-- {
			ss[j-1], ss[j] = ss[j], ss[j-1]
		}
	}
}
