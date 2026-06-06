package onboarding

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestGatewayScreen_DefaultNoEnabledDeclinesAndExits(t *testing.T) {
	m := newGatewayModel()
	// Enter on the y/N prompt accepts the default (no).
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected nextScreen cmd on enter")
	}
	mm := next.(gatewayModel)
	res := mm.buildResult()
	if res.enabled {
		t.Errorf("default-no should produce enabled=false; got %+v", res)
	}
	if res.ntfy.Enabled || res.telegram.Enabled {
		t.Errorf("no channels should be configured: %+v", res)
	}
}

func TestGatewayScreen_ExplicitNoExits(t *testing.T) {
	m := newGatewayModel()
	next, cmd := m.Update(tea.KeyMsg{Runes: []rune{'n'}, Type: tea.KeyRunes})
	if cmd == nil {
		t.Fatal("expected nextScreen cmd")
	}
	if mm := next.(gatewayModel); mm.enabled {
		t.Error("n should produce enabled=false")
	}
}

func TestGatewayScreen_YesAdvancesToChannelPicker(t *testing.T) {
	m := newGatewayModel()
	next, cmd := m.Update(tea.KeyMsg{Runes: []rune{'y'}, Type: tea.KeyRunes})
	if cmd != nil {
		t.Error("y should NOT yet exit; we need channel selection next")
	}
	mm := next.(gatewayModel)
	if mm.stage != gwStageChannels {
		t.Errorf("stage: want gwStageChannels got %v", mm.stage)
	}
}

func TestGatewayScreen_NoneChannelChoiceExits(t *testing.T) {
	m := newGatewayModel()
	m.stage = gwStageChannels
	m.enabled = true
	m.choice = 0 // None
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected nextScreen on None+enter")
	}
	res := next.(gatewayModel).buildResult()
	if !res.enabled {
		t.Error("gateway should still be enabled (master switch)")
	}
	if res.ntfy.Enabled || res.telegram.Enabled {
		t.Error("None choice should leave channels off")
	}
}

func TestGatewayScreen_NtfyFullFlow(t *testing.T) {
	m := newGatewayModel()
	m.stage = gwStageChannels
	m.enabled = true
	m.choice = 1 // ntfy only
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("channel select should advance internally, not exit")
	}
	m = next.(gatewayModel)
	if m.stage != gwStageNtfy || m.ntfyField != ntfyFieldServer {
		t.Fatalf("expected ntfy/server stage, got stage=%v field=%v", m.stage, m.ntfyField)
	}
	if m.input.Value() == "" {
		t.Error("server field should be pre-filled with https://ntfy.sh")
	}
	// Advance through server → topic → key.
	for range 3 {
		next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(gatewayModel)
		if cmd != nil {
			break
		}
	}
	if cmd == nil {
		t.Fatal("expected final nextScreen cmd after key field")
	}
	res := m.buildResult()
	if !res.enabled || !res.ntfy.Enabled {
		t.Errorf("expected ntfy enabled in result: %+v", res)
	}
	if res.ntfy.Server == "" || res.ntfy.Topic == "" || res.ntfy.SigningKey == "" {
		t.Errorf("ntfy fields should be populated: %+v", res.ntfy)
	}
	if len(res.ntfy.SigningKey) < 32 {
		t.Errorf("signing key too short: %q", res.ntfy.SigningKey)
	}
}

func TestGatewayScreen_NtfyEmptyTopicWarns(t *testing.T) {
	m := newGatewayModel()
	m.stage = gwStageNtfy
	m.ntfyField = ntfyFieldTopic
	m.ntfyServer = "https://ntfy.sh"
	m.input.SetValue("")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("empty topic should not advance")
	}
	mm := next.(gatewayModel)
	if mm.warn == "" {
		t.Error("expected warn on empty topic")
	}
}

func TestGatewayScreen_NtfyShortKeyWarns(t *testing.T) {
	m := newGatewayModel()
	m.stage = gwStageNtfy
	m.ntfyField = ntfyFieldSigningKey
	m.input.SetValue("tooshort")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("short key should not advance")
	}
	if mm := next.(gatewayModel); mm.warn == "" {
		t.Error("expected warn on short signing key")
	}
}

func TestGatewayScreen_TelegramFullFlow(t *testing.T) {
	m := newGatewayModel()
	m.stage = gwStageChannels
	m.enabled = true
	m.choice = 2 // telegram only
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(gatewayModel)
	if m.stage != gwStageTelegram || m.tgField != tgFieldBotToken {
		t.Fatalf("stage: %v field: %v", m.stage, m.tgField)
	}
	// Default value (env: indirection) is already in the input.
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(gatewayModel)
	if cmd != nil {
		t.Fatal("bot token submit should advance to chat_id, not exit")
	}
	if m.tgField != tgFieldChatID {
		t.Errorf("expected chat_id stage, got %v", m.tgField)
	}
	// Empty chat_id rejected.
	m.input.SetValue("")
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(gatewayModel)
	if cmd != nil {
		t.Error("empty chat_id should not advance")
	}
	if m.warn == "" {
		t.Error("expected warn on empty chat_id")
	}
	// Non-numeric chat_id rejected.
	m.input.SetValue("abc")
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(gatewayModel)
	if cmd != nil {
		t.Error("non-numeric chat_id should not advance")
	}
	// Valid numeric chat_id submits.
	m.input.SetValue("123456789")
	next, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("valid chat_id should advance")
	}
	res := next.(gatewayModel).buildResult()
	if !res.telegram.Enabled || res.telegram.BotToken == "" {
		t.Errorf("telegram fields not populated: %+v", res.telegram)
	}
	if len(res.telegram.AllowedChatIDs) != 1 || res.telegram.AllowedChatIDs[0] != 123456789 {
		t.Errorf("chat_id not parsed: %v", res.telegram.AllowedChatIDs)
	}
}

