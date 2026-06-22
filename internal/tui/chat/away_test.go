package chat

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/georgebuilds/carlos/internal/agent"
)

// latestPresence returns the Away value of the most recent EvtPresence row,
// or (false, false) if none exists.
func latestPresence(t *testing.T, log *agent.SQLiteEventLog) (away bool, found bool) {
	t.Helper()
	evs, err := log.Read(context.Background(), agent.PresenceAgentID, 0)
	if err != nil {
		t.Fatalf("read presence: %v", err)
	}
	for _, ev := range evs {
		if ev.Type != agent.EvtPresence {
			continue
		}
		var p agent.PresencePayload
		if json.Unmarshal(ev.Payload, &p) == nil {
			away, found = p.Away, true
		}
	}
	return away, found
}

func openAwayLog(t *testing.T) *agent.SQLiteEventLog {
	t.Helper()
	log, err := agent.OpenSQLiteEventLog(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

// TestAwaySlash_OnOffToggle covers the three input forms and the durable
// EvtPresence write the daemon watcher reads.
func TestAwaySlash_OnOffToggle(t *testing.T) {
	log := openAwayLog(t)
	m := &Model{log: log}

	// /away on
	msg := m.awaySlash("on")()
	if !m.away {
		t.Fatal("away should be on after /away on")
	}
	if s, ok := msg.(statusMsg); !ok || s.kind != statusInfo {
		t.Errorf("want statusInfo, got %#v", msg)
	}
	if away, found := latestPresence(t, log); !found || !away {
		t.Errorf("EvtPresence not written / not away: found=%v away=%v", found, away)
	}

	// /away off
	m.awaySlash("off")()
	if m.away {
		t.Fatal("away should be off after /away off")
	}
	if away, _ := latestPresence(t, log); away {
		t.Error("latest presence should be back (away=false)")
	}

	// /away with no arg toggles (currently off -> on)
	m.awaySlash("")()
	if !m.away {
		t.Fatal("no-arg /away should toggle on")
	}
}

// TestAwaySlash_BadArg leaves state untouched and warns.
func TestAwaySlash_BadArg(t *testing.T) {
	log := openAwayLog(t)
	m := &Model{log: log}
	m.away = true

	msg := m.awaySlash("sideways")()
	if !m.away {
		t.Error("a bad arg must not change away state")
	}
	if s, ok := msg.(statusMsg); !ok || s.kind != statusWarn {
		t.Errorf("want statusWarn for bad arg, got %#v", msg)
	}
}
