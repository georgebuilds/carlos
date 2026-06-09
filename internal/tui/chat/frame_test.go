package chat

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/georgebuilds/carlos/internal/agent"
)

// stubLog satisfies the chat.New eventlog dependency without touching
// SQLite — adequate for the slash-handler tests below since they never
// trigger the projection / event subscribe paths.
type stubLog struct{ agent.EventLog }

func newFramedModel(t *testing.T, ui FrameUI, opts ...Option) *Model {
	t.Helper()
	allOpts := append([]Option{WithFrame(ui)}, opts...)
	m := New(stubLog{}, "test-agent", NewMemTextSource(), allOpts...)
	m.width = 120
	m.height = 30
	return m
}

func runStatusCmd(t *testing.T, cmd tea.Cmd) statusMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("nil cmd")
	}
	msg := cmd()
	s, ok := msg.(statusMsg)
	if !ok {
		t.Fatalf("expected statusMsg, got %T (%v)", msg, msg)
	}
	return s
}

func TestFrameSlash_NoArgsEchoesActive(t *testing.T) {
	m := newFramedModel(t, FrameUI{Active: "work", Available: []string{"personal", "work"}})
	s := runStatusCmd(t, m.frameSlash(""))
	if !strings.Contains(s.text, "work") {
		t.Errorf("expected echo to mention active frame; got %q", s.text)
	}
	if s.kind != statusInfo {
		t.Errorf("kind = %v, want statusInfo", s.kind)
	}
}

func TestFrameSlash_ListEnumerates(t *testing.T) {
	m := newFramedModel(t, FrameUI{
		Active:    "personal",
		Available: []string{"personal", "work", "research"},
	})
	s := runStatusCmd(t, m.frameSlash("list"))
	for _, n := range []string{"personal", "work", "research"} {
		if !strings.Contains(s.text, n) {
			t.Errorf("/frame list missing %q in %q", n, s.text)
		}
	}
}

func TestFrameSlash_UnwiredEchoesWarn(t *testing.T) {
	m := newFramedModel(t, FrameUI{}) // Active == ""
	s := runStatusCmd(t, m.frameSlash(""))
	if s.kind != statusWarn {
		t.Errorf("kind = %v, want statusWarn", s.kind)
	}
	if !strings.Contains(s.text, "not wired") {
		t.Errorf("expected 'not wired' message; got %q", s.text)
	}
}

func TestFrameSlash_SwitchPersistsAndUpdatesActive(t *testing.T) {
	var captured string
	m := newFramedModel(t, FrameUI{
		Active:    "personal",
		Available: []string{"personal", "work"},
		SwitchActive: func(name string) error {
			captured = name
			return nil
		},
	})
	s := runStatusCmd(t, m.frameSlash("switch work"))
	if captured != "work" {
		t.Errorf("SwitchActive captured %q, want %q", captured, "work")
	}
	if m.frame.Active != "work" {
		t.Errorf("in-process Active = %q, want %q", m.frame.Active, "work")
	}
	if !strings.Contains(s.text, "switched to work") {
		t.Errorf("expected confirmation; got %q", s.text)
	}
}

func TestFrameSlash_SwitchRefreshesInProcessFields(t *testing.T) {
	m := newFramedModel(t, FrameUI{
		Active:    "personal",
		Glyph:     "◉",
		Accent:    "cream",
		Mode:      "solo",
		Available: []string{"personal", "work"},
		SwitchActive: func(string) error { return nil },
		LookupFrame: func(name string) (FrameUIUpdate, bool) {
			if name != "work" {
				return FrameUIUpdate{}, false
			}
			return FrameUIUpdate{
				Glyph:        "▣",
				Accent:       "rust",
				Mode:         "orchestrator",
				Capabilities: map[string]string{"calendar": "caldav"},
			}, true
		},
	})
	runStatusCmd(t, m.frameSlash("switch work"))
	if m.frame.Glyph != "▣" {
		t.Errorf("Glyph not refreshed: %q", m.frame.Glyph)
	}
	if m.frame.Accent != "rust" {
		t.Errorf("Accent not refreshed: %q", m.frame.Accent)
	}
	if m.frame.Mode != "orchestrator" {
		t.Errorf("Mode not refreshed: %q", m.frame.Mode)
	}
	if m.frame.Capabilities["calendar"] != "caldav" {
		t.Errorf("Capabilities not refreshed: %v", m.frame.Capabilities)
	}
}