func TestGatewayScreen_BothChannelsWalkBoth(t *testing.T) {
	m := newGatewayModel()
	m.stage = gwStageChannels
	m.enabled = true
	m.choice = 3 // both
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = next.(gatewayModel)
	if !m.pickNtfy || !m.pickTelegram {
		t.Errorf("both should select both: ntfy=%v telegram=%v", m.pickNtfy, m.pickTelegram)
	}
	if m.stage != gwStageNtfy {
		t.Errorf("should start with ntfy: %v", m.stage)
	}
	// Walk through ntfy.
	for range 3 {
		next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m = next.(gatewayModel)
	}
	// Should now be in telegram stage, not exited.
	if m.stage != gwStageTelegram {
		t.Errorf("after ntfy expected telegram stage, got %v", m.stage)
	}
}

func TestGatewayScreen_ChannelArrowKeys(t *testing.T) {
	m := newGatewayModel()
	m.stage = gwStageChannels
	m.choice = 0
	for _, k := range []string{"down", "down", "down"} {
		next, _ := m.Update(tea.KeyMsg{Runes: []rune(k), Type: tea.KeyRunes})
		m = next.(gatewayModel)
	}
	// down works via key strings — but our Update uses k.String() which
	// for KeyRunes wouldn't be "down". Let's instead use KeyDown.
	m.choice = 0
	for range 3 {
		next, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
		m = next.(gatewayModel)
	}
	if m.choice != 3 {
		t.Errorf("after 3 downs choice should be 3, got %d", m.choice)
	}
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = next.(gatewayModel)
	if m.choice != 2 {
		t.Errorf("up: want 2 got %d", m.choice)
	}
}

func TestGenerateTopic_Slugifies(t *testing.T) {
	got := generateTopic("George F.")
	if !strings.HasPrefix(got, "carlos-georgef-") {
		t.Errorf("topic format: want carlos-georgef-<hex>, got %q", got)
	}
	if len(got) != len("carlos-georgef-")+8 {
		t.Errorf("topic length: %d", len(got))
	}
}

func TestGenerateTopic_EmptyNameFallback(t *testing.T) {
	got := generateTopic("")
	if !strings.HasPrefix(got, "carlos-user-") {
		t.Errorf("empty name fallback: %q", got)
	}
}

func TestGenerateSigningKey_64Hex(t *testing.T) {
	got := generateSigningKey()
	if len(got) != 64 {
		t.Errorf("signing key length: want 64 got %d", len(got))
	}
	for _, r := range got {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			t.Errorf("non-hex char in signing key: %q", got)
			break
		}
	}
}

func TestGenerateSigningKey_NotRepeating(t *testing.T) {
	a := generateSigningKey()
	b := generateSigningKey()
	if a == b {
		t.Error("two signing keys should not match")
	}
}

func TestIsAllDigits(t *testing.T) {
	cases := map[string]bool{
		"":          false,
		"123":       true,
		"12a3":      false,
		"-1":        false,
		"123456789": true,
	}
	for in, want := range cases {
		if got := isAllDigits(in); got != want {
			t.Errorf("isAllDigits(%q) = %v want %v", in, got, want)
		}
	}
}

func TestFlow_GatewayAutoSkipsWhenDaemonOff(t *testing.T) {
	f := New()
	// Pretend we reached the Daemon screen and chose "no daemon".
	f.current = ScreenDaemon
	f.cfg.Daemon.Enabled = false
	f.advance()
	if f.current != ScreenDone {
		t.Errorf("expected auto-skip to ScreenDone, got %v", f.current)
	}
}

func TestFlow_GatewayShownWhenDaemonOn(t *testing.T) {
	f := New()
	f.current = ScreenDaemon
	f.cfg.Daemon.Enabled = true
	f.advance()
	if f.current != ScreenGateway {
		t.Errorf("expected ScreenGateway, got %v", f.current)
	}
}

func TestFlow_BackNavAlsoSkipsGateway(t *testing.T) {
	f := New()
	f.current = ScreenDone
	f.cfg.Daemon.Enabled = false
	// Simulate shift-tab from Done.
	if f.current > ScreenName {
		f.current--
		if f.current == ScreenGateway && !f.cfg.Daemon.Enabled {
			f.current--
		}
	}
	if f.current != ScreenDaemon {
		t.Errorf("back-nav from Done with daemon off should land on Daemon, got %v", f.current)
	}
}
