package agent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newSessionLog(t *testing.T) *SQLiteEventLog {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	log, err := OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return log
}

func mkSession(t *testing.T, log *SQLiteEventLog, id, title string, parentID string, agedBy time.Duration) {
	t.Helper()
	now := time.Now().UTC().Add(-agedBy).Truncate(time.Millisecond)
	r := AgentRow{
		ID:              id,
		ParentID:        parentID,
		RootID:          id,
		State:           StateRunning,
		Attempt:         1,
		Title:           title,
		Model:           "test-model",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastHeartbeatAt: now,
	}
	if err := log.InsertAgent(context.Background(), r); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
}

func appendUserMsg(t *testing.T, log *SQLiteEventLog, agentID, text string) {
	t.Helper()
	payload, _ := json.Marshal(MessagePayload{Text: text})
	if _, err := log.Append(context.Background(), Event{
		AgentID: agentID,
		TS:      time.Now().UTC().Truncate(time.Millisecond),
		Type:    EvtUserMessage,
		Payload: payload,
	}); err != nil {
		t.Fatalf("append user msg: %v", err)
	}
}

func TestListUserSessions_OrderByUpdatedAtDesc(t *testing.T) {
	log := newSessionLog(t)
	mkSession(t, log, "01HA", "older chat", "", 1*time.Hour)
	mkSession(t, log, "01HB", "newer chat", "", 1*time.Minute)
	got, err := ListUserSessions(context.Background(), log, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	if got[0].ID != "01HB" {
		t.Errorf("most recent first: want 01HB, got %s", got[0].ID)
	}
	if got[1].ID != "01HA" {
		t.Errorf("oldest last: want 01HA, got %s", got[1].ID)
	}
}

func TestListUserSessions_ExcludesSubAgents(t *testing.T) {
	log := newSessionLog(t)
	mkSession(t, log, "01HTOP", "top-level", "", 1*time.Minute)
	// Sub-agent: parent_id set.
	mkSession(t, log, "01HSUB", "subagent", "01HTOP", 30*time.Second)
	got, _ := ListUserSessions(context.Background(), log, "")
	if len(got) != 1 || got[0].ID != "01HTOP" {
		t.Errorf("expected only top-level; got %+v", got)
	}
}

func TestListUserSessions_ExcludedID(t *testing.T) {
	log := newSessionLog(t)
	mkSession(t, log, "01HA", "current", "", 1*time.Minute)
	mkSession(t, log, "01HB", "other", "", 5*time.Minute)
	got, _ := ListUserSessions(context.Background(), log, "01HA")
	if len(got) != 1 || got[0].ID != "01HB" {
		t.Errorf("excluded session leaked: %+v", got)
	}
}

func TestListUserSessions_PreviewAndCount(t *testing.T) {
	log := newSessionLog(t)
	mkSession(t, log, "01HCHAT", "title", "", 1*time.Minute)
	appendUserMsg(t, log, "01HCHAT", "first message")
	appendUserMsg(t, log, "01HCHAT", "second message")
	appendUserMsg(t, log, "01HCHAT", "third message — this should be the preview")
	got, _ := ListUserSessions(context.Background(), log, "")
	if len(got) != 1 {
		t.Fatalf("got %d sessions", len(got))
	}
	if got[0].UserMsgs != 3 {
		t.Errorf("UserMsgs: want 3, got %d", got[0].UserMsgs)
	}
	if !strings.HasPrefix(got[0].Preview, "third message") {
		t.Errorf("preview should be last msg: %q", got[0].Preview)
	}
}

func TestListUserSessions_NoMessages_EmptyPreview(t *testing.T) {
	log := newSessionLog(t)
	mkSession(t, log, "01H", "draft", "", time.Minute)
	got, _ := ListUserSessions(context.Background(), log, "")
	if got[0].Preview != "" || got[0].UserMsgs != 0 {
		t.Errorf("draft session preview/count not empty: %+v", got[0])
	}
}

func TestListUserSessions_PreviewCollapsesNewlines(t *testing.T) {
	log := newSessionLog(t)
	mkSession(t, log, "01H", "x", "", time.Minute)
	appendUserMsg(t, log, "01H", "line one\nline two\nline three")
	got, _ := ListUserSessions(context.Background(), log, "")
	if strings.Contains(got[0].Preview, "\n") {
		t.Errorf("preview should collapse newlines: %q", got[0].Preview)
	}
}

func TestListUserSessions_LongPreviewTruncated(t *testing.T) {
	log := newSessionLog(t)
	mkSession(t, log, "01H", "x", "", time.Minute)
	appendUserMsg(t, log, "01H", strings.Repeat("a", 500))
	got, _ := ListUserSessions(context.Background(), log, "")
	if len(got[0].Preview) > 125 { // 120 + ellipsis byte
		t.Errorf("preview length unbounded: %d", len(got[0].Preview))
	}
	if !strings.HasSuffix(got[0].Preview, "…") {
		t.Errorf("long preview missing ellipsis: %q", got[0].Preview)
	}
}

func TestListUserSessions_Empty(t *testing.T) {
	log := newSessionLog(t)
	got, err := ListUserSessions(context.Background(), log, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("empty log: got %d sessions", len(got))
	}
}

func TestMostRecentUserSession_Empty(t *testing.T) {
	log := newSessionLog(t)
	_, err := MostRecentUserSession(context.Background(), log)
	if !errors.Is(err, ErrNoSessions) {
		t.Errorf("expected ErrNoSessions; got %v", err)
	}
}

func TestMostRecentUserSession_PicksNewest(t *testing.T) {
	log := newSessionLog(t)
	mkSession(t, log, "01HOLD", "old", "", time.Hour)
	mkSession(t, log, "01HNEW", "new", "", time.Minute)
	got, err := MostRecentUserSession(context.Background(), log)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "01HNEW" {
		t.Errorf("most recent: want 01HNEW, got %s", got.ID)
	}
}

func TestTruncatePreview(t *testing.T) {
	cases := []struct {
		in   string
		max  int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hell…"},
		{"hi\nthere", 10, "hi there"},
		{"a", 0, ""},
		{"abc", 1, "…"},
		{"", 5, ""},
	}
	for _, tc := range cases {
		if got := truncatePreview(tc.in, tc.max); got != tc.want {
			t.Errorf("truncatePreview(%q, %d) = %q, want %q", tc.in, tc.max, got, tc.want)
		}
	}
}