func TestFrameSlash_SwitchUseAlias(t *testing.T) {
	m := newFramedModel(t, FrameUI{
		Active:       "personal",
		Available:    []string{"personal", "work"},
		SwitchActive: func(string) error { return nil },
	})
	runStatusCmd(t, m.frameSlash("use work"))
	if m.frame.Active != "work" {
		t.Errorf("`use` alias did not switch; Active=%q", m.frame.Active)
	}
}

func TestFrameSlash_SwitchToSameNoops(t *testing.T) {
	called := false
	m := newFramedModel(t, FrameUI{
		Active:       "personal",
		Available:    []string{"personal", "work"},
		SwitchActive: func(string) error { called = true; return nil },
	})
	s := runStatusCmd(t, m.frameSlash("switch personal"))
	if called {
		t.Errorf("SwitchActive should not fire when switching to the active frame")
	}
	if !strings.Contains(s.text, "already") {
		t.Errorf("expected 'already' echo; got %q", s.text)
	}
}

func TestFrameSlash_SwitchUnknownFrameFailsFast(t *testing.T) {
	called := false
	m := newFramedModel(t, FrameUI{
		Active:       "personal",
		Available:    []string{"personal", "work"},
		SwitchActive: func(string) error { called = true; return nil },
	})
	s := runStatusCmd(t, m.frameSlash("switch nope"))
	if called {
		t.Errorf("SwitchActive should not fire for an unknown name")
	}
	if s.kind != statusWarn {
		t.Errorf("kind = %v, want statusWarn", s.kind)
	}
	if !strings.Contains(s.text, "unknown frame") {
		t.Errorf("expected 'unknown frame' echo; got %q", s.text)
	}
}

func TestFrameSlash_SwitchEmptyArgEchoesUsage(t *testing.T) {
	m := newFramedModel(t, FrameUI{
		Active:       "personal",
		Available:    []string{"personal"},
		SwitchActive: func(string) error { return nil },
	})
	s := runStatusCmd(t, m.frameSlash("switch "))
	if s.kind != statusWarn {
		t.Errorf("kind = %v, want statusWarn", s.kind)
	}
	if !strings.Contains(s.text, "usage") {
		t.Errorf("expected usage echo; got %q", s.text)
	}
}

func TestFrameSlash_SwitchPropagatesHookError(t *testing.T) {
	m := newFramedModel(t, FrameUI{
		Active:    "personal",
		Available: []string{"personal", "work"},
		SwitchActive: func(string) error {
			return errors.New("disk full")
		},
	})
	s := runStatusCmd(t, m.frameSlash("switch work"))
	if s.kind != statusWarn {
		t.Errorf("kind = %v, want statusWarn", s.kind)
	}
	if !strings.Contains(s.text, "disk full") {
		t.Errorf("expected hook error in echo; got %q", s.text)
	}
	if m.frame.Active != "personal" {
		t.Errorf("Active should not change on hook error; got %q", m.frame.Active)
	}
}

func TestFrameSlash_SwitchUnwiredWhenHookNil(t *testing.T) {
	m := newFramedModel(t, FrameUI{
		Active:    "personal",
		Available: []string{"personal", "work"},
	})
	s := runStatusCmd(t, m.frameSlash("switch work"))
	if s.kind != statusWarn {
		t.Errorf("kind = %v, want statusWarn", s.kind)
	}
	if !strings.Contains(s.text, "not wired") {
		t.Errorf("expected not-wired warning; got %q", s.text)
	}
}

func TestFrameSlash_UnknownVerbEchoesHelp(t *testing.T) {
	m := newFramedModel(t, FrameUI{
		Active:    "personal",
		Available: []string{"personal"},
	})
	s := runStatusCmd(t, m.frameSlash("foo bar"))
	if !strings.Contains(s.text, "/frame") {
		t.Errorf("expected help echo; got %q", s.text)
	}
}

