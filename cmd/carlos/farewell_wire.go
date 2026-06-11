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

// runBrewCheck is the production check-runner: it wires the real tap +
// brew probes into runBrewCheckWith. Pulled apart so the branch logic
// is unit-testable without hitting the network or shelling out.
func runBrewCheck(panel *farewell.Panel, currentVersion string) {
	runBrewCheckWith(panel, currentVersion,
		func(ctx context.Context, v string) (string, bool) {
			return farewell.CheckTapUpdate(ctx, v, "")
		},
		func(ctx context.Context) bool {
			return farewell.CheckBrewUpdate(ctx, "carlos")
		},
	)
}

// runBrewCheckWith is the testable seam behind runBrewCheck. Under a
// bounded ctx it:
//
//  1. Probes the homebrew tap (checkTap) and compares the live formula
//     version against the running binary's version. This is
//     authoritative — independent of brew's local cache — so it
//     surfaces updates even when the user hasn't run `brew update`
//     recently or has HOMEBREW_NO_AUTO_UPDATE set.
//  2. If the tap probe reports nothing, falls back to checkBrew (`brew
//     outdated`, which reads brew's local cache). Better to surface a
//     stale notice than no notice.
//
// The message advises `brew update && brew upgrade carlos` — the
// `brew update` half is important because most users hit this notice in
// the "I never run `brew update` manually" mode where `brew upgrade`
// alone would resolve against the same stale local cache that produced
// the notice.
func runBrewCheckWith(panel *farewell.Panel, currentVersion string,
	checkTap func(context.Context, string) (string, bool),
	checkBrew func(context.Context) bool) {
	ctx, cancel := context.WithTimeout(context.Background(), brewProbeTimeout)
	defer cancel()
	if newer, ok := checkTap(ctx, currentVersion); ok {
		panel.AddWithDetail("⬆️", "carlos "+newer+" is available",
			"run `brew update && brew upgrade carlos`")
		return
	}
	if checkBrew(ctx) {
		panel.AddWithDetail("⬆️", "update available",
			"run `brew update && brew upgrade carlos`")
	}
}

// checkBrewAtExit is the deferred exit-time hook: when the binary
// lives in a Homebrew Cellar, it runs the synchronous (timeout-
// bounded) update probe and queues the ⬆️ message into the panel
// before printFarewell renders. The user is already on their way
// out by the time this runs, so a small wall-clock wait is fine —
// the trade is "show the update notice promptly" vs. "delay
// startup with a check the user didn't ask for at session boot".
// No-op when the running binary isn't a Homebrew install or when
// the panel is nil (tests). Pulled out behind checkBrewAtExitWith
// so unit tests can inject the detector + check function without
// shelling out to a real brew.
func checkBrewAtExit(panel *farewell.Panel) {
	checkBrewAtExitWith(panel, farewell.IsBrewInstall, brewCheckWithVersion(versionString()))
}

// brewCheckWithVersion binds the running binary's version into a
// panel-check closure. Extracted so the closure body (the call into
// runBrewCheck) is reachable from a unit test without faking a Homebrew
// install — checkBrewAtExit itself can only invoke it on a real Cellar
// binary, which `go test` never is.
func brewCheckWithVersion(version string) func(*farewell.Panel) {
	return func(p *farewell.Panel) {
		runBrewCheck(p, version)
	}
}

// checkBrewAtExitWith is the testable seam behind checkBrewAtExit.
// Pure function; no globals consulted.
func checkBrewAtExitWith(panel *farewell.Panel, isBrew func() bool, check func(*farewell.Panel)) {
	if panel == nil {
		return
	}
	if !isBrew() {
		return
	}
	check(panel)
}
