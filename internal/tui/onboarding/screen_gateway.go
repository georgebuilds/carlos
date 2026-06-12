package onboarding

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/config"
)

// gatewayStage tracks where we are in the gateway sub-flow. The screen
// is composite: one Screen enum slot at the Flow level, multiple inner
// stages here. Modeled the same way provider screen handles its own
// per-provider walk.
//
// gwStageDecide is the first-launch gate: the user picks "set up
// later" (default) or "step through now". Picking later lands the user
// at done; now drops into the legacy gwStageEnable → channels → adapter
// flow.
type gatewayStage int

const (
	gwStageDecide gatewayStage = iota
	gwStageEnable
	gwStageChannels
	gwStageNtfy
	gwStageTelegram
)

// ntfyField indexes the multi-field ntfy form within gwStageNtfy. We
// edit one field at a time and advance the cursor on enter.
type ntfyField int

const (
	ntfyFieldServer ntfyField = iota
	ntfyFieldTopic
	ntfyFieldSigningKey
	ntfyFieldCount
)

// telegramField is the analogue for the telegram sub-form.
type telegramField int

const (
	tgFieldBotToken telegramField = iota
	tgFieldChatID
	tgFieldCount
)

// gatewayModel composes a six-screen onboarding step into one tea.Model.
// Sub-stages are walked in order; selections gate which sub-stages
// actually run (no ntfy → skip the ntfy field walk, etc.).
//
// Auto-skip: Flow.advance bypasses this screen entirely when the daemon
// is disabled because the gateway is daemon-owned. The model still
// exists in the Flow so the back-nav stays uniform.
type gatewayModel struct {
	stage gatewayStage

	// gwStageEnable / gwStageChannels collected choices.
	enabled      bool
	pickNtfy     bool
	pickTelegram bool
	choice       int // index for radio-style pickers

	// ntfy field state.
	ntfyField  ntfyField
	ntfyServer string
	ntfyTopic  string
	ntfyKey    string

	// telegram field state.
	tgField    telegramField
	tgBotToken string
	tgChatID   string

	// Active text input - re-used across stages, re-seeded on stage
	// transitions. We don't keep one input per field because the
	// memory footprint is trivial and the value lives in the model
	// fields above on advance.
	input textinput.Model

	// One-shot non-fatal validation hint shown beneath the prompt.
	warn string
}

// gatewayResult carries everything the Flow needs to populate
// cfg.Gateway when this screen finishes.
type gatewayResult struct {
	enabled  bool
	ntfy     config.NtfyGatewayConfig
	telegram config.TelegramConfig
}

func newGatewayModel() gatewayModel {
	ti := textinput.New()
	ti.CharLimit = 512
	ti.Width = 56
	ti.Prompt = "> "
	return gatewayModel{
		stage: gwStageDecide,
		input: ti,
	}
}

// NewGatewayStandalone constructs a gatewayModel that bypasses the
// "later or now" gate so `carlos gateway add` can drive the same wizard
// without re-asking the question.
func NewGatewayStandalone() gatewayModel {
	m := newGatewayModel()
	m.stage = gwStageEnable
	return m
}

func (m gatewayModel) Init() tea.Cmd { return textinput.Blink }

