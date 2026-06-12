package fuzzy

import (
	"reflect"
	"strings"
	"testing"
)

// --- Match: basic semantics ---------------------------------------------

func TestMatchBasics(t *testing.T) {
	tests := []struct {
		name      string
		pattern   string
		candidate string
		wantOK    bool
		wantPos   []int // nil means "don't check positions"
	}{
		{name: "empty pattern matches anything", pattern: "", candidate: "whatever", wantOK: true, wantPos: []int{}},
		{name: "empty pattern matches empty candidate", pattern: "", candidate: "", wantOK: true, wantPos: []int{}},
		{name: "nonempty pattern vs empty candidate", pattern: "a", candidate: "", wantOK: false},
		{name: "simple subsequence", pattern: "fb", candidate: "foobar", wantOK: true, wantPos: []int{0, 3}},
		{name: "not a subsequence", pattern: "ba", candidate: "ab", wantOK: false},
		{name: "pattern longer than candidate", pattern: "abcdef", candidate: "abc", wantOK: false},
		{name: "exact match", pattern: "mode", candidate: "mode", wantOK: true, wantPos: []int{0, 1, 2, 3}},
		{name: "case-insensitive by default", pattern: "readme", candidate: "README.md", wantOK: true, wantPos: []int{0, 1, 2, 3, 4, 5}},
		{name: "no shared runes", pattern: "xyz", candidate: "/research", wantOK: false},
		{name: "consecutive run positions", pattern: "chat", candidate: "cmd/carlos/chat_helpers.go", wantOK: true, wantPos: []int{11, 12, 13, 14}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score, pos, ok := Match(tt.pattern, tt.candidate)
			if ok != tt.wantOK {
				t.Fatalf("Match(%q, %q) ok = %v, want %v", tt.pattern, tt.candidate, ok, tt.wantOK)
			}
			if !ok {
				if pos != nil {
					t.Errorf("no-match positions = %v, want nil", pos)
				}
				return
			}
			if tt.pattern == "" {
				if score != 0 || pos != nil {
					t.Errorf("empty pattern: score=%d pos=%v, want 0/nil", score, pos)
				}
				return
			}
			if len(pos) != len([]rune(tt.pattern)) {
				t.Fatalf("positions length = %d, want %d (one per pattern rune)", len(pos), len([]rune(tt.pattern)))
			}
			for k := 1; k < len(pos); k++ {
				if pos[k] <= pos[k-1] {
					t.Fatalf("positions not strictly ascending: %v", pos)
				}
			}
			if len(tt.wantPos) > 0 && !reflect.DeepEqual(pos, tt.wantPos) {
				t.Errorf("positions = %v, want %v", pos, tt.wantPos)
			}
		})
	}
}

func TestMatchGuardRails(t *testing.T) {
	longPattern := strings.Repeat("a", maxPatternLen+1)
	longCandidate := strings.Repeat("a", maxCandidateLen+1)
	if _, _, ok := Match(longPattern, longCandidate); ok {
		t.Error("over-long pattern should not match")
	}
	if _, _, ok := Match("a", longCandidate); ok {
		t.Error("over-long candidate should not match")
	}
	// Just inside the limits still works.
	if _, _, ok := Match("a", strings.Repeat("a", maxCandidateLen)); !ok {
		t.Error("candidate at the limit should match")
	}
}

// --- Smartcase ------------------------------------------------------------

func TestSmartcase(t *testing.T) {
	tests := []struct {
		name      string
		pattern   string
		candidate string
		wantOK    bool
	}{
		{name: "lowercase pattern matches lowercase", pattern: "re", candidate: "readme", wantOK: true},
		{name: "lowercase pattern matches uppercase", pattern: "re", candidate: "Readme", wantOK: true},
		{name: "uppercase pattern char requires uppercase", pattern: "Re", candidate: "readme", wantOK: false},
		{name: "uppercase pattern char matches uppercase", pattern: "Re", candidate: "Readme", wantOK: true},
		{name: "mixed: only the uppercase char is sensitive", pattern: "rM", candidate: "readMe", wantOK: true},
		{name: "mixed: sensitive char missing", pattern: "rM", candidate: "readme", wantOK: false},
		{name: "unicode uppercase pattern is sensitive", pattern: "Ü", candidate: "über", wantOK: false},
		{name: "unicode uppercase pattern matches", pattern: "Ü", candidate: "Über", wantOK: true},
		{name: "unicode lowercase pattern folds", pattern: "ü", candidate: "Über", wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, _, ok := Match(tt.pattern, tt.candidate); ok != tt.wantOK {
				t.Errorf("Match(%q, %q) ok = %v, want %v", tt.pattern, tt.candidate, ok, tt.wantOK)
			}
		})
	}
}

