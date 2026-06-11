package onboarding

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestGatewayView_EnableStageRenders covers the legacy enable prompt's
// View body (reached via the decide gate's [n] path).
func TestGatewayView_EnableStageRenders(t *testing.T) {
	m := NewGatewayStandalone() // seeded at gwStageEnable
	out := stripStyle(m.View())
	for _, want := range []string{"Enable the gateway?", "[y/N]", "daemon"} {
		if !strings.Contains(out, want) {
			t.Errorf("enable-stage view missing %q; got:\n%s", want, out)
		}
	}
}

// TestGatewayView_ChannelsStageRendersAllOptions exercises the
// radio-style channel picker body and the selected-marker branch.
func TestGatewayView_ChannelsStageRendersAllOptions(t *testing.T) {
	m := newGatewayModel()
	m.stage = gwStageChannels
	m.choice = 2 // Telegram only highlighted
	out := stripStyle(m.View())
	for _, want := range []string{"None", "ntfy only", "Telegram only", "Both", "1-4 to pick"} {
		if !strings.Contains(out, want) {
			t.Errorf("channels view missing %q; got:\n%s", want, out)
		}
	}
	// The selected option uses the filled bullet marker.
	if !strings.Contains(out, "(●)") {
		t.Errorf("channels view should mark the selected choice with a filled bullet; got:\n%s", out)
	}
}

// TestGatewayView_NtfyStageRendersEachField walks every ntfy field and
// confirms its hint copy renders. This is the large uncovered View arm.
func TestGatewayView_NtfyStageRendersEachField(t *testing.T) {
	cases := []struct {
		field ntfyField
		want  string
	}{
		{ntfyFieldServer, "ntfy server URL"},
		{ntfyFieldTopic, "Topic name"},
		{ntfyFieldSigningKey, "HMAC key"},
	}
	for _, c := range cases {
		m := newGatewayModel()
		m.stage = gwStageNtfy
		m.ntfyField = c.field
		out := stripStyle(m.View())
		if !strings.Contains(out, "ntfy configuration") {
			t.Errorf("field %v: missing ntfy header", c.field)
		}
		if !strings.Contains(out, c.want) {
			t.Errorf("ntfy field %v view missing %q; got:\n%s", c.field, c.want, out)
		}
	}
}

// TestGatewayView_NtfyStageShowsWarn covers the warn branch of the ntfy
// View arm.
func TestGatewayView_NtfyStageShowsWarn(t *testing.T) {
	m := newGatewayModel()
	m.stage = gwStageNtfy
	m.ntfyField = ntfyFieldTopic
	m.warn = "topic cannot be empty"
	out := stripStyle(m.View())
	if !strings.Contains(out, "topic cannot be empty") {
		t.Errorf("ntfy view should render warn; got:\n%s", out)
	}
}

// TestGatewayView_TelegramStageRendersEachField covers both telegram
// field hint arms.
func TestGatewayView_TelegramStageRendersEachField(t *testing.T) {
	cases := []struct {
		field telegramField
		want  string
	}{
		{tgFieldBotToken, "Bot token from @BotFather"},
		{tgFieldChatID, "chat_id"},
	}
	for _, c := range cases {
		m := newGatewayModel()
		m.stage = gwStageTelegram
		m.tgField = c.field
		out := stripStyle(m.View())
		if !strings.Contains(out, "Telegram configuration") {
			t.Errorf("field %v: missing telegram header", c.field)
		}
		if !strings.Contains(out, c.want) {
			t.Errorf("telegram field %v view missing %q; got:\n%s", c.field, c.want, out)
		}
	}
}

// TestGatewayView_TelegramStageShowsWarn covers the warn branch of the
// telegram View arm.
func TestGatewayView_TelegramStageShowsWarn(t *testing.T) {
	m := newGatewayModel()
	m.stage = gwStageTelegram
	m.tgField = tgFieldChatID
	m.warn = "chat_id required"
	out := stripStyle(m.View())
	if !strings.Contains(out, "chat_id required") {
		t.Errorf("telegram view should render warn; got:\n%s", out)
	}
}

// TestGatewayUpdate_ChannelsNumberKeys covers the 1-4 direct-select keys
// in the channel picker.
func TestGatewayUpdate_ChannelsNumberKeys(t *testing.T) {
	cases := map[string]int{"1": 0, "2": 1, "3": 2, "4": 3}
	for key, want := range cases {
		m := newGatewayModel()
		m.stage = gwStageChannels
		next, _ := m.Update(tea.KeyMsg{Runes: []rune(key), Type: tea.KeyRunes})
		if got := next.(gatewayModel).choice; got != want {
			t.Errorf("key %q: choice = %d want %d", key, got, want)
		}
	}
}