func TestRenderHeader_IncludesFramePillWhenActive(t *testing.T) {
	m := newFramedModel(t, FrameUI{
		Active: "work",
		Glyph:  "▣",
		Accent: "rust",
	})
	out := m.renderHeader(120)
	if !strings.Contains(out, "work") {
		t.Errorf("header missing frame name; got:\n%s", out)
	}
	if !strings.Contains(out, "▣") {
		t.Errorf("header missing frame glyph; got:\n%s", out)
	}
}

func TestRenderHeader_OmitsFramePillWhenInactive(t *testing.T) {
	m := newFramedModel(t, FrameUI{})
	out := m.renderHeader(120)
	// `·` could appear elsewhere but the pill specifically pairs it with
	// a frame name; asserting on absence of any frame-like artifact.
	if strings.Contains(out, "▣") || strings.Contains(out, "◉") {
		t.Errorf("header should not paint a frame pill when Active is empty; got:\n%s", out)
	}
}

func TestCapabilitiesSlash_NoFrame(t *testing.T) {
	m := newFramedModel(t, FrameUI{})
	s := runStatusCmd(t, m.capabilitiesSlash())
	if s.kind != statusWarn {
		t.Errorf("kind = %v, want statusWarn", s.kind)
	}
}

func TestCapabilitiesSlash_EmptyMapEchoesCTA(t *testing.T) {
	m := newFramedModel(t, FrameUI{Active: "personal"})
	s := runStatusCmd(t, m.capabilitiesSlash())
	if !strings.Contains(s.text, "no capabilities") {
		t.Errorf("expected empty-state CTA; got %q", s.text)
	}
	if !strings.Contains(s.text, "config.yaml") {
		t.Errorf("expected config.yaml pointer; got %q", s.text)
	}
}

func TestModeSlash_NoArgsEchoesCurrent(t *testing.T) {
	m := newFramedModel(t, FrameUI{Active: "work", Mode: "orchestrator"})
	s := runStatusCmd(t, m.modeSlash(""))
	if !strings.Contains(s.text, "orchestrator") {
		t.Errorf("missing current mode in echo: %q", s.text)
	}
	if !strings.Contains(s.text, "work") {
		t.Errorf("missing frame name: %q", s.text)
	}
}

func TestModeSlash_RejectsUnknownMode(t *testing.T) {
	m := newFramedModel(t, FrameUI{Active: "personal", Mode: "solo"})
	s := runStatusCmd(t, m.modeSlash("rocket"))
	if s.kind != statusWarn {
		t.Errorf("kind = %v, want statusWarn", s.kind)
	}
	if !strings.Contains(s.text, "rocket") {
		t.Errorf("error should name the bad input: %q", s.text)
	}
}

func TestModeSlash_SwitchPersistsAndUpdatesMode(t *testing.T) {
	var captured string
	m := newFramedModel(t, FrameUI{
		Active: "work",
		Mode:   "solo",
		SwitchMode: func(mode string) error {
			captured = mode
			return nil
		},
	})
	s := runStatusCmd(t, m.modeSlash("orchestrator"))
	if captured != "orchestrator" {
		t.Errorf("SwitchMode captured %q, want orchestrator", captured)
	}
	if m.frame.Mode != "orchestrator" {
		t.Errorf("in-process Mode = %q, want orchestrator", m.frame.Mode)
	}
	if !strings.Contains(s.text, "orchestrator") {
		t.Errorf("expected confirmation; got %q", s.text)
	}
}

func TestModeSlash_NoOpOnSameMode(t *testing.T) {
	called := false
	m := newFramedModel(t, FrameUI{
		Active:     "personal",
		Mode:       "solo",
		SwitchMode: func(string) error { called = true; return nil },
	})
	runStatusCmd(t, m.modeSlash("solo"))
	if called {
		t.Error("SwitchMode should not fire when target equals current")
	}
}

