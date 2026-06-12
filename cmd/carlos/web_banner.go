// web_banner.go - the launch banner for `carlos web`.
//
// Replaces the plain three-line stderr printf with a bordered panel
// styled as a sibling of the `carlos please` / `carlos research`
// status boxes (please_status.go, research_status.go) and the
// farewell panel (internal/farewell): RoundedBorder, accent edge,
// Padding(0,1), width clamped to [50,100] - 2 columns of breathing
// room. Static print-once output, no bubbletea; there is no ongoing
// work to animate and the TUI research note is explicit that motion
// must communicate state ("no decorative motion").
//
// From the same research note (personal/projects/carlos/research/
// 2026-06-05 How to Make a TUI Feel Awesome in 2026.md):
//
//   - "OSC 8 hyperlinks on any file path or URL you emit" - the URL
//     is wrapped in an OSC 8 hyperlink (clickable in iTerm2, Kitty,
//     Ghostty, WezTerm, Windows Terminal, VS Code). Verified
//     empirically that this lipgloss version measures OSC 8 as
//     zero-width, so the border math stays intact.
//   - "restraint and graceful degradation beat ornament" - when
//     stderr is not a TTY, or the terminal is too narrow to hold the
//     URL on one line inside the box, we fall back to the original
//     plain three-line form. The URL stays alone on its own line in
//     both shapes so scripts can grep it out.
//   - "respect NO_COLOR" - inherited from theme.Load; a monochrome
//     palette also suppresses the OSC 8 wrap (NO_COLOR users are
//     asking for inert plain text, not invisible escapes).
//
// Visual shape (interactive, wide TTY):
//
//	╭──────────────────────────────────────────────────────────────╮
//	│ 🧢 Web interface is ready to go! · interactive               │
//	│    http://127.0.0.1:7777/#token=4f8a…                        │
//	│    the token lives only in this URL; relaunch reprints it ·  │
//	╰──────────────────────────────────────────────────────────────╯
//
// The 🧢 rides inline with the headline - the same placement the
// farewell panel and the chat agent card ("🧢 agent · running") use;
// no shipped surface interrupts the border with a title tag.
package main

import (
	"github.com/charmbracelet/lipgloss"

	"github.com/georgebuilds/carlos/internal/config"
	"github.com/georgebuilds/carlos/internal/theme"
)

// webBannerHeadline is the panel's first line. George's words.
const webBannerHeadline = "Web interface is ready to go!"

// webBannerURLIndent aligns the URL + hint lines under the headline
// text (the 🧢 occupies two cells plus a space).
const webBannerURLIndent = "   "

// webOSC8 wraps text in an OSC 8 hyperlink pointing at url. Three-line
// duplicate of internal/tui/chat/linkify.go's osc8 - that helper is
// package-internal to chat and a launch banner is not worth widening
// chat's API surface for. Zero-width under lipgloss.Width, so the
// panel layout math is undisturbed.
func webOSC8(url, text string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// webBannerPalette resolves the theme palette for the banner from
// config, honoring NO_COLOR / COLORFGBG via theme.Load. Same shape as
// loadPickerPalette but takes the already-loaded cfg runWeb holds
// rather than re-reading the config file.
func webBannerPalette(cfg *config.Config) theme.Palette {
	var opts theme.Options
	if cfg != nil {
		opts.ForcedVariant = cfg.Theme.Variant
		opts.AccentOverride = cfg.Theme.Accent
	}
	return theme.Load(opts)
}

// webBanner renders the `carlos web` launch banner. Pure function over
// its inputs so every branch is testable without a real TTY.
//
// Fallback matrix:
//
//   - stderr not a TTY (piped, redirected)   -> plain three-line form
//   - TTY too narrow to hold the URL un-wrapped inside the box
//     (the URL must stay copyable; never wrap it mid-token) -> plain
//   - NO_COLOR -> bordered panel, monochrome, no OSC 8 hyperlink
//   - otherwise -> bordered accent panel with an OSC 8 linked URL
//
// mode is "interactive" or "read-only" - same vocabulary the old
// banner printed.
func webBanner(url, mode string, termWidth int, isTTY bool, pal theme.Palette) string {
	// Width behavior mirrors the please/research panels: unknown
	// widths assume 90, cap at 100, box leaves a 2-column margin,
	// floor at 50.
	w := termWidth
	if w <= 0 {
		w = 90
	}
	if w > 100 {
		w = 100
	}
	boxW := w - 2
	if boxW < 50 {
		boxW = 50
	}

	// The URL line must fit inside the box without wrapping (indent +
	// URL within the content width, which is boxW minus Padding(0,1)
	// on each side). A wrapped URL cannot be copied in one motion, and
	// copyability outranks the border.
	if !isTTY || len(webBannerURLIndent)+lipgloss.Width(url) > boxW-2 {
		return plainWebBanner(url, mode)
	}

	bold := lipgloss.NewStyle().Foreground(pal.Accent).Bold(true)
	muted := lipgloss.NewStyle().Foreground(pal.Muted)
	subtle := lipgloss.NewStyle().Foreground(pal.Subtle).Italic(true)
	link := lipgloss.NewStyle().Foreground(pal.Accent).Underline(true)

	line1 := "🧢 " + bold.Render(webBannerHeadline) + muted.Render(" · "+mode)

	urlText := link.Render(url)
	if !pal.NoColor {
		urlText = webOSC8(url, urlText)
	}
	line2 := webBannerURLIndent + urlText

	line3 := webBannerURLIndent +
		subtle.Render("the token lives only in this URL; relaunch reprints it · ctrl-c to stop")

	body := lipgloss.JoinVertical(lipgloss.Left, line1, line2, line3)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(pal.Accent).
		Padding(0, 1).
		Width(boxW).
		Render(body)
	return "\n" + box
}

// plainWebBanner is the non-TTY / narrow-terminal fallback - the exact
// three lines `carlos web` shipped before the panel, so anything that
// greps "open: <url>" out of piped stderr keeps working.
func plainWebBanner(url, mode string) string {
	return "carlos web - localhost agent console (" + mode + ")\n" +
		"  open: " + url + "\n" +
		"  the token lives only in this URL fragment; relaunch reprints it. ctrl-c to stop."
}