// --- Unicode: positions are rune indices ----------------------------------

func TestUnicodePositions(t *testing.T) {
	// "über/càfé.txt" -> runes: ü b e r / c à f é . t x t
	//                            0 1 2 3 4 5 6 7 8 9 ...
	_, pos, ok := Match("cf", "über/càfé.txt")
	if !ok {
		t.Fatal("expected match")
	}
	want := []int{5, 7}
	if !reflect.DeepEqual(pos, want) {
		t.Errorf("positions = %v, want %v (rune indices, not byte offsets)", pos, want)
	}
	// Highlighting via []rune must hit the matched characters.
	runes := []rune("über/càfé.txt")
	if runes[pos[0]] != 'c' || runes[pos[1]] != 'f' {
		t.Errorf("positions do not index the matched runes: got %q %q", runes[pos[0]], runes[pos[1]])
	}
}

func TestUnicodeFold(t *testing.T) {
	score, pos, ok := Match("hél", "Héllo.go")
	if !ok {
		t.Fatal("expected match")
	}
	if !reflect.DeepEqual(pos, []int{0, 1, 2}) {
		t.Errorf("positions = %v, want [0 1 2]", pos)
	}
	if score <= 0 {
		t.Errorf("prefix match score = %d, want positive", score)
	}
}

// --- Ranking behavior ------------------------------------------------------

// rankOrder runs Rank and returns just the candidates, best-first.
func rankOrder(t *testing.T, pattern string, candidates []string) []string {
	t.Helper()
	results := Rank(pattern, candidates)
	out := make([]string, len(results))
	for i, r := range results {
		out[i] = r.Candidate
	}
	return out
}

// assertBefore fails unless a ranks strictly before b in the result.
func assertBefore(t *testing.T, order []string, a, b string) {
	t.Helper()
	ia, ib := -1, -1
	for i, c := range order {
		if c == a {
			ia = i
		}
		if c == b {
			ib = i
		}
	}
	if ia < 0 || ib < 0 {
		t.Fatalf("missing candidate in ranking %v (want both %q and %q)", order, a, b)
	}
	if ia >= ib {
		t.Errorf("expected %q before %q, got order %v", a, b, order)
	}
}

func TestRankSlashCommands(t *testing.T) {
	commands := []string{"/mode", "/model", "/research", "/resume", "/restore", "/help", "/away"}

	t.Run("rese prefers consecutive run", func(t *testing.T) {
		order := rankOrder(t, "rese", commands)
		// /research has "rese" as a 4-rune consecutive run; /resume
		// needs an inner gap to reach its second e; /restore needs one
		// for its e... wait, /restore has no second e after s-t-o-r-e:
		// r-e-s-(t-o-r)-e is a gapped match.
		assertBefore(t, order, "/research", "/resume")
		assertBefore(t, order, "/research", "/restore")
		for _, c := range order {
			if c == "/mode" || c == "/help" || c == "/away" || c == "/model" {
				t.Errorf("%q should not match pattern \"rese\"", c)
			}
		}
	})

	t.Run("exact name beats its extensions", func(t *testing.T) {
		order := rankOrder(t, "mode", commands)
		if len(order) == 0 || order[0] != "/mode" {
			t.Fatalf("order = %v, want /mode first", order)
		}
		assertBefore(t, order, "/mode", "/model")
	})

	t.Run("res matches only the res-commands", func(t *testing.T) {
		order := rankOrder(t, "res", commands)
		want := map[string]bool{"/research": true, "/resume": true, "/restore": true}
		if len(order) != len(want) {
			t.Fatalf("order = %v, want exactly the three res-commands", order)
		}
		for _, c := range order {
			if !want[c] {
				t.Errorf("unexpected match %q", c)
			}
		}
	})
}

func TestRankFilePaths(t *testing.T) {
	paths := []string{
		"cmd/carlos/chat_helpers.go",
		"internal/tui/chat/chat.go",
		"internal/chatglue/render.go",
		"internal/agent/sessions.go",
		"docs/skills.html",
	}

	t.Run("chat prefers basename, then tighter basename", func(t *testing.T) {
		order := rankOrder(t, "chat", paths)
		// Basename "chat.go" is the tightest fit, then the looser
		// basename "chat_helpers.go", then the directory-only match.
		assertBefore(t, order, "internal/tui/chat/chat.go", "cmd/carlos/chat_helpers.go")
		assertBefore(t, order, "cmd/carlos/chat_helpers.go", "internal/chatglue/render.go")
		for _, c := range order {
			if c == "internal/agent/sessions.go" || c == "docs/skills.html" {
				t.Errorf("%q should not match pattern \"chat\"", c)
			}
		}
	})

	t.Run("filename match beats directory match", func(t *testing.T) {
		order := rankOrder(t, "sess", paths)
		if len(order) == 0 || order[0] != "internal/agent/sessions.go" {
			t.Fatalf("order = %v, want sessions.go first", order)
		}
	})
}

