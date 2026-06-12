package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/theme"
)

// ----- fixtures ---------------------------------------------------------

// mentionFixtureTree builds a small repo-shaped tree exercising every
// index exclusion: gitignored dir, binary extension, SKILL.md, skills/
// dir, plus a realistic nested layout for ranking tests.
func mentionFixtureTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"main.go":                      "package main",
		"README.md":                    "# fixture",
		".gitignore":                   "ignored/\n*.log\n",
		"internal/tui/chat/chat.go":    "package chat",
		"cmd/carlos/chat_helpers.go":   "package main",
		"vault/local.md":               "decoy in-repo vault dir",
		"ignored/secret.txt":           "must not index",
		"debug.log":                    "must not index",
		"assets/logo.png":              "\x89PNG",
		"SKILL.md":                     "must not index",
		"skills/web/SKILL.md":          "must not index",
		"skills/web/reference.md":      "must not index (skills dir pruned)",
		"docs/notes/getting-going.md":  "hello",
		"internal/agent/loop.go":       "package agent",
		"internal/fuzzy/fuzzy.go":      "package fuzzy",
		"internal/fuzzy/fuzzy_test.go": "package fuzzy",
	}
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// newMentionModel builds a driven chat Model whose mention walk is
// rooted at the fixture tree.
func newMentionModel(t *testing.T, agentID string) *Model {
	t.Helper()
	log := openTempLog(t)
	seedAgent(t, log, agentID, "mention test", "fake")
	m := New(log, agentID, NewMemTextSource())
	m = drive(t, m, 120, 30)
	m.mentionRoot = mentionFixtureTree(t)
	return m
}

// typeRunes feeds s one keystroke at a time through the full Update
// route, exactly as a user typing.
func typeRunes(t *testing.T, m *Model, s string) *Model {
	t.Helper()
	for _, r := range s {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = next.(*Model)
	}
	return m
}

// ----- predicate --------------------------------------------------------

// TestMentionQueryAt pins the @token predicate edge cases: token-
// opening @ only (start of line or after whitespace), no whitespace
// between @ and cursor, email-like text never triggers, the lone-@-
// then-space escape hatch stays literal.
func TestMentionQueryAt(t *testing.T) {
	cases := []struct {
		name  string
		line  string
		col   int
		query string
		ok    bool
	}{
		{"bare @ at start", "@", 1, "", true},
		{"query at start", "@cha", 4, "cha", true},
		{"after space", "see @cha", 8, "cha", true},
		{"after tab", "see\t@x", 6, "x", true},
		{"cursor mid-token", "@abcd", 3, "ab", true},
		{"path query", "@internal/tui", 13, "internal/tui", true},
		{"email-like never triggers", "foo@bar", 7, "", false},
		{"mid-word @ deep", "a@", 2, "", false},
		{"escape hatch: @ then space", "@ ", 2, "", false},
		{"cursor past token", "@x y", 4, "", false},
		{"no @ in token", "plain", 5, "", false},
		{"empty line", "", 0, "", false},
		{"cursor before @", "@x", 0, "", false},
		{"double @ disarms", "x @@y", 5, "", false},
		{"col out of range", "@x", 9, "", false},
		{"negative col", "@x", -1, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, ok := mentionQueryAt(tc.line, tc.col)
			if ok != tc.ok || q != tc.query {
				t.Errorf("mentionQueryAt(%q, %d) = (%q, %v), want (%q, %v)",
					tc.line, tc.col, q, ok, tc.query, tc.ok)
			}
		})
	}
}

// ----- index ------------------------------------------------------------

