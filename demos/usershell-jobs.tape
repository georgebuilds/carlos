# usershell-jobs.tape - VHS recording of the carlos user-shell `!` prefix
# plus the jobs overlay.
#
# Regenerate with:
#   vhs demos/usershell-jobs.tape
#
# Prereq: charmbracelet/vhs installed (brew install vhs). The tape builds the
# carlos binary to /tmp/carlos-demo and runs it in a throwaway HOME via
# demos/usershell-demo-env.sh, so it never touches your real ~/.carlos.
#
# Record this on a real terminal (a local Mac/Linux session). VHS drives the
# binary inside its own headless terminal; carlos's user-shell spawns a nested
# PTY per job, which runs correctly under a real terminal but hangs in a
# doubly-nested headless CI sandbox.

Output "docs/branding/screenshots/usershell-jobs.gif"

Set Shell zsh
Set FontSize 16
Set Width 1000
Set Height 600
Set Padding 20
Set TypingSpeed 60ms
Set PlaybackSpeed 1.0

# Build the demo binary, then launch carlos in a clean throwaway HOME.
Hide
Type "go build -o /tmp/carlos-demo ./cmd/carlos"
Enter
Wait
Type "bash demos/usershell-demo-env.sh"
Enter
Sleep 3s
# A fresh HOME means carlos asks to trust this workspace on first launch.
# Press y so the shell driver is unblocked for the rest of the demo.
Type "y"
Sleep 1.5s
Show

# Foreground job: list the repo with `!ls -la`. Output streams into the
# transcript as a styled shell card.
Type "!ls -la"
Sleep 800ms
Enter
Sleep 3.5s

# Background job: start a long-running `!sleep 30` in the foreground, then
# Ctrl+Z to push it into the background (mirrors a real shell's ^Z).
Type "!sleep 30"
Sleep 800ms
Enter
Sleep 1.5s
Ctrl+Z
Sleep 1.5s

# Open the jobs overlay (Ctrl+J) to show the running background job.
Ctrl+J
Sleep 3.5s

# Close the overlay.
Escape
Sleep 1.5s

# Exit carlos.
Ctrl+C
Sleep 1.5s
