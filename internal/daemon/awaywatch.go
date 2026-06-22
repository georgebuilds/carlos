package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/gateway"
	"github.com/georgebuilds/carlos/internal/usershell"
)

// awayPollInterval is how often the watcher tails the shared event log for
// new presence changes + job completions. The chat TUI and the daemon are
// separate processes, so the in-process Subscribe fan-out can't carry these
// across - we poll. A few seconds is well under human notification latency,
// and each poll is an indexed point read by (agent_id, seq).
const awayPollInterval = 3 * time.Second

// awayNotifier delivers one background-job-completion notification. The real
// implementation wraps the gateway broker; tests record the calls.
type awayNotifier func(ctx context.Context, start usershell.StartPayload, end usershell.EndPayload)

// awayWatcher tails the shared event log and, while the user is marked away,
// fires a notification for each BACKGROUNDED shell job that completes. It's
// the first cross-process consumer of TUI-written events: presence
// (EvtPresence under PresenceAgentID) and the shell lifecycle
// (EvtUserShellStart / EvtUserShellEnd under usershell.EventAgentID).
//
// The notify func decouples the tail/gate logic from the broker so it's
// unit-testable without a live gateway.
type awayWatcher struct {
	log    *agent.SQLiteEventLog
	notify awayNotifier

	away         bool
	lastPresSeq  int64
	lastShellSeq int64
	// starts correlates each EvtUserShellEnd back to its start (for the
	// command text), keyed by job id - the same trick chatglue.shellEndBlock
	// uses. Populated as starts stream past in seq order.
	starts map[string]usershell.StartPayload
}

func newAwayWatcher(log *agent.SQLiteEventLog, notify awayNotifier) *awayWatcher {
	return &awayWatcher{log: log, notify: notify, starts: map[string]usershell.StartPayload{}}
}

// prime advances the watermarks past everything already in the log and loads
// the current presence + the start payloads for already-running jobs, WITHOUT
// notifying. Without it the watcher would fire for every historical
// completion the moment the daemon boots.
func (w *awayWatcher) prime(ctx context.Context) {
	w.drainPresence(ctx)
	if shell, err := w.log.Read(ctx, usershell.EventAgentID, w.lastShellSeq); err == nil {
		for _, ev := range shell {
			w.lastShellSeq = ev.Seq
			if ev.Type == agent.EvtUserShellStart {
				if p, e := usershell.DecodeStartPayload(ev.Payload); e == nil {
					w.starts[p.JobID] = p
				}
			}
		}
	}
}

// tick performs one poll cycle: refresh presence, then process new shell
// events - recording starts and notifying for backgrounded completions while
// away.
func (w *awayWatcher) tick(ctx context.Context) {
	w.drainPresence(ctx)
	shell, err := w.log.Read(ctx, usershell.EventAgentID, w.lastShellSeq)
	if err != nil {
		return
	}
	for _, ev := range shell {
		w.lastShellSeq = ev.Seq
		switch ev.Type {
		case agent.EvtUserShellStart:
			if p, e := usershell.DecodeStartPayload(ev.Payload); e == nil {
				w.starts[p.JobID] = p
			}
		case agent.EvtUserShellEnd:
			// Sample the latest presence: notify only if the user is away
			// at the time we observe the completion (within one poll of the
			// real finish). Coming back before the next poll means no ping,
			// which is the intended behavior.
			if !w.away {
				continue
			}
			end, e := usershell.DecodeEndPayload(ev.Payload)
			if e != nil || !end.Backgrounded {
				continue
			}
			w.notify(ctx, w.starts[end.JobID], end)
		}
	}
}

// drainPresence consumes new EvtPresence rows and updates the away flag to
// the most recent one.
func (w *awayWatcher) drainPresence(ctx context.Context) {
	pres, err := w.log.Read(ctx, agent.PresenceAgentID, w.lastPresSeq)
	if err != nil {
		return
	}
	for _, ev := range pres {
		w.lastPresSeq = ev.Seq
		if ev.Type != agent.EvtPresence {
			continue
		}
		var p agent.PresencePayload
		if json.Unmarshal(ev.Payload, &p) == nil {
			w.away = p.Away
		}
	}
}

// run primes once, then ticks on awayPollInterval until ctx is cancelled.
func (w *awayWatcher) run(ctx context.Context) {
	w.prime(ctx)
	t := time.NewTicker(awayPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.tick(ctx)
		}
	}
}

// notifyBackgroundComplete is the production awayNotifier: it renders a
// gateway notification for the finished job and fans it out over every
// routed channel. Best-effort - a missing/sick gateway degrades to silence
// rather than disturbing the daemon.
func (d *Daemon) notifyBackgroundComplete(ctx context.Context, start usershell.StartPayload, end usershell.EndPayload) {
	d.mu.Lock()
	gw := d.gw
	d.mu.Unlock()
	if gw == nil || gw.broker == nil {
		return
	}

	cmd := start.Command
	if cmd == "" {
		cmd = "(background job " + end.JobID + ")"
	}
	dur := end.Duration.Round(time.Millisecond)
	urgency := gateway.UrgencyDefault
	var body string
	switch {
	case end.Cancelled:
		body = fmt.Sprintf("$ %s\ncancelled after %s", cmd, dur)
	case end.ExitCode == 0:
		body = fmt.Sprintf("$ %s\nexit 0 (%s)", cmd, dur)
	default:
		urgency = gateway.UrgencyHigh
		body = fmt.Sprintf("$ %s\nexit %d (%s)", cmd, end.ExitCode, dur)
	}

	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, _ = gw.broker.Send(sctx, gateway.OutboundEnvelope{
		Kind:    gateway.OutboundNotification,
		Title:   "carlos: background job finished",
		Body:    body,
		Urgency: urgency,
	})
}