// TestGatewayUpdate_ChannelsArrowClamps verifies up at the top and down
// at the bottom don't run off the option list.
func TestGatewayUpdate_ChannelsArrowClamps(t *testing.T) {
	m := newGatewayModel()
	m.stage = gwStageChannels
	m.choice = 0
	// Up from the top is a no-op (clamped at 0).
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := next.(gatewayModel).choice; got != 0 {
		t.Errorf("up at top should clamp to 0; got %d", got)
	}
	// Down past the bottom (choice 3) is a no-op.
	m.choice = 3
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := next.(gatewayModel).choice; got != 3 {
		t.Errorf("down at bottom should clamp to 3; got %d", got)
	}
}

// TestGatewayUpdate_DecideGateLowercaseN covers the lowercasing of the
// decide-gate key match (uppercase N should also drop into enable).
func TestGatewayUpdate_DecideGateUppercaseN(t *testing.T) {
	m := newGatewayModel()
	next, cmd := m.Update(tea.KeyMsg{Runes: []rune{'N'}, Type: tea.KeyRunes})
	if cmd != nil {
		t.Error("uppercase N on decide gate should not advance")
	}
	if next.(gatewayModel).stage != gwStageEnable {
		t.Errorf("uppercase N should drop into enable stage; got %v", next.(gatewayModel).stage)
	}
}

// TestGatewayUpdate_DecideGateL covers the explicit [l] "later" key on
// the decide gate (distinct from [enter]).
func TestGatewayUpdate_DecideGateL(t *testing.T) {
	m := newGatewayModel()
	next, cmd := m.Update(tea.KeyMsg{Runes: []rune{'l'}, Type: tea.KeyRunes})
	if cmd == nil {
		t.Fatal("[l] on decide gate should advance (set up later)")
	}
	if next.(gatewayModel).enabled {
		t.Error("[l] should leave gateway disabled")
	}
}

// TestGatewayUpdate_EnableExplicitYesThenInputRouting verifies that once
// in a text-input stage, non-enter keys route to the textinput rather
// than being swallowed.
func TestGatewayUpdate_NtfyServerInputRouting(t *testing.T) {
	m := newGatewayModel()
	m.stage = gwStageNtfy
	m.ntfyField = ntfyFieldServer
	m.seedForCurrentField("")
	m.input.SetValue("")
	// Type a character; it should land in the input value.
	next, _ := m.Update(tea.KeyMsg{Runes: []rune{'x'}, Type: tea.KeyRunes})
	if got := next.(gatewayModel).input.Value(); got != "x" {
		t.Errorf("typed rune should route to textinput; value = %q", got)
	}
}

// TestGatewayUpdate_NtfyServerEmptyDefaults verifies the server field's
// empty-input default substitution.
func TestGatewayUpdate_NtfyServerEmptyDefaults(t *testing.T) {
	m := newGatewayModel()
	m.stage = gwStageNtfy
	m.ntfyField = ntfyFieldServer
	m.input.SetValue("")
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	mm := next.(gatewayModel)
	if mm.ntfyServer != "https://ntfy.sh" {
		t.Errorf("empty server should default to https://ntfy.sh; got %q", mm.ntfyServer)
	}
	if mm.ntfyField != ntfyFieldTopic {
		t.Errorf("should advance to topic field; got %v", mm.ntfyField)
	}
}

// TestGatewayUpdate_TelegramBotTokenEmptyWarns covers the empty bot
// token rejection arm.
func TestGatewayUpdate_TelegramBotTokenEmptyWarns(t *testing.T) {
	m := newGatewayModel()
	m.stage = gwStageTelegram
	m.tgField = tgFieldBotToken
	m.input.SetValue("")
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Error("empty bot token should not advance")
	}
	if next.(gatewayModel).warn == "" {
		t.Error("empty bot token should warn")
	}
}

// TestGatewayBuildResult_TelegramZeroChatID verifies a zero/unparseable
// chat_id yields no AllowedChatIDs entry.
func TestGatewayBuildResult_TelegramZeroChatID(t *testing.T) {
	m := newGatewayModel()
	m.pickTelegram = true
	m.tgBotToken = "tok"
	m.tgChatID = "0" // parses to 0 → no allowed list
	res := m.buildResult()
	if len(res.telegram.AllowedChatIDs) != 0 {
		t.Errorf("chat_id 0 should produce no allowed IDs; got %v", res.telegram.AllowedChatIDs)
	}
	if !res.telegram.Enabled {
		t.Error("telegram should still be marked enabled")
	}
}