// seedForCurrentField is called whenever we transition into a stage or
// field that uses the text input, so the cursor lands on a useful
// default. Re-using one input model keeps the screen state compact.
func (m *gatewayModel) seedForCurrentField(userName string) {
	m.warn = ""
	switch m.stage {
	case gwStageNtfy:
		switch m.ntfyField {
		case ntfyFieldServer:
			if m.ntfyServer == "" {
				m.ntfyServer = "https://ntfy.sh"
			}
			m.input.SetValue(m.ntfyServer)
			m.input.Placeholder = "https://ntfy.sh"
		case ntfyFieldTopic:
			if m.ntfyTopic == "" {
				m.ntfyTopic = generateTopic(userName)
			}
			m.input.SetValue(m.ntfyTopic)
			m.input.Placeholder = "carlos-<user>-<random>"
		case ntfyFieldSigningKey:
			if m.ntfyKey == "" {
				m.ntfyKey = generateSigningKey()
			}
			m.input.SetValue(m.ntfyKey)
			m.input.Placeholder = "32-byte hex"
		}
	case gwStageTelegram:
		switch m.tgField {
		case tgFieldBotToken:
			if m.tgBotToken == "" {
				m.tgBotToken = "env:CARLOS_TELEGRAM_TOKEN"
			}
			m.input.SetValue(m.tgBotToken)
			m.input.Placeholder = "env:CARLOS_TELEGRAM_TOKEN"
		case tgFieldChatID:
			m.input.SetValue(m.tgChatID)
			m.input.Placeholder = "123456789"
		}
	}
	m.input.Focus()
}

// generateTopic builds the default ntfy topic. Format:
//
//	carlos-<userslug>-<8hex>
//
// The user slug is lowercased + non-alphanum stripped so "Boss Lady"
// becomes "bosslady". A short random suffix keeps the topic effectively
// secret on public ntfy.sh - anyone with the topic name can subscribe,
// so randomness here is the security boundary against drive-by
// readers.
func generateTopic(userName string) string {
	slug := strings.ToLower(strings.TrimSpace(userName))
	clean := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r
		}
		return -1
	}, slug)
	if clean == "" {
		clean = "user"
	}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	return fmt.Sprintf("carlos-%s-%s", clean, hex.EncodeToString(suffix))
}

// generateSigningKey returns 32 random bytes hex-encoded. Matches the
// ntfy adapter's expected key length and produces a 64-character ascii
// string the user can copy from the screen into a password manager.
func generateSigningKey() string {
	buf := make([]byte, 32)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}