func TestRankPrefixAndBoundaries(t *testing.T) {
	t.Run("prefix beats infix", func(t *testing.T) {
		order := rankOrder(t, "re", []string{"more", "research"})
		assertBefore(t, order, "research", "more")
	})

	t.Run("consecutive beats gapped", func(t *testing.T) {
		order := rankOrder(t, "ab", []string{"axb", "abx"})
		assertBefore(t, order, "abx", "axb")
	})

	t.Run("word boundary beats mid-word", func(t *testing.T) {
		order := rankOrder(t, "h", []string{"chat.go", "chat_helpers.go"})
		assertBefore(t, order, "chat_helpers.go", "chat.go")
	})

	t.Run("camelCase boundary beats mid-word", func(t *testing.T) {
		order := rankOrder(t, "fb", []string{"foobar", "FooBar"})
		assertBefore(t, order, "FooBar", "foobar")
	})
}

func TestRankEmptyPattern(t *testing.T) {
	candidates := []string{"/zeta", "/alpha", "/mid"}
	results := Rank("", candidates)
	if len(results) != len(candidates) {
		t.Fatalf("got %d results, want all %d", len(results), len(candidates))
	}
	for i, r := range results {
		if r.Candidate != candidates[i] || r.Index != i {
			t.Errorf("result %d = %+v, want input order preserved", i, r)
		}
		if r.Score != 0 || r.Positions != nil {
			t.Errorf("result %d: score=%d positions=%v, want 0/nil", i, r.Score, r.Positions)
		}
	}
}

func TestRankNoMatches(t *testing.T) {
	results := Rank("zzz", []string{"/mode", "/help"})
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}

func TestRankStableOnTies(t *testing.T) {
	// Identical candidates score identically; stable sort must keep
	// input order, observable via Index.
	results := Rank("aa", []string{"aab", "aab", "aab"})
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	for i, r := range results {
		if r.Index != i {
			t.Errorf("tie order broken: result %d has Index %d", i, r.Index)
		}
	}
}

func TestRankDeterministic(t *testing.T) {
	pattern := "chat"
	candidates := []string{
		"cmd/carlos/chat_helpers.go",
		"internal/tui/chat/chat.go",
		"internal/chatglue/render.go",
	}
	first := Rank(pattern, candidates)
	for i := 0; i < 10; i++ {
		if got := Rank(pattern, candidates); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d differs: %+v vs %+v", i, got, first)
		}
	}
}

func TestRankIndexAndScoreConsistency(t *testing.T) {
	candidates := []string{"/research", "/resume"}
	for _, r := range Rank("res", candidates) {
		if r.Candidate != candidates[r.Index] {
			t.Errorf("Index %d does not point at %q", r.Index, r.Candidate)
		}
		score, pos, ok := Match("res", r.Candidate)
		if !ok || score != r.Score || !reflect.DeepEqual(pos, r.Positions) {
			t.Errorf("Rank result %+v disagrees with Match (%d, %v, %v)", r, score, pos, ok)
		}
	}
}

// --- Scoring internals worth pinning ---------------------------------------

func TestExactMatchOutranksEverything(t *testing.T) {
	exact, _, _ := Match("mode", "mode")
	prefix, _, _ := Match("mode", "modeling-extended")
	if exact <= prefix {
		t.Errorf("exact %d should outrank prefix %d", exact, prefix)
	}
	// Exact match under smartcase fold also gets the bonus.
	folded, _, _ := Match("mode", "MODE")
	if folded <= prefix {
		t.Errorf("case-folded exact %d should outrank prefix %d", folded, prefix)
	}
}

func TestGapPenalties(t *testing.T) {
	tight, _, _ := Match("ab", "a-b")
	loose, _, _ := Match("ab", "a---b")
	if tight <= loose {
		t.Errorf("shorter inner gap %d should beat longer %d", tight, loose)
	}
	early, _, _ := Match("ab", "ab-xxxx")
	late, _, _ := Match("ab", "xxxx-ab")
	if early <= late {
		t.Errorf("leading match %d should beat trailing match %d", early, late)
	}
}

