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

func newFramedModel(t *testing.T, ui FrameUI) *Model {
	t.Helper()
	m := New(stubLog{}, "test-agent", NewMemTextSource(), WithFrame(ui))
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
	if !strings.Contains(s.text, "next session") {
		t.Errorf("expected restart hint; got %q", s.text)
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
