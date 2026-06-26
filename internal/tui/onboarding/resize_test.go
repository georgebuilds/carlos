package onboarding

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestFitInputWidth(t *testing.T) {
	cases := []struct {
		name       string
		paneW, max int
		want       int
	}{
		{"clamps to max on a roomy pane", 200, 48, 48},
		{"shrinks to fit a narrow pane", 30, 48, 30 - inputChrome},
		{"never below the floor", 5, 48, minInputWidth},
		{"exact fit minus chrome", 40, 60, 40 - inputChrome},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := fitInputWidth(tc.paneW, tc.max); got != tc.want {
				t.Errorf("fitInputWidth(%d, %d) = %d, want %d", tc.paneW, tc.max, got, tc.want)
			}
		})
	}
}

// TestInputModelsSetWidth confirms every input-bearing screen shrinks its
// textinput on a narrow pane and caps at its design width on a roomy one.
// This is the regression guard for the resize bug: before the fix the
// inputs kept their constructed width and overflowed.
func TestInputModelsSetWidth(t *testing.T) {
	type widthSetter interface{ setWidth(int) }
	cases := []struct {
		name string
		make func() (widthSetter, func() int)
		max  int
	}{
		{"name", func() (widthSetter, func() int) {
			m := newNameModel("Boss")
			return &m, func() int { return m.input.Width }
		}, nameInputMax},
		{"provider", func() (widthSetter, func() int) {
			m := newProviderModel()
			return &m, func() int { return m.input.Width }
		}, providerInputMax},
		{"model", func() (widthSetter, func() int) {
			m := newModelModel()
			return &m, func() int { return m.input.Width }
		}, modelInputMax},
		{"vault", func() (widthSetter, func() int) {
			m := newVaultModel()
			return &m, func() int { return m.input.Width }
		}, vaultInputMax},
		{"gateway", func() (widthSetter, func() int) {
			m := newGatewayModel()
			return &m, func() int { return m.input.Width }
		}, gatewayInputMax},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, width := tc.make()
			// Roomy pane: cap at the design width.
			s.setWidth(500)
			if got := width(); got != tc.max {
				t.Errorf("%s.setWidth(500) -> width %d, want max %d", tc.name, got, tc.max)
			}
			// Narrow pane: shrink to fit.
			s.setWidth(28)
			if got := width(); got != fitInputWidth(28, tc.max) {
				t.Errorf("%s.setWidth(28) -> width %d, want %d", tc.name, got, fitInputWidth(28, tc.max))
			}
			if got := width(); got >= tc.max {
				t.Errorf("%s did not shrink on a narrow pane: width %d (max %d)", tc.name, got, tc.max)
			}
		})
	}
}

// TestFlow_ResizeShrinksProviderInput drives a full Flow through a resize
// and confirms the live render re-fits the provider input (design width
// 60) down to the pane at an 80-column terminal, where the right pane is
// narrower than 60. Before the fix the input stayed at 60 and overflowed.
func TestFlow_ResizeShrinksProviderInput(t *testing.T) {
	f := NewWithOptions(Options{StartingScreen: ScreenProvider})
	f, _ = updateFlow(f, tea.WindowSizeMsg{Width: 120, Height: 40})
	_ = f.View()
	wide := f.provider.input.Width

	f, _ = updateFlow(f, tea.WindowSizeMsg{Width: minTerminalW, Height: minTerminalH})
	_ = f.View()
	narrow := f.provider.input.Width

	if narrow >= wide {
		t.Errorf("provider input did not shrink on resize: wide=%d narrow=%d", wide, narrow)
	}
	if narrow < minInputWidth {
		t.Errorf("provider input shrank below the floor: %d < %d", narrow, minInputWidth)
	}
}
