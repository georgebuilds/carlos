package web

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/georgebuilds/carlos/internal/tui/chatglue"
)

// cc_map.go: the Claude Code observe adapter's pure core. It maps a CC
// session's on-disk JSONL (~/.claude/projects/<enc-cwd>/<uuid>.jsonl) into
// the normalized wire vocabulary (spec §8), so a CC thread renders with the
// exact same transcript grammar as a carlos one. This is the §12
// conformance surface: golden-file tested (fixture JSONL in, []WireEvent
// out). The on-disk format differs from the stream-json output the live
// driver (B-3) will consume; this half is the file projection.

// ccRecord is the unknown-field-tolerant envelope of one JSONL line. Only
// the fields the projection needs are pulled; everything else is ignored
// (forward-compatible, same discipline as the rest of the wire layer).
type ccRecord struct {
	Type        string    `json:"type"`
	Timestamp   string    `json:"timestamp"`
	IsSidechain bool      `json:"isSidechain"`
	Cwd         string    `json:"cwd"`
	Message     ccMessage `json:"message"`
}

type ccMessage struct {
	Role  string `json:"role"`
	Model string `json:"model"`
	// Content is polymorphic: a bare string (a plain user turn) or an array
	// of content blocks (assistant text/tool_use, or user tool_result).
	Content json.RawMessage `json:"content"`
}

type ccBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ID        string          `json:"id"`
	ToolUseID string          `json:"tool_use_id"`
	IsError   *bool           `json:"is_error"`
	Content   json.RawMessage `json:"content"` // tool_result payload: string or array
}

// ccRecordsToWire parses a CC session's JSONL bytes and returns the ordered
// wire events for the thread `tid` (a "cc:<uuid>" wire id). Seqs are the
// emission ordinal (1-based) over the whole file: deterministic and stable
// for an append-only session, so ReadEvents (backfill) and Subscribe (tail)
// agree on the SSE reconnect cursor. Sub-agent sidechain records are skipped
// (they belong to a crew column, not the main transcript). One record may
// yield several wire events (an assistant turn with text + N tool calls).
func ccRecordsToWire(tid string, data []byte) []WireEvent {
	var out []WireEvent
	toolName := map[string]string{} // tool_use id -> name, for tool_result correlation
	var seq int64

	emit := func(kind string, ts string, d any) {
		seq++
		out = append(out, WireEvent{Seq: seq, Thread: tid, TS: ts, Kind: kind, Data: d})
	}

	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var rec ccRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec.IsSidechain {
			continue
		}
		ts := ccTimestamp(rec.Timestamp)

		switch rec.Type {
		case "user":
			if s, ok := ccContentString(rec.Message.Content); ok {
				// Claude Code persists slash commands as user messages wrapped
				// in <local-command-*> / <command-*> tags. Rendered verbatim
				// they are noisy bubbles; project them compactly: drop the
				// boilerplate caveat, surface the invocation + its output as a
				// `command` event the SPA renders as a one-line pill.
				if data, kind := ccCommandWrapper(s); kind == ccWrapDrop {
					continue
				} else if kind == ccWrapCommand {
					emit("command", ts, data)
					continue
				}
				if strings.TrimSpace(s) == "" {
					continue
				}
				emit("user_message", ts, map[string]any{"text": s})
				continue
			}
			for _, b := range ccContentBlocks(rec.Message.Content) {
				switch b.Type {
				case "tool_result":
					prev, truncated := ccPreview(b.Content)
					isErr := b.IsError != nil && *b.IsError
					emit("tool_result", ts, map[string]any{
						"name":           toolName[b.ToolUseID],
						"output_preview": prev,
						"is_error":       isErr,
						"truncated":      truncated,
					})
				case "text":
					if strings.TrimSpace(b.Text) != "" {
						emit("user_message", ts, map[string]any{"text": b.Text})
					}
				}
			}

		case "assistant":
			for _, b := range ccContentBlocks(rec.Message.Content) {
				switch b.Type {
				case "text":
					if strings.TrimSpace(b.Text) != "" {
						emit("assistant_message", ts, map[string]any{"text": b.Text, "error": false})
					}
				case "tool_use":
					if b.ID != "" {
						toolName[b.ID] = b.Name
					}
					emit("tool_call", ts, map[string]any{"name": b.Name, "input": ccRawInput(b.Input)})
				}
			}
		}
	}
	return out
}

// ccWrap classifies a user-message string as a Claude Code slash-command
// wrapper: drop (boilerplate caveat), a compact command event, or none (a
// normal user turn).
type ccWrap int

const (
	ccWrapNone ccWrap = iota
	ccWrapDrop
	ccWrapCommand
)