func TestModeSlash_UnwiredWhenHookNil(t *testing.T) {
	m := newFramedModel(t, FrameUI{Active: "work", Mode: "solo"})
	s := runStatusCmd(t, m.modeSlash("orchestrator"))
	if s.kind != statusWarn {
		t.Errorf("kind = %v, want statusWarn", s.kind)
	}
}

func TestRenderHeader_IncludesModeWhenNonSolo(t *testing.T) {
	m := newFramedModel(t, FrameUI{
		Active: "work",
		Glyph:  "▣",
		Accent: "rust",
		Mode:   "orchestrator",
	})
	out := m.renderHeader(120)
	if !strings.Contains(out, "orchestrator") {
		t.Errorf("expected mode pill in header; got:\n%s", out)
	}
}

func TestWhoamiSlash_LegacySingleShelfEcho(t *testing.T) {
	m := newFramedModel(t, FrameUI{})
	s := runStatusCmd(t, m.whoamiSlash())
	if !strings.Contains(s.text, "no frame wired") {
		t.Errorf("legacy whoami echo wrong: %q", s.text)
	}
}

func TestWhoamiSlash_RendersFrameModeIdentity(t *testing.T) {
	m := newFramedModel(t, FrameUI{
		Active: "work",
		Mode:   "orchestrator",
		Capabilities: map[string]string{
			"calendar": "caldav",
		},
		Identity: func() (string, string) {
			return "anthropic", "claude-sonnet-4-6"
		},
	})
	s := runStatusCmd(t, m.whoamiSlash())
	for _, want := range []string{"work", "orchestrator", "anthropic", "claude-sonnet-4-6", "calendar=caldav"} {
		if !strings.Contains(s.text, want) {
			t.Errorf("missing %q in whoami: %q", want, s.text)
		}
	}
}

func TestWhoamiSlash_HandlesNilIdentity(t *testing.T) {
	m := newFramedModel(t, FrameUI{Active: "personal", Mode: "solo"})
	s := runStatusCmd(t, m.whoamiSlash())
	if !strings.Contains(s.text, "personal") || !strings.Contains(s.text, "solo") {
		t.Errorf("frame+mode missing: %q", s.text)
	}
}

// TestWhoamiSlash_AppendsTranscriptEntry is the regression test for
// the "/whoami appears to do nothing" field report: even when the
// footer status echo is invisible in a given terminal/theme combo,
// the inline transcript row must surface the same identity string so
// the slash is never a black hole. Pins three properties: a row is
// appended, the kind is entrySlashEcho (not entrySystemNote — that
// would render as a warn-colored error), and the body matches the
// status echo so the two surfaces stay in sync.
func TestWhoamiSlash_AppendsTranscriptEntry(t *testing.T) {
	m := newFramedModel(t, FrameUI{
		Active: "personal",
		Mode:   "solo",
		Identity: func() (string, string) {
			return "openrouter", "gemini-3.5-flash"
		},
	})
	startLen := len(m.transcript)
	s := runStatusCmd(t, m.whoamiSlash())

	if got := len(m.transcript) - startLen; got != 1 {
		t.Fatalf("transcript grew by %d, want 1", got)
	}
	row := m.transcript[len(m.transcript)-1]
	if row.kind != entrySlashEcho {
		t.Errorf("appended row kind = %d, want entrySlashEcho (%d)", row.kind, entrySlashEcho)
	}
	if row.text != s.text {
		t.Errorf("transcript row text = %q, want %q (must match status echo)", row.text, s.text)
	}
	for _, want := range []string{"personal", "solo", "openrouter", "gemini-3.5-flash"} {
		if !strings.Contains(row.text, want) {
			t.Errorf("transcript row missing %q: %q", want, row.text)
		}
	}
}

