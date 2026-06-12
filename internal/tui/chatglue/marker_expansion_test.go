package chatglue

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/georgebuilds/carlos/internal/agent"
)

// appendUserMessageWithAtts is the chip-aware sibling of the
// appendUserMessage helper: persists a user_message whose Text embeds
// composer markers and whose Attachments carry the chip payloads.
func appendUserMessageWithAtts(t *testing.T, log *agent.SQLiteEventLog, id, text string, atts []agent.Attachment) {
	t.Helper()
	payload, err := json.Marshal(agent.MessagePayload{Text: text, Attachments: atts})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: agent.EvtUserMessage, Payload: payload,
	}); err != nil {
		t.Fatalf("append user_message: %v", err)
	}
}

// TestBuildHistory_ExpandsPasteMarkers: a persisted chip message
// reaches the model with the paste expanded into a labeled fenced
// block - full content, nickname label, surrounding prose intact.
func TestBuildHistory_ExpandsPasteMarkers(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-chip-1"
	seedAgent(t, log, id)
	appendUserMessageWithAtts(t, log, id, "explain ‹p:1› for me", []agent.Attachment{
		{ID: "1", Kind: agent.AttachmentPaste, Nickname: "paste#1", Content: "func main() {\n\tpanic(\"boom\")\n}"},
	})

	l := NewLoop(Config{}, log, newMemSource(), id)
	history, err := l.buildHistory(context.Background())
	if err != nil {
		t.Fatalf("buildHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1", len(history))
	}
	text := history[0].Content[0].Text
	for _, want := range []string{
		"explain ",
		" for me",
		"[pasted: paste#1]",
		"panic(\"boom\")",
		"```",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("expanded history missing %q:\n%s", want, text)
		}
	}
}

// TestBuildHistory_NoRawMarkerEverReachesTheModel is the slice I-1
// invariant test: across paste, image, mention AND dangling markers,
// not one message handed to the model may contain a raw ‹x:id›
// marker. This is the contract later slices build on.
func TestBuildHistory_NoRawMarkerEverReachesTheModel(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-chip-2"
	seedAgent(t, log, id)
	appendUserMessageWithAtts(t, log, id,
		"all kinds: ‹p:1› ‹i:2› ‹m:3› and a dangling ‹p:9z›",
		[]agent.Attachment{
			{ID: "1", Kind: agent.AttachmentPaste, Nickname: "logs", Content: "ERROR: oh no"},
			{ID: "2", Kind: agent.AttachmentImage, Nickname: "screen.png", Path: "/tmp/screen.png"},
			{ID: "3", Kind: agent.AttachmentMention, Nickname: "loop.go", Path: "internal/agent/loop.go"},
		})
	appendUserMessage(t, log, id, "plain follow-up")

	l := NewLoop(Config{}, log, newMemSource(), id)
	history, err := l.buildHistory(context.Background())
	if err != nil {
		t.Fatalf("buildHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2", len(history))
	}
	for i, msg := range history {
		for _, blk := range msg.Content {
			if agent.ContainsMarker(blk.Text) {
				t.Errorf("RAW MARKER LEAKED to the model in message %d: %q", i, blk.Text)
			}
		}
	}
	first := history[0].Content[0].Text
	if !strings.Contains(first, "[image: screen.png]") {
		t.Errorf("image placeholder missing: %q", first)
	}
	if !strings.Contains(first, "@internal/agent/loop.go (mentioned file") {
		t.Errorf("mention reference missing: %q", first)
	}
	if !strings.Contains(first, "[attachment 9z unavailable]") {
		t.Errorf("dangling marker placeholder missing: %q", first)
	}
}

// TestBuildHistory_AssistantTextNotExpanded: expansion applies to USER
// messages only - assistant text that happens to contain marker-shaped
// bytes must round-trip verbatim (the model wrote it; rewriting it
// would corrupt the conversational record).
func TestBuildHistory_AssistantTextNotExpanded(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-chip-3"
	seedAgent(t, log, id)
	appendUserMessage(t, log, id, "hi")
	asst := "markers look like ‹p:1› in the composer"
	payload, err := json.Marshal(agent.MessagePayload{Text: asst})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: agent.EvtAssistantMessage, Payload: payload,
	}); err != nil {
		t.Fatalf("append assistant: %v", err)
	}

	l := NewLoop(Config{}, log, newMemSource(), id)
	history, err := l.buildHistory(context.Background())
	if err != nil {
		t.Fatalf("buildHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2", len(history))
	}
	if got := history[1].Content[0].Text; got != asst {
		t.Errorf("assistant text rewritten: %q, want %q", got, asst)
	}
}

// TestBuildHistory_ExpansionRespectsSessionReset: a /clear between a
// chip message and the next turn drops the pre-reset chip message
// entirely (no half-expanded leftovers surviving the reset).
func TestBuildHistory_ExpansionRespectsSessionReset(t *testing.T) {
	log := openTestLog(t)
	const id = "agent-chip-4"
	seedAgent(t, log, id)
	appendUserMessageWithAtts(t, log, id, "pre-reset ‹p:1›", []agent.Attachment{
		{ID: "1", Kind: agent.AttachmentPaste, Nickname: "old", Content: "stale"},
	})
	if _, err := log.Append(context.Background(), agent.Event{
		AgentID: id, TS: time.Now().UTC(), Type: agent.EvtSessionReset, Payload: []byte(`{}`),
	}); err != nil {
		t.Fatalf("append reset: %v", err)
	}
	appendUserMessage(t, log, id, "fresh start")

	l := NewLoop(Config{}, log, newMemSource(), id)
	history, err := l.buildHistory(context.Background())
	if err != nil {
		t.Fatalf("buildHistory: %v", err)
	}
	if len(history) != 1 || history[0].Content[0].Text != "fresh start" {
		t.Errorf("post-reset history wrong: %+v", history)
	}
}