func TestPositionsPreferBoundaryRun(t *testing.T) {
	// Both "ch" runs exist; backtracking should pick the boundary-
	// anchored basename run, not the directory one.
	_, pos, ok := Match("chat", "internal/tui/chat/chat.go")
	if !ok {
		t.Fatal("expected match")
	}
	if !reflect.DeepEqual(pos, []int{18, 19, 20, 21}) {
		t.Errorf("positions = %v, want the basename run [18 19 20 21]", pos)
	}
}

func TestBoundaryBonusTable(t *testing.T) {
	tests := []struct {
		name string
		prev rune
		cur  rune
		j    int
		want int
	}{
		{name: "start of string", prev: 0, cur: 'a', j: 0, want: bonusBoundaryStart},
		{name: "after slash", prev: '/', cur: 'a', j: 5, want: bonusBoundarySlash},
		{name: "after dash", prev: '-', cur: 'a', j: 5, want: bonusBoundaryWord},
		{name: "after underscore", prev: '_', cur: 'a', j: 5, want: bonusBoundaryWord},
		{name: "after space", prev: ' ', cur: 'a', j: 5, want: bonusBoundaryWord},
		{name: "after dot", prev: '.', cur: 'g', j: 5, want: bonusBoundaryDot},
		{name: "camel transition", prev: 'o', cur: 'B', j: 5, want: bonusCamel},
		{name: "mid-word", prev: 'o', cur: 'o', j: 5, want: 0},
		{name: "upper to upper is not camel", prev: 'O', cur: 'B', j: 5, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := boundaryBonus(tt.prev, tt.cur, tt.j); got != tt.want {
				t.Errorf("boundaryBonus(%q, %q, %d) = %d, want %d", tt.prev, tt.cur, tt.j, got, tt.want)
			}
		})
	}
}

func TestRunesMatch(t *testing.T) {
	tests := []struct {
		p, c rune
		want bool
	}{
		{'a', 'a', true},
		{'a', 'A', true},
		{'A', 'a', false}, // smartcase: uppercase pattern is sensitive
		{'A', 'A', true},
		{'/', '/', true},
		{'1', '1', true},
		{'é', 'É', true},
		{'É', 'é', false},
		{'a', 'b', false},
	}
	for _, tt := range tests {
		if got := runesMatch(tt.p, tt.c); got != tt.want {
			t.Errorf("runesMatch(%q, %q) = %v, want %v", tt.p, tt.c, got, tt.want)
		}
	}
}

func TestIsSubsequence(t *testing.T) {
	tests := []struct {
		p, c string
		want bool
	}{
		{"abc", "aXbXc", true},
		{"abc", "acb", false},
		{"", "x", false}, // callers handle empty pattern before this
		{"a", "a", true},
		{"aa", "a", false},
	}
	for _, tt := range tests {
		if got := isSubsequence([]rune(tt.p), []rune(tt.c)); got != tt.want {
			t.Errorf("isSubsequence(%q, %q) = %v, want %v", tt.p, tt.c, got, tt.want)
		}
	}
}

// --- Realistic end-to-end palette scenario ---------------------------------

func TestRealisticFileTree(t *testing.T) {
	tree := []string{
		"cmd/carlos/main.go",
		"cmd/carlos/sessions.go",
		"cmd/carlos/chat_helpers.go",
		"internal/agent/sessions.go",
		"internal/tui/chat/chat.go",
		"internal/tui/chat/input.go",
		"internal/tui/manage/filter.go",
		"internal/fuzzy/fuzzy.go",
		"docs/index.html",
	}

	t.Run("fuzz finds the fuzzy package first", func(t *testing.T) {
		order := rankOrder(t, "fuzz", tree)
		if len(order) == 0 || order[0] != "internal/fuzzy/fuzzy.go" {
			t.Fatalf("order = %v, want fuzzy.go first", order)
		}
	})

	t.Run("sessions ranks both sessions files above others", func(t *testing.T) {
		order := rankOrder(t, "sessions", tree)
		if len(order) < 2 {
			t.Fatalf("order = %v, want at least 2 matches", order)
		}
		got := map[string]bool{order[0]: true, order[1]: true}
		if !got["cmd/carlos/sessions.go"] || !got["internal/agent/sessions.go"] {
			t.Errorf("top two = %v, want both sessions.go files", order[:2])
		}
	})

	t.Run("path-segment pattern", func(t *testing.T) {
		order := rankOrder(t, "tui/chat", tree)
		if len(order) == 0 {
			t.Fatal("expected matches for tui/chat")
		}
		if order[0] != "internal/tui/chat/chat.go" && order[0] != "internal/tui/chat/input.go" {
			t.Errorf("order = %v, want a tui/chat file first", order)
		}
	})
}
