package chat

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/theme"
)

// pumpTextTicks feeds n textTickMsg through Update - the typewriter
// reveal cursor (slice 9b) only advances on ticks, so tests that
// assert on live-buffer content pump enough of them to expose it.
func pumpTextTicks(m *Model, n int) *Model {
	for i := 0; i < n; i++ {
		updated, _ := m.Update(textTickMsg{})
		m = updated.(*Model)
	}
	return m
}

// withReducedMotion flips the package-level reduced-motion gate for
// one test and restores it on cleanup.
func withReducedMotion(t *testing.T, on bool) {
	t.Helper()
	prev := reducedMotion
	reducedMotion = on
	t.Cleanup(func() { reducedMotion = prev })
}

// ----- pure pacing functions ----------------------------------------------

// TestAdvanceReveal_Table pins the pacing policy edge cases.
func TestAdvanceReveal_Table(t *testing.T) {
	cases := []struct {
		name            string
		revealed, total int
		want            int
	}{
		{"idle", 0, 0, 0},
		{"seal resets cursor", 57, 0, 0},
		{"negative total clamps", 3, -1, 0},
		{"fresh stream takes min step plus catchup", 0, 21, 0 + typewriterMinStep + 21/typewriterCatchupDiv},
		{"small backlog snaps to total", 19, 21, 21},
		{"exact backlog equal to step snaps", 20, 22, 22},
		{"caught up stays put", 30, 30, 30},
		{"buffer reset restarts from zero", 10, 8, typewriterMinStep + 8/typewriterCatchupDiv},
	}
	for _, tc := range cases {
		if got := advanceReveal(tc.revealed, tc.total); got != tc.want {
			t.Errorf("%s: advanceReveal(%d, %d) = %d, want %d",
				tc.name, tc.revealed, tc.total, got, tc.want)
		}
	}
}

// TestAdvanceReveal_CatchupScalesWithBacklog: a big burst advances
// much faster than a trickle, so the reveal never falls far behind.
func TestAdvanceReveal_CatchupScalesWithBacklog(t *testing.T) {
	smallStep := advanceReveal(0, 40)
	bigStep := advanceReveal(0, 4000)
	if bigStep <= smallStep {
		t.Fatalf("catch-up did not scale: backlog 40 stepped %d, backlog 4000 stepped %d", smallStep, bigStep)
	}
	// A 4KB burst must be fully revealed in well under a second of
	// ticks (30/sec): 25 ticks ≈ 825ms.
	revealed := 0
	for tick := 1; tick <= 25; tick++ {
		revealed = advanceReveal(revealed, 4000)
		if revealed == 4000 {
			return
		}
	}
	t.Fatalf("4000-rune backlog not caught up after 25 ticks; revealed=%d", revealed)
}

// TestAdvanceReveal_MonotonicAndTerminates: the cursor never moves
// backward mid-stream and always reaches total.
func TestAdvanceReveal_MonotonicAndTerminates(t *testing.T) {
	const total = 333
	revealed := 0
	for tick := 0; tick < 1000; tick++ {
		next := advanceReveal(revealed, total)
		if next < revealed {
			t.Fatalf("cursor moved backward: %d -> %d", revealed, next)
		}
		if next > total {
			t.Fatalf("cursor overshot total: %d > %d", next, total)
		}
		revealed = next
		if revealed == total {
			return
		}
	}
	t.Fatalf("reveal never reached total; stuck at %d/%d", revealed, total)
}

// TestRevealPrefix covers rune-boundary safety for multibyte text.
func TestRevealPrefix(t *testing.T) {
	cases := []struct {
		s    string
		n    int
		want string
	}{
		{"hello", 0, ""},
		{"hello", -3, ""},
		{"hello", 3, "hel"},
		{"hello", 5, "hello"},
		{"hello", 99, "hello"},
		{"héllo", 2, "hé"}, // 2-byte rune inside the cut
		{"🧢🧢🧢", 1, "🧢"},    // 4-byte runes
		{"🧢🧢🧢", 2, "🧢🧢"},
		{"🧢🧢", 2, "🧢🧢"}, // n == rune count < byte count
		{"a🧢b", 2, "a🧢"},
		{"", 4, ""},
	}
	for _, tc := range cases {
		if got := revealPrefix(tc.s, tc.n); got != tc.want {
			t.Errorf("revealPrefix(%q, %d) = %q, want %q", tc.s, tc.n, got, tc.want)
		}
	}
}

// ----- model integration ---------------------------------------------------

// TestTypewriter_RevealsIncrementally: one tick exposes a prefix of
// the live buffer, not the whole burst; further ticks catch up to the
// full text.
func TestTypewriter_RevealsIncrementally(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000TW001"
	seedAgent(t, log, agentID, "typewriter", "fake")

	src := NewMemTextSource()
	m := New(log, agentID, src)
	m = drive(t, m, 120, 30)

	body := strings.Repeat("alpha ", 30) + "OMEGA"
	src.Append(agentID, body)

	m = pumpTextTicks(m, 1)
	view := m.View()
	if !strings.Contains(view, "alpha") {
		t.Fatalf("first tick should reveal a prefix:\n%s", view)
	}
	if strings.Contains(view, "OMEGA") {
		t.Fatalf("first tick must not reveal the tail of a %d-rune burst:\n%s", len(body), view)
	}

	m = pumpTextTicks(m, 200)
	if view := m.View(); !strings.Contains(view, "OMEGA") {
		t.Fatalf("reveal never caught up to the stream head:\n%s", view)
	}
}

