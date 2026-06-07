// Package oacompat implements the OpenAI Chat Completions wire protocol
// shared by carlos's `openai` and `openrouter` providers. Both backends
// speak the same JSON-over-SSE format on /chat/completions, so the parser
// + request builder + stream-to-Event mapper live here. Each provider is
// then a ~50-line adapter over this package that supplies the BaseURL,
// auth header, and any vendor-specific extras (HTTP-Referer/X-Title for
// OpenRouter, none for OpenAI).
//
// Wire-format docs: https://platform.openai.com/docs/api-reference/chat
package oacompat

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// SSEFrame is one parsed event from a server-sent events stream. OpenAI's
// Chat Completions streaming protocol mostly omits the `event:` field and
// just sends `data: {json}\n\n`, plus a single sentinel `data: [DONE]\n\n`
// terminator. We still parse the SSE format generically so that proxies
// (OpenRouter, gateways) which DO set event names round-trip cleanly.
type SSEFrame struct {
	Event string
	Data  string
}

// ParseSSE reads the entire SSE stream from r, calling onFrame for each
// complete frame in order. Returns on io.EOF (normal stream close) or any
// non-EOF read error. If onFrame returns a non-nil error, parsing stops
// and that error is returned to the caller - io.EOF can be used as a
// sentinel "stop cleanly" signal.
//
// Framing rules from the SSE spec we actually need:
//   - Lines beginning with `:` are comments / keepalives → ignore.
//     (OpenRouter sends these as ": OPENROUTER PROCESSING" keepalives
//     while waiting on the upstream provider.)
//   - Lines `field: value` accumulate into the current frame.
//   - An empty line dispatches the current frame.
//   - A single leading space after the colon is conventionally stripped.
//   - We use a bufio.Scanner with a generous buffer because tool_call
//     arguments JSON can be large within a single data line.
//
// OpenAI specifics deliberately NOT handled here (left to the caller):
//   - `data: [DONE]` sentinel: the parser passes it through as a frame with
//     Data == "[DONE]"; the caller treats it as end-of-stream.
//   - Multi-line `data:` per single event: the SSE spec joins them with
//     newlines, which we implement, but OpenAI never emits this in practice.
func ParseSSE(r io.Reader, onFrame func(SSEFrame) error) error {
	sc := bufio.NewScanner(r)
	// 1 MiB is more than enough for any single OpenAI SSE line and well
	// below the default buffer growth ceiling for bufio.Scanner.
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1<<20)
	var cur SSEFrame
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if cur.Event != "" || cur.Data != "" {
				if err := onFrame(cur); err != nil {
					return err
				}
				cur = SSEFrame{}
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // comment / keepalive
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			// Per SSE spec, a line without a colon is a field name with
			// empty value; OpenAI/OpenRouter do not emit this. Treat as no-op.
			continue
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			cur.Event = value
		case "data":
			if cur.Data == "" {
				cur.Data = value
			} else {
				cur.Data = cur.Data + "\n" + value
			}
		default:
			// id / retry / unknown → ignore.
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("oacompat/sse: scan: %w", err)
	}
	// Dispatch a trailing frame if the stream ended without a blank line.
	if cur.Event != "" || cur.Data != "" {
		if err := onFrame(cur); err != nil {
			return err
		}
	}
	return nil
}
