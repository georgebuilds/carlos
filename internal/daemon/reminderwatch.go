package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
	"github.com/georgebuilds/carlos/internal/gateway"
	"github.com/georgebuilds/carlos/internal/todo"
)

// reminderScanInterval is how often the watcher scans the todo backends for due
// items. Reminders are date-granular, so a minute of latency is irrelevant;
// the interval is generous to keep vault walks infrequent.
const reminderScanInterval = 60 * time.Second

// maxReminderList caps how many item descriptions a single notification body
// enumerates before summarising the rest as "+N more".
const maxReminderList = 5

// reminderDeliverer delivers one batch of newly-due items. The production impl
// renders a gateway notification; tests record the calls.
type reminderDeliverer func(ctx context.Context, items []todo.Item)

// reminderWatcher tails presence from the shared event log and, while the user
// is /away, periodically scans every frame's todo backend for items whose due
// date has arrived (or passed). Each item fires at most once per watcher
// lifetime (tracked in notified), so a scan every minute does not re-ping the
// same task. It mirrors awayWatcher's presence gate: nothing is delivered while
// the user is present, since they can see the list in the UI.
type reminderWatcher struct {
	log      *agent.SQLiteEventLog
	router   *todo.Router
	deliver  reminderDeliverer
	now      func() time.Time
	interval time.Duration

	away        bool
	lastPresSeq int64
	notified    map[string]bool
}

func newReminderWatcher(log *agent.SQLiteEventLog, router *todo.Router, deliver reminderDeliverer, now func() time.Time, interval time.Duration) *reminderWatcher {
	if interval <= 0 {
		interval = reminderScanInterval
	}
	if now == nil {
		now = time.Now
	}
	return &reminderWatcher{
		log:      log,
		router:   router,
		deliver:  deliver,
		now:      now,
		interval: interval,
		notified: map[string]bool{},
	}
}

// drainPresence consumes new EvtPresence rows and updates the away flag to the
// most recent one (identical to awayWatcher.drainPresence).
func (w *reminderWatcher) drainPresence(ctx context.Context) {
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

// scan lists the master todo set and delivers a reminder for each newly-due,
// not-done item. No-op when present or when the router is unavailable.
func (w *reminderWatcher) scan(ctx context.Context) {
	if !w.away || w.router == nil {
		return
	}
	items, err := w.router.Master(ctx, todo.FilterOpen)
	if err != nil {
		return
	}
	// Use the local calendar date so a reminder fires on the day the user
	// sees on their wall clock, not the UTC date (which can differ near
	// midnight). DueOnOrBefore extracts the Y/M/D in the supplied time's
	// location.
	today := w.now()
	var due []todo.Item
	for _, it := range items {
		if it.Done || !it.DueOnOrBefore(today) {
			continue
		}
		// Dedup across scans, keyed by backend + frame + id so the same item
		// id surfacing in two different frames still fires for each.
		key := it.Backend + ":" + it.Frame + ":" + it.ID
		if it.ID == "" || w.notified[key] {
			continue
		}
		w.notified[key] = true
		due = append(due, it)
	}
	if len(due) > 0 {
		w.deliver(ctx, due)
	}
}

// run primes presence once (without an immediate scan), then scans on the
// configured interval until ctx is cancelled.
func (w *reminderWatcher) run(ctx context.Context) {
	w.drainPresence(ctx)
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.drainPresence(ctx)
			w.scan(ctx)
		}
	}
}

// notifyTodoDue is the production reminderDeliverer: it renders a single
// gateway notification summarising the newly-due items and fans it out over
// every routed channel. Best-effort - a missing/sick gateway degrades to
// silence rather than disturbing the daemon.
func (d *Daemon) notifyTodoDue(ctx context.Context, items []todo.Item) {
	d.mu.Lock()
	gw := d.gw
	d.mu.Unlock()
	if gw == nil || gw.broker == nil || len(items) == 0 {
		return
	}
	title, body := reminderNotification(items)

	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, _ = gw.broker.Send(sctx, gateway.OutboundEnvelope{
		Kind:    gateway.OutboundNotification,
		Title:   title,
		Body:    body,
		Urgency: gateway.UrgencyHigh,
	})
}

// reminderNotification renders the title + markdown body for a batch of due
// items. Pure (no gateway), so the formatting is unit-testable on its own.
func reminderNotification(items []todo.Item) (title, body string) {
	title = "carlos: 1 todo due"
	if len(items) > 1 {
		title = fmt.Sprintf("carlos: %d todos due", len(items))
	}
	var b strings.Builder
	for i, it := range items {
		if i >= maxReminderList {
			fmt.Fprintf(&b, "+%d more", len(items)-maxReminderList)
			break
		}
		line := "- " + it.Text
		if it.Frame != "" {
			line += " (" + it.Frame + ")"
		}
		if it.Due != "" {
			line += " 📅 " + it.Due
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return title, strings.TrimRight(b.String(), "\n")
}
