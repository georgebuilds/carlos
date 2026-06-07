// Phase 8 slice 8d - push notifications for scheduled-run completion.
//
// The daemon fires schedules autonomously; without a notification path
// the user has no idea anything happened until they open the TUI and
// scroll the manage roster. Surface a quick desktop banner per fire so
// "carlos ran your morning summary; 3 tools called, $0.04 spent"
// reaches the user out-of-band.
//
// Pure-Go, no CGO. Each platform shells out to the standard utility:
//   - macOS:  osascript -e 'display notification "<body>" with title "carlos"'
//   - Linux:  notify-send "carlos" "<body>"
//   - other:  no-op (return nil so the daemon doesn't trip).
//
// Failures are non-fatal - the daemon's whole point is autonomy; a
// missing osascript / notify-send shouldn't crash the scheduler. We
// log the error to stderr (visible via `carlos daemon status` log
// fetch in a future slice) and continue.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"time"
)

// Notifier is the seam between the daemon's tick loop and the
// platform-specific notification dispatch. Production wires
// SystemNotifier; tests pass a recordingNotifier.
type Notifier interface {
	Notify(ctx context.Context, n Notification) error
}

// Notification is the payload - minimal because both backends only
// expose a title + body. urgency hint is macOS-ignored; Linux uses
// the `--urgency=` flag.
type Notification struct {
	// Title shows in bold above the body. "carlos" by default; the
	// daemon prepends per-schedule context if useful.
	Title string
	// Body is the main message. Keep under ~120 chars - both
	// platforms truncate aggressively past that.
	Body string
	// Urgency: "low" | "normal" | "critical". Empty = "normal".
	Urgency string
}

// SystemNotifier is the production Notifier. Dispatches to osascript
// on macOS, notify-send on Linux, no-op elsewhere.
type SystemNotifier struct {
	// Timeout caps each shell-out so a hung utility doesn't pin the
	// tick loop. Default 5s - banners should be near-instant.
	Timeout time.Duration
}

// Notify dispatches n to the platform-native notification surface.
// Returns nil on no-op platforms (Windows, etc.) so callers don't
// need to special-case.
func (s *SystemNotifier) Notify(ctx context.Context, n Notification) error {
	if n.Title == "" {
		n.Title = "carlos"
	}
	if n.Body == "" {
		return errors.New("notify: empty body")
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch runtime.GOOS {
	case "darwin":
		return runMacOSNotify(cctx, n)
	case "linux":
		return runLinuxNotify(cctx, n)
	default:
		// Windows, FreeBSD, etc. - no-op rather than error so the
		// daemon's tick loop stays platform-agnostic.
		return nil
	}
}

// runMacOSNotify invokes osascript with a `display notification`
// command. The double-escape on the body handles quotes within the
// message; AppleScript is sensitive to string delimiters.
func runMacOSNotify(ctx context.Context, n Notification) error {
	// AppleScript syntax: display notification "<body>" with title "<title>"
	// Escape quotes in inputs by replacing " with \" (AppleScript
	// honors backslash-escape in literal strings inside `display
	// notification`).
	body := escapeForAppleScript(n.Body)
	title := escapeForAppleScript(n.Title)
	script := fmt.Sprintf(`display notification "%s" with title "%s"`, body, title)
	cmd := exec.CommandContext(ctx, "osascript", "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("osascript: %w (%s)", err, string(out))
	}
	return nil
}

// runLinuxNotify shells out to notify-send. Available on most desktop
// distros that ship libnotify; servers / minimal containers may not
// have it - those return ENOENT which we wrap clearly.
func runLinuxNotify(ctx context.Context, n Notification) error {
	args := []string{n.Title, n.Body}
	if n.Urgency != "" {
		args = append([]string{"--urgency=" + n.Urgency}, args...)
	}
	cmd := exec.CommandContext(ctx, "notify-send", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("notify-send not installed (apt install libnotify-bin / pacman -S libnotify)")
		}
		return fmt.Errorf("notify-send: %w (%s)", err, string(out))
	}
	return nil
}

// escapeForAppleScript handles the two delimiters AppleScript cares
// about: backslash + double-quote. Other characters pass through.
func escapeForAppleScript(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '\\' || c == '"' {
			out = append(out, '\\')
		}
		out = append(out, c)
	}
	return string(out)
}