// TestWhoamiSlash_LegacyMode_AppendsTranscriptEntry covers the
// no-frame-wired path the same way: still gets a transcript echo so
// the user sees that /whoami did something.
func TestWhoamiSlash_LegacyMode_AppendsTranscriptEntry(t *testing.T) {
	m := newFramedModel(t, FrameUI{})
	startLen := len(m.transcript)
	s := runStatusCmd(t, m.whoamiSlash())

	if got := len(m.transcript) - startLen; got != 1 {
		t.Fatalf("transcript grew by %d, want 1", got)
	}
	row := m.transcript[len(m.transcript)-1]
	if row.kind != entrySlashEcho {
		t.Errorf("appended row kind = %d, want entrySlashEcho", row.kind)
	}
	if row.text != s.text {
		t.Errorf("transcript row text = %q, want %q", row.text, s.text)
	}
	if !strings.Contains(row.text, "no frame wired") {
		t.Errorf("legacy echo missing marker: %q", row.text)
	}
}

// TestWhoamiSlash_RenderedViewContainsEcho closes the loop with a
// full View() render: the transcript row's body must reach the
// rendered output so the user actually sees /whoami's reply. This is
// the assertion the original probe was missing — the previous test
// only checked m.transcript and m.status separately.
func TestWhoamiSlash_RenderedViewContainsEcho(t *testing.T) {
	m := newFramedModel(t, FrameUI{
		Active: "personal",
		Mode:   "solo",
		Identity: func() (string, string) {
			return "openrouter", "gemini-3.5-flash"
		},
	})
	m.width, m.height = 120, 30
	_ = m.whoamiSlash()
	view := m.View()
	for _, want := range []string{"personal", "solo", "gemini-3.5-flash"} {
		if !strings.Contains(view, want) {
			t.Errorf("rendered View missing %q from whoami echo", want)
		}
	}
}

// TestComposeWhoamiEcho_PureFunction pins the pure text composer so
// future callers (e.g. headless `please` mode) can reuse it without
// needing a full Model.
func TestComposeWhoamiEcho_PureFunction(t *testing.T) {
	got := composeWhoamiEcho(FrameUI{
		Active: "work",
		Mode:   "orchestrator",
		Capabilities: map[string]string{
			"calendar": "caldav",
			"notes":    "obsidian",
		},
		Identity: func() (string, string) {
			return "anthropic", "claude-sonnet-4-6"
		},
	})
	for _, want := range []string{
		"frame work",
		"orchestrator",
		"provider=anthropic",
		"model=claude-sonnet-4-6",
		"calendar=caldav",
		"notes=obsidian",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("compose missing %q: %q", want, got)
		}
	}
	// Capabilities should sort alphabetically so the echo is stable
	// across map-iteration order.
	if i, j := strings.Index(got, "calendar="), strings.Index(got, "notes="); i > j {
		t.Errorf("capabilities not sorted: %q", got)
	}
}

func TestRenderHeader_RendersModePillForSolo(t *testing.T) {
	// Contract update: the mode pill is now always rendered (even for
	// solo) so it has a stable click hitbox for the mouse-driven mode
	// switcher. Solo stays subtle so the visual weight roughly tracks
	// the prior "hide solo" behaviour.
	m := newFramedModel(t, FrameUI{
		Active: "personal",
		Glyph:  "◉",
		Accent: "cream",
		Mode:   "solo",
	})
	out := m.renderHeader(120)
	if !strings.Contains(out, "solo") {
		t.Errorf("solo mode pill should be rendered (click target); got:\n%s", out)
	}
}

func TestCapabilitiesSlash_RendersWiredCapabilities(t *testing.T) {
	m := newFramedModel(t, FrameUI{
		Active: "work",
		Capabilities: map[string]string{
			"calendar": "caldav",
			"email":    "fastmail-imap",
		},
	})
	s := runStatusCmd(t, m.capabilitiesSlash())
	if !strings.Contains(s.text, "calendar=caldav") {
		t.Errorf("missing calendar=caldav; got %q", s.text)
	}
	if !strings.Contains(s.text, "email=fastmail-imap") {
		t.Errorf("missing email entry; got %q", s.text)
	}
	// Sort assertion: calendar should come before email (alphabetic).
	calAt := strings.Index(s.text, "calendar")
	emailAt := strings.Index(s.text, "email")
	if !(calAt < emailAt) {
		t.Errorf("expected alphabetical order; got %q", s.text)
	}
}