func (m gatewayModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	k, isKey := msg.(tea.KeyMsg)

	switch m.stage {
	case gwStageDecide:
		if isKey {
			switch strings.ToLower(k.String()) {
			case "n":
				// Step through now: drop into the legacy enable
				// prompt + channel/adapter wizard.
				m.stage = gwStageEnable
				return m, nil
			case "l", "enter":
				// Default: set up later. Gateway stays disabled
				// and lands at done; user can finish via
				// `carlos gateway add` whenever.
				m.enabled = false
				return m, nextScreen(m.buildResult())
			}
		}
		return m, nil

	case gwStageEnable:
		if isKey {
			switch strings.ToLower(k.String()) {
			case "y":
				m.enabled = true
				m.stage = gwStageChannels
				m.choice = 0
				return m, nil
			case "n":
				m.enabled = false
				return m, nextScreen(m.buildResult())
			case "enter":
				// Default no.
				return m, nextScreen(m.buildResult())
			}
		}
		return m, nil

	case gwStageChannels:
		if isKey {
			switch k.String() {
			case "up", "k":
				if m.choice > 0 {
					m.choice--
				}
				return m, nil
			case "down", "j":
				if m.choice < 3 {
					m.choice++
				}
				return m, nil
			case "1":
				m.choice = 0
				return m, nil
			case "2":
				m.choice = 1
				return m, nil
			case "3":
				m.choice = 2
				return m, nil
			case "4":
				m.choice = 3
				return m, nil
			case "enter":
				m.pickNtfy = m.choice == 1 || m.choice == 3
				m.pickTelegram = m.choice == 2 || m.choice == 3
				if !m.pickNtfy && !m.pickTelegram {
					// User picked "none" - gateway stays enabled but
					// no adapters are configured. That's a valid
					// state (notifications go nowhere until they
					// later edit YAML).
					return m, nextScreen(m.buildResult())
				}
				return m, m.advanceFromChannels()
			}
		}
		return m, nil

	case gwStageNtfy:
		if isKey && k.String() == "enter" {
			val := strings.TrimSpace(m.input.Value())
			switch m.ntfyField {
			case ntfyFieldServer:
				if val == "" {
					val = "https://ntfy.sh"
				}
				m.ntfyServer = val
				m.ntfyField = ntfyFieldTopic
				m.seedForCurrentField("")
				return m, nil
			case ntfyFieldTopic:
				if val == "" {
					m.warn = "topic cannot be empty"
					return m, nil
				}
				m.ntfyTopic = val
				m.ntfyField = ntfyFieldSigningKey
				m.seedForCurrentField("")
				return m, nil
			case ntfyFieldSigningKey:
				if len(val) < 32 {
					m.warn = "signing key must be at least 32 chars (use the suggested default unless you have a reason)"
					return m, nil
				}
				m.ntfyKey = val
				if m.pickTelegram {
					m.stage = gwStageTelegram
					m.tgField = tgFieldBotToken
					m.seedForCurrentField("")
					return m, nil
				}
				return m, nextScreen(m.buildResult())
			}
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd

	case gwStageTelegram:
		if isKey && k.String() == "enter" {
			val := strings.TrimSpace(m.input.Value())
			switch m.tgField {
			case tgFieldBotToken:
				if val == "" {
					m.warn = "bot token required (paste the value or env:VARNAME)"
					return m, nil
				}
				m.tgBotToken = val
				m.tgField = tgFieldChatID
				m.seedForCurrentField("")
				return m, nil
			case tgFieldChatID:
				if val == "" {
					m.warn = "chat_id required (find via @userinfobot on Telegram)"
					return m, nil
				}
				if !isAllDigits(val) {
					m.warn = "chat_id should be a positive integer"
					return m, nil
				}
				m.tgChatID = val
				return m, nextScreen(m.buildResult())
			}
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	}
	return m, nil
}

// advanceFromChannels prepares the next sub-stage after the channel
// picker. Wraps the Update transition so the caller stays compact.
func (m *gatewayModel) advanceFromChannels() tea.Cmd {
	if m.pickNtfy {
		m.stage = gwStageNtfy
		m.ntfyField = ntfyFieldServer
		m.seedForCurrentField("")
		return nil
	}
	m.stage = gwStageTelegram
	m.tgField = tgFieldBotToken
	m.seedForCurrentField("")
	return nil
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// buildResult collapses the in-progress fields into a gatewayResult the
// Flow can assign onto cfg.Gateway.
func (m gatewayModel) buildResult() gatewayResult {
	res := gatewayResult{enabled: m.enabled}
	if m.pickNtfy {
		res.ntfy = config.NtfyGatewayConfig{
			Enabled:    true,
			Server:     m.ntfyServer,
			Topic:      m.ntfyTopic,
			SigningKey: m.ntfyKey,
		}
	}
	if m.pickTelegram {
		var allowed []int64
		var id int64
		_, _ = fmt.Sscanf(m.tgChatID, "%d", &id)
		if id != 0 {
			allowed = []int64{id}
		}
		res.telegram = config.TelegramConfig{
			Enabled:        true,
			BotToken:       m.tgBotToken,
			AllowedChatIDs: allowed,
		}
	}
	return res
}

func (m gatewayModel) View() string {
	var sb strings.Builder
	switch m.stage {
	case gwStageDecide:
		sb.WriteString(styleHint.Render(
			"the gateway routes notifications, approvals, and conversation to ntfy + telegram."))
		sb.WriteString("\n")
		sb.WriteString(styleHint.Render(
			"the wizard is a few screens long. you can step through now or finish later."))
		sb.WriteString("\n\n")
		sb.WriteString("set up the gateway?")
		sb.WriteString("\n\n")
		sb.WriteString("   ")
		sb.WriteString(styleKey.Render("[enter]"))
		sb.WriteString(styleHint.Render(" set up later (run `carlos gateway add`)"))
		sb.WriteString("\n")
		sb.WriteString("   ")
		sb.WriteString(styleKey.Render("[n]"))
		sb.WriteString(styleHint.Render("     step through now"))

	case gwStageEnable:
		sb.WriteString(styleHint.Render(
			"Push notifications + HITL approvals from your phone (ntfy, Telegram). Requires the daemon."))
		sb.WriteString("\n")
		sb.WriteString(styleHint.Render(
			"Off by default. You can flip this later by editing ~/.carlos/config.yaml."))
		sb.WriteString("\n\n")
		sb.WriteString("Enable the gateway? ")
		sb.WriteString(lipgloss.NewStyle().Foreground(colorMuted).Render("[y/N]"))

	case gwStageChannels:
		sb.WriteString(styleHint.Render("Pick the channels you want to route through."))
		sb.WriteString("\n\n")
		opts := []struct{ label, sub string }{
			{"None", "leave routing empty for now; configure later"},
			{"ntfy only", "fire-and-forget push + 3-button HITL (needs Tailscale Funnel for inbound)"},
			{"Telegram only", "rich text + inline keyboards via Bot API (no public listener needed)"},
			{"Both", "ntfy for notifications, Telegram for approvals + conversation"},
		}
		for i, o := range opts {
			marker := lipgloss.NewStyle().Foreground(colorMuted).Render("(  )")
			label := o.label
			if i == m.choice {
				marker = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render("(●)")
				label = lipgloss.NewStyle().Foreground(colorAccent).Bold(true).Render(o.label)
			}
			sb.WriteString(fmt.Sprintf("  %s  %s\n", marker, label))
			sb.WriteString(fmt.Sprintf("       %s\n\n", styleHint.Render(o.sub)))
		}
		sb.WriteString(styleHint.Render("↑/↓ or 1-4 to pick · enter to continue"))

	case gwStageNtfy:
		sb.WriteString(stylePrompt.Render("ntfy configuration"))
		sb.WriteString("\n\n")
		switch m.ntfyField {
		case ntfyFieldServer:
			sb.WriteString(styleHint.Render("ntfy server URL. Public ntfy.sh works out of the box."))
		case ntfyFieldTopic:
			sb.WriteString(styleHint.Render(
				"Topic name. Treat as a secret - anyone with the topic can subscribe."))
			sb.WriteString("\n")
			sb.WriteString(styleHint.Render(
				"The default is randomized; press enter to keep it."))
		case ntfyFieldSigningKey:
			sb.WriteString(styleHint.Render(
				"HMAC key for signing action-button URLs (prevents forged decisions)."))
			sb.WriteString("\n")
			sb.WriteString(styleHint.Render(
				"Auto-generated above. Save a copy somewhere - you'll need it if you ever rebuild config."))
		}
		sb.WriteString("\n\n")
		sb.WriteString(m.input.View())
		sb.WriteString("\n")
		if m.warn != "" {
			errStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
			sb.WriteString("\n")
			sb.WriteString(errStyle.Render(m.warn))
		}

	case gwStageTelegram:
		sb.WriteString(stylePrompt.Render("Telegram configuration"))
		sb.WriteString("\n\n")
		switch m.tgField {
		case tgFieldBotToken:
			sb.WriteString(styleHint.Render(
				"Bot token from @BotFather. Use env:CARLOS_TELEGRAM_TOKEN to indirect through an env var."))
		case tgFieldChatID:
			sb.WriteString(styleHint.Render(
				"Your chat_id - DM @userinfobot from Telegram to find it (a positive integer)."))
		}
		sb.WriteString("\n\n")
		sb.WriteString(m.input.View())
		sb.WriteString("\n")
		if m.warn != "" {
			errStyle := lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
			sb.WriteString("\n")
			sb.WriteString(errStyle.Render(m.warn))
		}
	}
	return sb.String()
}