// ccCommandWrapper detects Claude Code's slash-command bookkeeping. The
// caveat block is identical boilerplate on every command and carries no
// signal, so it is dropped. A <command-name> invocation and a
// <local-command-stdout/stderr> output each become a compact `command`
// event (data: {name, args?} or {output, stream?}).
func ccCommandWrapper(s string) (map[string]any, ccWrap) {
	t := strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(t, "<local-command-caveat>"):
		return nil, ccWrapDrop
	case strings.HasPrefix(t, "<command-name>"):
		name := ccTag(t, "command-name")
		if name == "" {
			return nil, ccWrapNone
		}
		d := map[string]any{"name": name}
		if args := ccTag(t, "command-args"); args != "" {
			d["args"] = args
		}
		return d, ccWrapCommand
	case strings.HasPrefix(t, "<local-command-stdout>"):
		out := ccTag(t, "local-command-stdout")
		if out == "" {
			return nil, ccWrapDrop
		}
		return map[string]any{"output": out}, ccWrapCommand
	case strings.HasPrefix(t, "<local-command-stderr>"):
		out := ccTag(t, "local-command-stderr")
		if out == "" {
			return nil, ccWrapDrop
		}
		return map[string]any{"output": out, "stream": "stderr"}, ccWrapCommand
	}
	return nil, ccWrapNone
}

// ccTag returns the trimmed inner text of the first <tag>...</tag> in s, or "".
func ccTag(s, tag string) string {
	open, closeT := "<"+tag+">", "</"+tag+">"
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	i += len(open)
	j := strings.Index(s[i:], closeT)
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(s[i : i+j])
}

// ccContentString returns (s, true) when a message content is a bare JSON
// string (a plain user turn).
func ccContentString(raw json.RawMessage) (string, bool) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '"' {
		return "", false
	}
	var s string
	if err := json.Unmarshal(t, &s); err != nil {
		return "", false
	}
	return s, true
}

// ccContentBlocks returns the content blocks when a message content is a
// JSON array, else nil.
func ccContentBlocks(raw json.RawMessage) []ccBlock {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || t[0] != '[' {
		return nil
	}
	var blocks []ccBlock
	if err := json.Unmarshal(t, &blocks); err != nil {
		return nil
	}
	return blocks
}

// ccRawInput passes a tool_use input through as nested JSON (like rawInput),
// falling back to "" for absent input.
func ccRawInput(raw json.RawMessage) any {
	if len(bytes.TrimSpace(raw)) == 0 {
		return ""
	}
	return json.RawMessage(raw)
}

// ccPreview renders a tool_result content (string, or an array of content
// blocks) into a capped preview string, mirroring the carlos F12 cap so a
// CC tool result reads identically to a carlos one. Returns (preview,
// truncated).
func ccPreview(raw json.RawMessage) (string, bool) {
	t := bytes.TrimSpace(raw)
	var s string
	switch {
	case len(t) == 0:
		s = ""
	case t[0] == '"':
		_ = json.Unmarshal(t, &s)
	case t[0] == '[':
		var blocks []ccBlock
		if err := json.Unmarshal(t, &blocks); err == nil {
			var sb strings.Builder
			for _, b := range blocks {
				if b.Type == "text" {
					sb.WriteString(b.Text)
				} else {
					// Non-text block (image, tool_reference): a compact tag
					// rather than dumping raw JSON at the UI.
					sb.WriteString("[" + b.Type + "]")
				}
			}
			s = sb.String()
		} else {
			s = string(t)
		}
	default:
		s = string(t)
	}
	if len(s) > chatglue.ToolResultPreviewCap {
		return s[:chatglue.ToolResultPreviewCap], true
	}
	return s, false
}

// ccTimestamp normalizes an on-disk ISO timestamp to the wire's
// millisecond-precision RFC3339, passing the raw value through if it does
// not parse.
func ccTimestamp(ts string) string {
	if ts == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return rfc3339(t)
	}
	return ts
}

// ccScan is the cheap roster projection of a session: enough to build a
// ThreadSummary without producing the full transcript.
type ccScan struct {
	FirstUser string // first plain user turn (title + preview)
	UserMsgs  int    // count of plain user turns
	Cwd       string // session working dir (from the first record)
	Model     string // first assistant model id
	FirstTS   string // first record timestamp (created_at)
}

// ccScanSession walks a session's JSONL once and extracts the roster
// projection (title, count, cwd, model, created). Sidechain records are
// excluded from the user-turn count, the same as the transcript.
func ccScanSession(data []byte) ccScan {
	var sc ccScan
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var rec ccRecord
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if sc.FirstTS == "" && rec.Timestamp != "" {
			sc.FirstTS = rec.Timestamp
		}
		if sc.Cwd == "" && rec.Cwd != "" {
			sc.Cwd = rec.Cwd
		}
		if sc.Model == "" && rec.Type == "assistant" && rec.Message.Model != "" {
			sc.Model = rec.Message.Model
		}
		if rec.IsSidechain || rec.Type != "user" {
			continue
		}
		if s, ok := ccContentString(rec.Message.Content); ok && strings.TrimSpace(s) != "" {
			sc.UserMsgs++
			if sc.FirstUser == "" {
				sc.FirstUser = s
			}
		}
	}
	return sc
}

// truncateRunes clamps s to n runes, appending an ellipsis when clipped.
func truncateRunes(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}