// TestBuildMentionIndex_Exclusions: gitignore rules, binary extensions,
// SKILL.md, and skills/ directories all stay out of the index; normal
// source files (including dotfiles like .gitignore itself) stay in.
func TestBuildMentionIndex_Exclusions(t *testing.T) {
	idx := buildMentionIndex(mentionFixtureTree(t), true)
	got := make(map[string]bool, len(idx.files))
	for _, f := range idx.files {
		got[f] = true
	}
	for _, want := range []string{
		"main.go", "internal/tui/chat/chat.go", "cmd/carlos/chat_helpers.go",
		"docs/notes/getting-going.md", "vault/local.md",
	} {
		if !got[want] {
			t.Errorf("index missing %q; have %v", want, idx.files)
		}
	}
	for _, banned := range []string{
		"ignored/secret.txt",      // gitignored dir
		"debug.log",               // gitignored pattern
		"assets/logo.png",         // binary extension
		"SKILL.md",                // skill file
		"skills/web/SKILL.md",     // skills dir (pruned)
		"skills/web/reference.md", // skills dir (pruned)
	} {
		if got[banned] {
			t.Errorf("index must exclude %q", banned)
		}
	}
	if idx.truncated {
		t.Error("small fixture must not report truncation")
	}
}

// TestBuildMentionIndex_DepthCap: a pathological nest deeper than
// mentionDepthCap is pruned; shallow files survive.
func TestBuildMentionIndex_DepthCap(t *testing.T) {
	root := t.TempDir()
	deep := root
	for i := 0; i < mentionDepthCap+2; i++ {
		deep = filepath.Join(deep, "d")
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "buried.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "top.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := buildMentionIndex(root, false)
	if len(idx.files) != 1 || idx.files[0] != "top.go" {
		t.Errorf("depth cap should prune the nest: files = %v", idx.files)
	}
}

// TestBuildMentionIndex_TruncationCap: the walk stops at the cap and
// says so via the truncated flag (never silently).
func TestBuildMentionIndex_TruncationCap(t *testing.T) {
	idx := buildMentionIndexCapped(mentionFixtureTree(t), false, 2)
	if !idx.truncated {
		t.Fatal("capped walk must set truncated")
	}
	if len(idx.files) != 2 {
		t.Errorf("capped index holds %d files, want 2", len(idx.files))
	}
}

// ----- ranking ----------------------------------------------------------

// seededModel returns a bare Model with a pre-built cwd index so the
// ranking tests are pure (no disk walk).
func seededModel(files ...string) *Model {
	return &Model{mentionIdx: &mentionIndex{files: files, builtAt: time.Now()}}
}

// TestMentionCandidates_FuzzyOrder: filename-over-directory weighting
// from internal/fuzzy carries through - "chat" ranks chat.go above
// chat_helpers.go on realistic paths.
func TestMentionCandidates_FuzzyOrder(t *testing.T) {
	m := seededModel(
		"cmd/carlos/chat_helpers.go",
		"internal/tui/chat/chat.go",
		"README.md",
	)
	matches, total, note := m.mentionCandidates("chat")
	if total != 2 || note != "" {
		t.Fatalf("total = %d note = %q, want 2 matches no note", total, note)
	}
	if matches[0].display != "internal/tui/chat/chat.go" {
		t.Errorf("top match = %q, want internal/tui/chat/chat.go", matches[0].display)
	}
	if matches[0].path != matches[0].display {
		t.Errorf("cwd-tier path must equal display: %+v", matches[0])
	}
}

// TestMentionCandidates_VaultTier: a "vault/" (or "@vault/") query with
// a configured vault searches the vault tree; displays gain the vault/
// prefix and paths resolve to absolute on-disk locations.
func TestMentionCandidates_VaultTier(t *testing.T) {
	vault := t.TempDir()
	if err := os.MkdirAll(filepath.Join(vault, "notes"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "notes", "roadmap.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := seededModel("main.go")
	m.vaultPath = vault

	for _, q := range []string{"vault/road", "@vault/road"} {
		matches, _, _ := m.mentionCandidates(q)
		if len(matches) != 1 {
			t.Fatalf("q=%q matches = %v, want exactly roadmap.md", q, matches)
		}
		if matches[0].display != "vault/notes/roadmap.md" {
			t.Errorf("q=%q display = %q", q, matches[0].display)
		}
		if want := filepath.Join(vault, "notes", "roadmap.md"); matches[0].path != want {
			t.Errorf("q=%q path = %q, want %q", q, matches[0].path, want)
		}
	}

	// Vault index is TTL-cached: a second query reuses the built index.
	before := m.mentionVaultIdx
	m.mentionCandidates("vault/road")
	if m.mentionVaultIdx != before {
		t.Error("vault index should be reused within the TTL")
	}
}

// TestMentionCandidates_VaultPrefixWithoutVault: no configured vault
// means "vault/" is just text - the query completes from the cwd tier,
// so a repo with a literal vault/ directory still works.
func TestMentionCandidates_VaultPrefixWithoutVault(t *testing.T) {
	m := seededModel("vault/local.md", "main.go")
	matches, _, _ := m.mentionCandidates("vault/loc")
	if len(matches) != 1 || matches[0].display != "vault/local.md" {
		t.Errorf("expected cwd fallthrough to vault/local.md, got %v", matches)
	}
	if m.mentionVaultIdx != nil {
		t.Error("no vault index should be built without a configured vault")
	}
	if idx := m.mentionVaultIndex(); idx != nil {
		t.Error("mentionVaultIndex must be nil without a configured vault")
	}
}

// TestMentionCandidates_MatchCap: more matches than mentionMatchCap
// clip the navigable list but report the true total so the band can
// say "showing N of M".
func TestMentionCandidates_MatchCap(t *testing.T) {
	files := make([]string, mentionMatchCap+10)
	for i := range files {
		files[i] = "f" + itoa(i) + ".txt"
	}
	m := seededModel(files...)
	matches, total, _ := m.mentionCandidates("")
	if len(matches) != mentionMatchCap || total != mentionMatchCap+10 {
		t.Errorf("len = %d total = %d, want %d / %d",
			len(matches), total, mentionMatchCap, mentionMatchCap+10)
	}
}

// ----- refresh / key flow -----------------------------------------------

// TestMentionSuggest_OpensOnAtAndRanks: typing "@cha" through the real
// key route opens the band with fuzzy-ranked fixture files.
func TestMentionSuggest_OpensOnAtAndRanks(t *testing.T) {
	m := newMentionModel(t, "01HV00000000000000000I4001")
	m = typeRunes(t, m, "look at @cha")
	s := m.mentionSuggest
	if !s.open || s.query != "cha" {
		t.Fatalf("band should be open with query 'cha': %+v", s)
	}
	if len(s.matches) == 0 || s.matches[0].display != "internal/tui/chat/chat.go" {
		t.Errorf("top match = %+v, want chat.go first", s.matches)
	}
}

// TestMentionSuggest_EmailNeverTriggers: "foo@bar" typed mid-message
// stays literal text - no band, no chip.
func TestMentionSuggest_EmailNeverTriggers(t *testing.T) {
	m := newMentionModel(t, "01HV00000000000000000I4002")
	m = typeRunes(t, m, "mail foo@bar")
	if m.mentionSuggest.open {
		t.Error("email-like @ must not open the mention band")
	}
}

// TestMentionSuggest_SlashModeOwnsTheBand: a slash value never
// mentions, even with an @ in the args.
func TestMentionSuggest_SlashModeOwnsTheBand(t *testing.T) {
	m := newMentionModel(t, "01HV00000000000000000I4003")
	m = typeRunes(t, m, "/frame @x")
	if m.mentionSuggest.open {
		t.Error("slash mode must suppress mention suggest")
	}
	if !m.slashSuggest.open {
		t.Error("slash suggest should own the band")
	}
}

// TestMentionSuggest_DismissAndRearm: Esc hides the band without
// erasing the token; further typing keeps it hidden; deleting back out
// of the token re-arms it for the next "@".
func TestMentionSuggest_DismissAndRearm(t *testing.T) {
	m := newMentionModel(t, "01HV00000000000000000I4004")
	m = typeRunes(t, m, "@ma")
	if !m.mentionSuggest.open {
		t.Fatal("band should open on @ma")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(*Model)
	if m.mentionSuggest.open || !m.mentionSuggest.dismissed {
		t.Fatal("esc must dismiss the band, keeping the token")
	}
	if got := m.ta.Value(); got != "@ma" {
		t.Fatalf("esc must not erase input: %q", got)
	}
	m = typeRunes(t, m, "i")
	if m.mentionSuggest.open {
		t.Error("band stays dismissed while still inside the token")
	}
	for i := 0; i < 4; i++ { // delete "@mai" entirely
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
		m = next.(*Model)
	}
	m = typeRunes(t, m, "@m")
	if !m.mentionSuggest.open {
		t.Error("leaving the token must re-arm the band")
	}
}

// TestMentionSuggest_AcceptInsertsChip: Tab replaces the typed @token
// with one ◇ mention chip whose attachment carries the relative path,
// and closes the band.
func TestMentionSuggest_AcceptInsertsChip(t *testing.T) {
	m := newMentionModel(t, "01HV00000000000000000I4005")
	m = typeRunes(t, m, "see @main.go")
	if !m.mentionSuggest.open {
		t.Fatal("band should be open")
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(*Model)

	val := m.ta.Value()
	if strings.Contains(val, "@main") || !strings.Contains(val, "‹m:") {
		t.Fatalf("tab must swap the @token for a mention marker: %q", val)
	}
	if !strings.HasPrefix(val, "see ") {
		t.Errorf("text before the token must survive: %q", val)
	}
	_, atts := m.composer.Serialize()
	if len(atts) != 1 {
		t.Fatalf("attachments = %v, want exactly one", atts)
	}
	if atts[0].Kind != agent.AttachmentMention || atts[0].Path != "main.go" || atts[0].Nickname != "main.go" {
		t.Errorf("attachment = %+v, want mention main.go", atts[0])
	}
	if m.mentionSuggest.open {
		t.Error("band must close after accept")
	}
}

// TestMentionSuggest_AcceptVaultPath: accepting a vault candidate
// stores the ABSOLUTE vault path so read tools resolve it.
func TestMentionSuggest_AcceptVaultPath(t *testing.T) {
	m := newMentionModel(t, "01HV00000000000000000I4006")
	vault := t.TempDir()
	if err := os.WriteFile(filepath.Join(vault, "todo.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m.vaultPath = vault
	m = typeRunes(t, m, "@vault/todo")
	if !m.mentionSuggest.open || len(m.mentionSuggest.matches) == 0 {
		t.Fatalf("vault tier should match: %+v", m.mentionSuggest)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(*Model)
	_, atts := m.composer.Serialize()
	if len(atts) != 1 || atts[0].Path != filepath.Join(vault, "todo.md") {
		t.Errorf("vault mention path = %+v, want absolute %s", atts, filepath.Join(vault, "todo.md"))
	}
}

// TestHandleMentionSuggestKey_Navigation: ↑↓ wrap the match list when
// it has 2+ entries and fall through (history/textarea motion) when it
// doesn't; tab with no matches still consumes the key; closed band
// handles nothing.
func TestHandleMentionSuggestKey_Navigation(t *testing.T) {
	m := &Model{}
	m.mentionSuggest = mentionSuggest{
		open:    true,
		matches: []mentionMatch{{display: "a.go"}, {display: "b.go"}, {display: "c.go"}},
	}
	if _, handled := m.handleMentionSuggestKey("down"); !handled || m.mentionSuggest.cursor != 1 {
		t.Errorf("down: cursor = %d handled = %v", m.mentionSuggest.cursor, handled)
	}
	if _, handled := m.handleMentionSuggestKey("up"); !handled || m.mentionSuggest.cursor != 0 {
		t.Errorf("up: cursor = %d handled = %v", m.mentionSuggest.cursor, handled)
	}
	m.mentionSuggest.cursorUp() // wrap from 0
	if m.mentionSuggest.cursor != 2 {
		t.Errorf("up wrap: cursor = %d, want 2", m.mentionSuggest.cursor)
	}
	m.mentionSuggest.cursorDown()
	if m.mentionSuggest.cursor != 0 {
		t.Errorf("down wrap: cursor = %d, want 0", m.mentionSuggest.cursor)
	}

	m.mentionSuggest.matches = m.mentionSuggest.matches[:1]
	if _, handled := m.handleMentionSuggestKey("down"); handled {
		t.Error("single match: arrows must fall through")
	}
	m.mentionSuggest.matches = nil
	if _, handled := m.handleMentionSuggestKey("tab"); !handled {
		t.Error("tab is consumed even with nothing to accept")
	}
	m.mentionSuggest.open = false
	if _, handled := m.handleMentionSuggestKey("esc"); handled {
		t.Error("closed band handles nothing")
	}
	m.readOnly = true
	m.mentionSuggest.open = true
	if _, handled := m.handleMentionSuggestKey("tab"); handled {
		t.Error("read-only model handles nothing")
	}
}

// TestMentionSuggest_GuardBranches: the closed/empty state is inert -
// selection is (zero, false), cursor motion is a no-op, an out-of-
// range cursor never panics.
func TestMentionSuggest_GuardBranches(t *testing.T) {
	var s mentionSuggest
	if _, ok := s.selected(); ok {
		t.Error("closed band must have no selection")
	}
	s.cursorUp()
	s.cursorDown()
	if s.cursor != 0 {
		t.Errorf("cursor moved on empty matches: %d", s.cursor)
	}
	s = mentionSuggest{open: true, matches: []mentionMatch{{display: "a.go"}}, cursor: 7}
	if _, ok := s.selected(); ok {
		t.Error("out-of-range cursor must yield no selection")
	}
}

// TestRefreshMentionSuggest_CwdDefault: with no mentionRoot override
// the index walks the process working directory.
func TestRefreshMentionSuggest_CwdDefault(t *testing.T) {
	m := newMentionModel(t, "01HV00000000000000000I4007")
	fixture := m.mentionRoot
	m.mentionRoot = ""
	t.Chdir(fixture)
	m = typeRunes(t, m, "@main")
	if !m.mentionSuggest.open || len(m.mentionSuggest.matches) == 0 {
		t.Errorf("cwd-default walk found nothing: %+v", m.mentionSuggest)
	}
}

// ----- band rendering ----------------------------------------------------

// TestRenderMentionHint_Structure: 3 rows with multiple matches -
// basename chips, ↳ full relative path of the highlighted candidate,
// keybind row with the attach verb.
func TestRenderMentionHint_Structure(t *testing.T) {
	s := mentionSuggest{
		open: true,
		matches: []mentionMatch{
			{display: "internal/tui/chat/chat.go"},
			{display: "cmd/carlos/chat_helpers.go"},
		},
		total: 2,
		query: "chat",
	}
	out := stripANSI(renderMentionHint(s, 100))
	rows := strings.Split(out, "\n")
	if len(rows) != 3 {
		t.Fatalf("band rows = %d, want 3:\n%s", len(rows), out)
	}
	if !strings.Contains(rows[0], "chat.go") || !strings.Contains(rows[0], "chat_helpers.go") {
		t.Errorf("chips row should carry basenames: %q", rows[0])
	}
	if !strings.Contains(rows[1], "↳ internal/tui/chat/chat.go") {
		t.Errorf("description row should carry the full path: %q", rows[1])
	}
	for _, key := range []string{"↑↓ select", "tab attach", "enter send", "esc cancel"} {
		if !strings.Contains(rows[2], key) {
			t.Errorf("keybind row missing %q: %q", key, rows[2])
		}
	}
	if renderMentionHint(mentionSuggest{}, 100) != "" {
		t.Error("closed band must render empty")
	}
}

// TestRenderMentionHint_SingleAndNoMatch: one match collapses the
// chips row into the description; zero matches warn with the query.
func TestRenderMentionHint_SingleAndNoMatch(t *testing.T) {
	one := mentionSuggest{open: true, matches: []mentionMatch{{display: "main.go"}}, total: 1}
	rows := strings.Split(stripANSI(renderMentionHint(one, 100)), "\n")
	if len(rows) != 2 || !strings.Contains(rows[0], "↳ main.go") {
		t.Errorf("single match should collapse to description + keybinds:\n%v", rows)
	}

	none := mentionSuggest{open: true, query: "zzz"}
	out := stripANSI(renderMentionHint(none, 100))
	if !strings.Contains(out, "no files match @zzz") {
		t.Errorf("no-match warn missing:\n%s", out)
	}
}

// TestRenderMentionHint_TruncationSurfaced: a clipped match list shows
// "showing N of M" and a capped index walk shows the index note -
// truncation is never silent.
func TestRenderMentionHint_TruncationSurfaced(t *testing.T) {
	matches := make([]mentionMatch, mentionMatchCap)
	for i := range matches {
		matches[i] = mentionMatch{display: "f" + itoa(i) + ".txt"}
	}
	s := mentionSuggest{
		open:    true,
		matches: matches,
		total:   mentionMatchCap + 23,
		note:    "first 5000 files indexed",
	}
	out := stripANSI(renderMentionHint(s, 120))
	if !strings.Contains(out, "showing 50 of 73") {
		t.Errorf("match-cap note missing:\n%s", out)
	}
	if !strings.Contains(out, "first 5000 files indexed") {
		t.Errorf("index-cap note missing:\n%s", out)
	}
}

// TestRenderMentionHint_ChipOverflowAndCushion: the chips row slides
// its window to keep the cursor visible and advertises off-screen
// matches with +N markers, mirroring the slash band.
func TestRenderMentionHint_ChipOverflowAndCushion(t *testing.T) {
	matches := make([]mentionMatch, 30)
	for i := range matches {
		matches[i] = mentionMatch{display: "directory/somefile" + itoa(i) + ".go"}
	}
	s := mentionSuggest{open: true, matches: matches, total: 30, cursor: 10}
	rows := strings.Split(stripANSI(renderMentionHint(s, 80)), "\n")
	if !strings.HasPrefix(strings.TrimSpace(rows[0]), "+8 ·") {
		t.Errorf("left overflow marker missing (cursor cushion): %q", rows[0])
	}
	if !strings.Contains(rows[0], "somefile10.go") {
		t.Errorf("cursor chip must stay visible: %q", rows[0])
	}
	if !strings.Contains(rows[0], "+") {
		t.Errorf("right overflow marker missing: %q", rows[0])
	}
}

// TestRenderMentionHint_NoColor: with a NO_COLOR palette the band
// still carries chips, path, notes, and keybinds as plain text.
func TestRenderMentionHint_NoColor(t *testing.T) {
	t.Cleanup(func() { ApplyPalette(theme.Load(theme.Options{})) })
	ApplyPalette(theme.Load(theme.Options{
		Env: func(k string) string {
			if k == "NO_COLOR" {
				return "1"
			}
			return ""
		},
	}))
	s := mentionSuggest{
		open:    true,
		matches: []mentionMatch{{display: "a/x.go"}, {display: "b/y.go"}},
		total:   2,
		note:    "first 5000 files indexed",
	}
	plain := stripANSI(renderMentionHint(s, 100))
	for _, want := range []string{"x.go", "y.go", "↳ a/x.go", "tab attach", "first 5000 files indexed"} {
		if !strings.Contains(plain, want) {
			t.Errorf("NO_COLOR band missing %q:\n%s", want, plain)
		}
	}
}

// TestRenderInput_MentionBandPlacement: the band occupies the hint-band
// slot above the separator while an @token is live; esc clears it.
func TestRenderInput_MentionBandPlacement(t *testing.T) {
	m := newMentionModel(t, "01HV00000000000000000I4008")
	m = typeRunes(t, m, "@main")
	out := stripANSI(m.renderInput(100))
	if !strings.Contains(out, "↳ main.go") {
		t.Fatalf("mention band missing from input block:\n%s", out)
	}
	bandIdx := strings.Index(out, "↳")
	sepIdx := strings.Index(out, "─")
	if bandIdx > sepIdx {
		t.Errorf("band must render above the separator:\n%s", out)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = next.(*Model)
	if strings.Contains(stripANSI(m.renderInput(100)), "↳") {
		t.Error("band should vanish after esc")
	}
}

// ----- peek card ----------------------------------------------------------

// TestRenderMentionPeekCard_Existing: tag, path row, size + mtime
// stats for a stat-able file.
func TestRenderMentionPeekCard_Existing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "loop.go")
	if err := os.WriteFile(p, []byte(strings.Repeat("x", 2048)), 0o644); err != nil {
		t.Fatal(err)
	}
	att := agent.Attachment{Kind: agent.AttachmentMention, Nickname: "loop.go", Path: p}
	out := stripANSI(renderMentionPeekCard(att, 120))
	rows := strings.Split(out, "\n")
	if len(rows) != 4 {
		t.Fatalf("card rows = %d, want 4:\n%s", len(rows), out)
	}
	if !strings.HasSuffix(rows[0], "mention") {
		t.Errorf("corner tag must read 'mention': %q", rows[0])
	}
	if !strings.Contains(rows[1], p) {
		t.Errorf("path row missing: %q", rows[1])
	}
	if !strings.Contains(rows[2], "2 KB · modified ") {
		t.Errorf("stats row missing size/mtime: %q", rows[2])
	}
	if strings.Contains(out, "no longer exists") {
		t.Error("existing file must not warn")
	}
}

// TestRenderMentionPeekCard_Missing: a deleted file swaps the stats
// row for a plain-text warning (stat happens at peek time).
func TestRenderMentionPeekCard_Missing(t *testing.T) {
	att := agent.Attachment{Kind: agent.AttachmentMention, Nickname: "gone.go",
		Path: filepath.Join(t.TempDir(), "gone.go")}
	out := stripANSI(renderMentionPeekCard(att, 80))
	if !strings.Contains(out, "↳ file no longer exists") {
		t.Errorf("missing-file warning absent:\n%s", out)
	}
	// Path-less attachment degrades to the nickname label + warning.
	out = stripANSI(renderMentionPeekCard(agent.Attachment{
		Kind: agent.AttachmentMention, Nickname: "mystery"}, 80))
	if !strings.Contains(out, "mystery") || !strings.Contains(out, "no longer exists") {
		t.Errorf("path-less card should show the label and warn:\n%s", out)
	}
}

// TestRenderMentionPeekCard_NoLeftStripeAndNarrow extends the I-2/I-3
// design regression to mention peeks: no row may open with a stripe
// glyph, at any width, existing or missing file.
func TestRenderMentionPeekCard_NoLeftStripeAndNarrow(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "real.go")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, att := range []agent.Attachment{
		{Kind: agent.AttachmentMention, Path: p},
		{Kind: agent.AttachmentMention, Path: filepath.Join(dir, "missing.go")},
	} {
		for _, w := range []int{120, 40, 10, 0} {
			for i, row := range strings.Split(stripANSI(renderMentionPeekCard(att, w)), "\n") {
				trimmed := strings.TrimLeft(row, " ")
				for _, banned := range []string{"│", "┃", "▌", "█", "▎"} {
					if strings.HasPrefix(trimmed, banned) {
						t.Errorf("w=%d row %d opens with banned stripe glyph %q: %q", w, i, banned, row)
					}
				}
				if !strings.HasPrefix(row, "  ") {
					t.Errorf("w=%d row %d missing 2-cell indent: %q", w, i, row)
				}
			}
		}
	}
}

// TestModel_MentionPeekFromCursor: end-to-end - accept a mention, the
// cursor lands after the chip, and renderInput shows the peek card.
func TestModel_MentionPeekFromCursor(t *testing.T) {
	m := newMentionModel(t, "01HV00000000000000000I4009")
	m = typeRunes(t, m, "@main.go")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(*Model)
	att, ok := m.peekAttachment()
	if !ok || att.Kind != agent.AttachmentMention {
		t.Fatalf("peek after accept = %+v/%v, want the mention chip", att, ok)
	}
	out := stripANSI(m.renderPeek(att, 100))
	if !strings.HasSuffix(strings.Split(out, "\n")[0], "mention") {
		t.Errorf("peek dispatch should reach the mention card:\n%s", out)
	}
}

// ----- replay round-trip ---------------------------------------------------

// TestMentionReplayRoundTrip: a persisted user message carrying a
// mention marker + attachment replays through applyEvent and renders
// the chip as plain "◇ nickname" in the transcript (slice I-1
// machinery, verified for mentions).
func TestMentionReplayRoundTrip(t *testing.T) {
	m := newMentionModel(t, "01HV00000000000000000I4010")
	payload, err := json.Marshal(agent.MessagePayload{
		Text: "see ‹m:1› please",
		Attachments: []agent.Attachment{{
			ID: "1", Kind: agent.AttachmentMention,
			Nickname: "loop.go", Path: "internal/agent/loop.go",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.applyEvent(agent.Event{
		AgentID: m.agentID,
		TS:      time.Now(),
		Type:    agent.EvtUserMessage,
		Payload: payload,
	})
	last := m.transcript[len(m.transcript)-1]
	if last.kind != entryUserMessage || len(last.attachments) != 1 {
		t.Fatalf("transcript entry = %+v, want user message with 1 attachment", last)
	}
	got := displayChips(last.text, last.attachments)
	want := "see " + theme.ChipSigilMention + " loop.go please"
	if got != want {
		t.Errorf("replay render = %q, want %q", got, want)
	}
}

// ----- expansion -----------------------------------------------------------

// TestSubmitMention_ExpansionShape: the submitted payload holds the
// raw marker (persisted form) while agent.ExpandMarkers produces the
// compact tool-readable reference - never the file contents, never the
// raw marker.
func TestSubmitMention_ExpansionShape(t *testing.T) {
	m := newMentionModel(t, "01HV00000000000000000I4011")
	m = typeRunes(t, m, "fix @main.go")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = next.(*Model)
	text, atts := m.composer.Serialize()
	expanded := agent.ExpandMarkers(text, atts)
	if agent.ContainsMarker(expanded) {
		t.Fatalf("raw marker leaked: %q", expanded)
	}
	if !strings.Contains(expanded, "@main.go (mentioned file, not inlined; read it with file tools if needed)") {
		t.Errorf("compact mention reference missing: %q", expanded)
	}
	if strings.Contains(expanded, "package main") {
		t.Errorf("file contents must NOT be inlined: %q", expanded)
	}
}

// TestWithVaultPath_WiresVaultTier: the New(...) option is what
// cmd/carlos.runDefault uses to hand cfg.Vault.Path to the mention
// engine; pin that the wiring (not just a hand-set field) turns the
// @vault/ tier on, and that the zero value leaves it off.
func TestWithVaultPath_WiresVaultTier(t *testing.T) {
	vault := t.TempDir()
	if err := os.WriteFile(filepath.Join(vault, "roadmap.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := openTempLog(t)
	const agentID = "01HV00000000000000000I4001"
	seedAgent(t, log, agentID, "vault option", "fake")

	m := New(log, agentID, NewMemTextSource(), WithVaultPath(vault))
	if m.vaultPath != vault {
		t.Fatalf("vaultPath = %q, want %q", m.vaultPath, vault)
	}
	m.mentionIdx = &mentionIndex{files: []string{"main.go"}, builtAt: time.Now()}
	matches, _, _ := m.mentionCandidates("vault/road")
	if len(matches) != 1 || matches[0].display != "vault/roadmap.md" {
		t.Errorf("vault tier via option: matches = %v, want vault/roadmap.md", matches)
	}

	// Default (no option): tier off, vault/ is plain text.
	bare := New(log, agentID, NewMemTextSource())
	if bare.vaultPath != "" {
		t.Errorf("default vaultPath = %q, want empty", bare.vaultPath)
	}
}
