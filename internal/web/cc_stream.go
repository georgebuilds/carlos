package web

import "encoding/json"

// cc_stream.go: the pure parser for Claude Code's stream-json STDOUT (the
// live driving channel, B-3). Distinct from cc_map.go, which projects the
// on-disk JSONL. The driver uses the stream only for the live, ephemeral
// signal: token deltas and turn boundaries. The persisted transcript
// (user/assistant/tool events with stable seqs) still comes from the
// file-tail, so the SSE reconnect cursor stays consistent with ReadEvents.

// ccStreamSig classifies a stream-json line into the only signals the
// driver acts on.
type ccStreamSig int

const (
	ccSigNone      ccStreamSig = iota
	ccSigDelta                 // an assistant text token delta
	ccSigTurnStart             // a new assistant message began (clear live buffer)
	ccSigTurnEnd               // the turn completed (result line)
	ccSigInit                  // system/init: carries session_id + model
)

// ccStreamLine is the unknown-field-tolerant shape of a stream-json line.
type ccStreamLine struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	Event     struct {
		Type  string `json:"type"`
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	} `json:"event"`
}

// parseCCStream classifies a stream-json line and returns the delta text for
// ccSigDelta. Lines it does not care about (full assistant/user records,
// tool events, rate_limit_event) return ccSigNone; the persisted versions of
// those arrive via the file-tail with proper seqs.
func parseCCStream(line []byte) (sig ccStreamSig, text, sessionID string) {
	var l ccStreamLine
	if json.Unmarshal(line, &l) != nil {
		return ccSigNone, "", ""
	}
	switch l.Type {
	case "system":
		if l.Subtype == "init" {
			return ccSigInit, "", l.SessionID
		}
	case "result":
		return ccSigTurnEnd, "", l.SessionID
	case "stream_event":
		switch l.Event.Type {
		case "message_start":
			return ccSigTurnStart, "", l.SessionID
		case "content_block_delta":
			if l.Event.Delta.Type == "text_delta" {
				return ccSigDelta, l.Event.Delta.Text, l.SessionID
			}
		}
	}
	return ccSigNone, "", l.SessionID
}
