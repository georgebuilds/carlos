package anthropic

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// sseFrame is one parsed event from a server-sent events stream. The
// Anthropic streaming protocol always sets `event:` and `data:` lines per
// frame; `id:` and `retry:` exist in the SSE spec but Anthropic doesn't
// use them, so we don't surface them.
type sseFrame struct {
	Event string
	Data  string
}

// parseSSE reads the entire SSE stream from r, calling onFrame for each
// complete frame in order. Returns on io.EOF (normal stream close) or any
// non-EOF read error.
//
// Framing rules from the SSE spec we actually need:
//   - Lines beginning with `:` are comments → ignore.
//   - Lines `field: value` accumulate into the current frame.
//   - An empty line dispatches the current frame.
//   - Anthropic always packs event+data into a single dispatched frame.
//   - We use a bufio.Scanner with a generous buffer because tool_use
//     input JSON can be large within a single data line.
func parseSSE(r io.Reader, onFrame func(sseFrame) error) error {
	sc := bufio.NewScanner(r)
	// 1 MiB is more than enough for any single Anthropic SSE line and
	// well below the default buffer growth ceiling for bufio.Scanner.
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1<<20)
	var cur sseFrame
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if cur.Event != "" || cur.Data != "" {
				if err := onFrame(cur); err != nil {
					return err
				}
				cur = sseFrame{}
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue // comment / keepalive
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			// Per SSE spec, a line without a colon is a field name
			// with empty value; we don't see this from Anthropic
			// in practice. Treat as no-op.
			continue
		}
		// A single leading space after the colon is conventionally stripped.
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			cur.Event = value
		case "data":
			// Anthropic sends single-line JSON per data field; the
			// SSE spec allows multi-line data which would concatenate
			// with newlines, but we never see that here. Stick with
			// the simple case.
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
		return fmt.Errorf("anthropic/sse: scan: %w", err)
	}
	// Dispatch a trailing frame if the stream ended without a blank line.
	if cur.Event != "" || cur.Data != "" {
		if err := onFrame(cur); err != nil {
			return err
		}
	}
	return nil
}
