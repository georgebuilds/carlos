package web

import (
	"strings"
	"testing"
)

func TestParseCCStream(t *testing.T) {
	cases := []struct {
		name string
		line string
		sig  ccStreamSig
		text string
	}{
		{
			"text delta",
			`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"hel"}}}`,
			ccSigDelta, "hel",
		},
		{
			"thinking delta is not forwarded",
			`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","text":"hmm"}}}`,
			ccSigNone, "",
		},
		{
			"turn start",
			`{"type":"stream_event","event":{"type":"message_start"}}`,
			ccSigTurnStart, "",
		},
		{
			"turn end",
			`{"type":"result","subtype":"success","session_id":"s1"}`,
			ccSigTurnEnd, "",
		},
		{
			"init",
			`{"type":"system","subtype":"init","session_id":"abc","model":"claude-opus-4-8"}`,
			ccSigInit, "",
		},
		{
			"full assistant line carries no live signal",
			`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`,
			ccSigNone, "",
		},
		{"garbage", `not json`, ccSigNone, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sig, text, _ := parseCCStream([]byte(c.line))
			if sig != c.sig || text != c.text {
				t.Errorf("parseCCStream = (%d,%q), want (%d,%q)", sig, text, c.sig, c.text)
			}
		})
	}
}

func TestParseCCStream_InitSessionID(t *testing.T) {
	_, _, sid := parseCCStream([]byte(`{"type":"system","subtype":"init","session_id":"abc-123"}`))
	if sid != "abc-123" {
		t.Errorf("session id = %q, want abc-123", sid)
	}
}

func TestCCHookSettings(t *testing.T) {
	s := ccHookSettings("/bin/carlos cc-hook --thread cc:x")
	// Must be valid JSON installing a PreToolUse command hook with our timeout.
	for _, want := range []string{
		`"PreToolUse"`, `"matcher":"*"`, `"type":"command"`,
		`/bin/carlos cc-hook --thread cc:x`, `"timeout":86400`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("settings %q missing %q", s, want)
		}
	}
}
