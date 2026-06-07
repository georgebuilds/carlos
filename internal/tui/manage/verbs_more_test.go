package manage

import (
	"errors"
	"strings"
	"testing"
)

// TestOverlayPromptLabel_AllVariants exercises every branch of the
// overlay prompt switch. Empty intent on the confirm prompts is the
// "selection cleared" path - the overlay should still surface a
// reasonable prompt rather than panic.
func TestOverlayPromptLabel_AllVariants(t *testing.T) {
	cases := []struct {
		name   string
		kind   overlayKind
		intent string
		want   string
	}{
		{"none", overlayNone, "anything", ""},
		{"steer", overlaySteer, "x", "steer: "},
		{"interrupt", overlayInterruptConfirm, "compile spec", `interrupt "compile spec"?`},
		{"interrupt-empty", overlayInterruptConfirm, "", `interrupt ""?`},
		{"stop", overlayStopConfirm, "deploy", `stop "deploy"?`},
		{"stop-long", overlayStopConfirm, strings.Repeat("x", 200), `stop "` + strings.Repeat("x", 200) + `"?`},
		{"filter", overlayFilter, "", "filter: "},
		{"reject", overlayRejectReason, "", "reject reason: "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := overlayPromptLabel(c.kind, c.intent)
			if !strings.Contains(got, c.want) {
				t.Errorf("overlayPromptLabel(%v, %q) = %q, want substring %q",
					c.kind, c.intent, got, c.want)
			}
		})
	}
}

// TestNoopDispatcher_SurfacesNotWired confirms the default dispatcher
// (installed by New() when sup==nil) returns the "no supervisor wired"
// error on each verb call so the TUI surfaces a clear failure rather
// than a silent no-op.
func TestNoopDispatcher_SurfacesNotWired(t *testing.T) {
	d := noopDispatcher{}
	if err := d.Steer("id", "msg"); err == nil || !strings.Contains(err.Error(), "no supervisor") {
		t.Errorf("noopDispatcher.Steer = %v, want 'no supervisor wired'", err)
	}
	if err := d.Interrupt("id"); err == nil || !strings.Contains(err.Error(), "no supervisor") {
		t.Errorf("noopDispatcher.Interrupt = %v, want 'no supervisor wired'", err)
	}
	if err := d.Stop("id"); err == nil || !strings.Contains(err.Error(), "no supervisor") {
		t.Errorf("noopDispatcher.Stop = %v, want 'no supervisor wired'", err)
	}
}

// TestDispatch_VerbsProduceVerbResult exercises each dispatch helper -
// the tea.Cmd they return must produce a VerbResult tagged with the
// matching verb name + agent ID. Errors propagate through.
func TestDispatch_VerbsProduceVerbResult(t *testing.T) {
	const id = "01HVabc12345678"

	t.Run("steer-ok", func(t *testing.T) {
		rec := &recordingDispatcher{}
		msg := dispatchSteer(rec, id, "nudge")()
		res, ok := msg.(VerbResult)
		if !ok {
			t.Fatalf("dispatchSteer msg = %T", msg)
		}
		if res.Verb != "steer" || res.AgentID != id || res.Err != nil {
			t.Errorf("VerbResult = %+v", res)
		}
		if len(rec.steers) != 1 || rec.steers[0].Msg != "nudge" {
			t.Errorf("recordingDispatcher.steers = %+v", rec.steers)
		}
	})

	t.Run("interrupt-err", func(t *testing.T) {
		rec := &recordingDispatcher{err: errors.New("boom")}
		msg := dispatchInterrupt(rec, id)()
		res := msg.(VerbResult)
		if res.Verb != "interrupt" || res.Err == nil {
			t.Errorf("VerbResult = %+v", res)
		}
		line := res.String()
		if !strings.Contains(line, "boom") {
			t.Errorf("String() = %q, want 'boom'", line)
		}
	})

	t.Run("stop-ok", func(t *testing.T) {
		rec := &recordingDispatcher{}
		msg := dispatchStop(rec, id)()
		res := msg.(VerbResult)
		if res.Verb != "stop" || res.Err != nil {
			t.Errorf("VerbResult = %+v", res)
		}
		if line := res.String(); !strings.Contains(line, "stopping") || !strings.Contains(line, "graceful drain") {
			t.Errorf("stop ok String() = %q", line)
		}
	})
}

// TestVerbResult_UnknownVerb covers the fallback branch of String() for
// a verb the switch doesn't recognise (shouldn't happen in production
// but the code path exists).
func TestVerbResult_UnknownVerb(t *testing.T) {
	r := VerbResult{Verb: "rumble", AgentID: "01HVxyz12345"}
	if got := r.String(); !strings.Contains(got, "rumble") || !strings.Contains(got, "ok") {
		t.Errorf("String() = %q, want 'rumble ... ok'", got)
	}
}
