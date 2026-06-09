// farewell_wire.go - wires the internal/farewell.Panel into the
// session lifecycle: collects startup notes, kicks off the brew
// probe, and renders the bordered box on exit.
//
// Most of the logic lives in internal/farewell so it stays unit-
// testable; this file is the tiny glue layer between that package
// and the runtime entry points (runDefault, runHeadless,
// please-once paths). Keeping it small means the per-call hooks
// remain easy to reason about and the test surface stays small.

package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"golang.org/x/term"

	"github.com/georgebuilds/carlos/internal/farewell"
	"github.com/georgebuilds/carlos/internal/theme"
)

// farewellTerminalWidth picks the box width for the rendered panel,
// preferring the real terminal width (stderr is what we'll write to)
// and falling back to a comfortable default when stderr isn't a TTY
// (e.g. piped to a file). Capped so a 200-column terminal doesn't
// produce a goofy stretched box.
func farewellTerminalWidth() int {
	w, ok := stderrTerminalWidth()
	if !ok {
		return farewellWidthFallback
	}
	return clampFarewellWidth(w)
}

// stderrTerminalWidth probes stderr for a real TTY width. Pulled out
// so the clamp + fallback logic is testable without faking a TTY.
func stderrTerminalWidth() (int, bool) {
	fd := int(os.Stderr.Fd())
	if !term.IsTerminal(fd) {
		return 0, false
	}
	w, _, err := term.GetSize(fd)
	if err != nil || w <= 0 {
		return 0, false
	}
	return w, true
}

// clampFarewellWidth caps the rendered box width so a 200-column
// terminal doesn't produce a goofy stretched panel.
func clampFarewellWidth(w int) int {
	if w > farewellWidthMax {
		return farewellWidthMax
	}
	return w
}

const (
	farewellWidthFallback = 78
	farewellWidthMax      = 100
)

// printFarewell is called via defer in the runtime entry points. It
// appends the goodbye line (always last) and writes the rendered box
// to stderr. No-op when the panel is empty — a session with no
// warnings + no name still gets a "later" line, so emptiness can only
// happen in tests that disable the panel deliberately.
func printFarewell(panel *farewell.Panel, userName string) {
	if panel == nil {
		return
	}
	name := userName
	if name == "" {
		name = "Boss"
	}
	panel.Add("👋", "later, "+name)
	out := panel.Render(farewellTerminalWidth(), theme.Load(theme.Options{}))
	if out == "" {
		return
	}
	fmt.Fprintln(os.Stderr, out)
}

// brewProbeTimeout caps the wall-clock budget for the brew update
// probe. brew can be slow on first-run-of-the-day (it auto-updates
// formulae); we'd rather skip the update note than make shutdown
// drag.
const brewProbeTimeout = 2 * time.Second

// startBrewProbe kicks off a background goroutine that consults
// `brew outdated --quiet` and queues the ⬆️ message when carlos is
// in the output. No-op when the running binary isn't a Homebrew
// install. Returns a channel the caller can wait on at shutdown so
// the result lands in the panel before render.
func startBrewProbe(panel *farewell.Panel) <-chan struct{} {
	return startBrewProbeWith(panel, farewell.IsBrewInstall, runBrewCheck)
}

// startBrewProbeWith is the seam: it takes the install-detector and
// the check-runner as parameters so unit tests can inject behaviour
// without spawning a real brew. The production wiring (startBrewProbe)
// hooks the two real helpers.
func startBrewProbeWith(panel *farewell.Panel, isBrew func() bool, check func(*farewell.Panel)) <-chan struct{} {
	done := make(chan struct{})
	if !isBrew() {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		check(panel)
	}()
	return done
}

// runBrewCheck is the production check-runner: bounded ctx + brew
// outdated lookup + queue the ⬆️ message when carlos is named.
func runBrewCheck(panel *farewell.Panel) {
	ctx, cancel := context.WithTimeout(context.Background(), brewProbeTimeout)
	defer cancel()
	if farewell.CheckBrewUpdate(ctx, "carlos") {
		panel.AddWithDetail("⬆️", "update available",
			"run `brew upgrade carlos` (restart the daemon to pick it up)")
	}
}

// waitBrewProbe blocks (briefly) until the probe finishes or its
// shutdown ceiling elapses. Called via defer right before
// printFarewell — small wait so the brew result, when present,
// makes it into the rendered box.
func waitBrewProbe(done <-chan struct{}) {
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(brewProbeTimeout + 100*time.Millisecond):
		// Hit the ceiling; the rest of the shutdown wins.
	}
}