// TestTypewriter_SnapOnSeal: when the turn seals into an
// EvtAssistantMessage the transcript entry renders complete
// immediately - the reveal cursor only ever gated the live buffer -
// and the cursor resets for the next turn.
func TestTypewriter_SnapOnSeal(t *testing.T) {
	log := openTempLog(t)
	const agentID = "01HV00000000000000000TW002"
	seedAgent(t, log, agentID, "typewriter", "fake")

	src := NewMemTextSource()
	m := New(log, agentID, src)
	m = drive(t, m, 120, 30)

	body := strings.Repeat("beta ", 40) + "FINALE"
	src.Append(agentID, body)
	m = pumpTextTicks(m, 1) // partial reveal only

	// Seal: chatglue resets the live buffer and the sealed message
	// arrives as an event.
	src.Reset(agentID)
	payload, err := json.Marshal(agent.MessagePayload{Text: body})
	if err != nil {
		t.Fatal(err)
	}
	updated, _ := m.Update(eventMsg{ev: agent.Event{
		AgentID: agentID,
		Type:    agent.EvtAssistantMessage,
		TS:      time.Now().UTC(),
		Payload: payload,
	}})
	m = updated.(*Model)

	if view := m.View(); !strings.Contains(view, "FINALE") {
		t.Fatalf("sealed turn must render in full instantly:\n%s", view)
	}
	m = pumpTextTicks(m, 1)
	if m.typeRevealed != 0 {
		t.Fatalf("cursor must reset after seal; got %d", m.typeRevealed)
	}
}

// TestTypewriter_ReducedMotionShowsTextAsItArrives: with the gate on,
// the full live buffer is visible with zero ticks (pre-9b behavior).
func TestTypewriter_ReducedMotionShowsTextAsItArrives(t *testing.T) {
	withReducedMotion(t, true)

	log := openTempLog(t)
	const agentID = "01HV00000000000000000TW003"
	seedAgent(t, log, agentID, "typewriter", "fake")

	src := NewMemTextSource()
	m := New(log, agentID, src)
	m = drive(t, m, 120, 30)

	src.Append(agentID, "instant full reveal EXPECTED")
	m.rerenderViewport()
	if view := m.View(); !strings.Contains(view, "instant full reveal EXPECTED") {
		t.Fatalf("reduced motion must show streamed text immediately:\n%s", view)
	}

	// The cursor machinery stays parked - nothing to advance.
	m = pumpTextTicks(m, 3)
	if m.typeRevealed != 0 {
		t.Fatalf("advanceTypewriter must no-op under reduced motion; cursor=%d", m.typeRevealed)
	}
}

// TestLiveReveal_GatesOnlyWithoutReducedMotion pins the gate at the
// method level: partial under normal motion, full under reduced.
func TestLiveReveal_GatesOnlyWithoutReducedMotion(t *testing.T) {
	m := &Model{typeRevealed: 3}

	withReducedMotion(t, false)
	if got := m.liveReveal("abcdef"); got != "abc" {
		t.Fatalf("liveReveal = %q, want %q", got, "abc")
	}

	reducedMotion = true
	if got := m.liveReveal("abcdef"); got != "abcdef" {
		t.Fatalf("reduced-motion liveReveal = %q, want full text", got)
	}
}

// TestThinkingDots_ReducedMotionStatic: the bouncing-dot row freezes
// under reduced motion and keeps animating without it.
func TestThinkingDots_ReducedMotionStatic(t *testing.T) {
	withReducedMotion(t, true)
	a, b, c := renderThinkingDots(0), renderThinkingDots(7), renderThinkingDots(13)
	if a != b || b != c {
		t.Fatalf("reduced-motion dots must be static:\n%q\n%q\n%q", a, b, c)
	}

	reducedMotion = false
	if renderThinkingDots(0) == renderThinkingDots(thinkingFrameTicks) {
		t.Fatal("dots must animate across frames when motion is allowed")
	}
}

// TestTypewriter_NoColorDoesNotDisableReveal: NO_COLOR is a color
// knob, not a motion knob - the typewriter still paces under a
// monochrome palette, and the thinking dots still animate.
func TestTypewriter_NoColorDoesNotDisableReveal(t *testing.T) {
	withReducedMotion(t, false)
	t.Cleanup(func() { ApplyPalette(theme.Load(theme.Options{})) })
	ApplyPalette(theme.Load(theme.Options{
		Env: func(k string) string {
			if k == "NO_COLOR" {
				return "1"
			}
			return ""
		},
	}))

	m := &Model{typeRevealed: 2}
	if got := m.liveReveal("monochrome"); got != "mo" {
		t.Fatalf("NO_COLOR must not bypass the reveal gate; got %q", got)
	}
	if renderThinkingDots(0) == renderThinkingDots(thinkingFrameTicks) {
		t.Fatal("NO_COLOR must not freeze the dot animation")
	}
}
