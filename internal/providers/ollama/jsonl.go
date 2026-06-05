package ollama

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// parseJSONL reads a line-delimited JSON stream from r, calling onLine
// for each non-empty line in order. Returns on io.EOF (normal stream
// close) or any non-EOF read error.
//
// Framing: one complete JSON object per line, terminated by '\n'. Blank
// lines are skipped (defensive — Ollama doesn't insert them, but some
// proxies and httptest setups normalize newlines and we'd rather not
// trip on a stray empty line).
//
// Buffer sizing follows the Anthropic SSE parser's 1 MiB ceiling. A
// single Ollama chunk is small (model + message + bookkeeping ~~ a few
// hundred bytes), but the FINAL chunk on a turn with a large tool_call
// arguments object can blow past 64 KiB if the model emits a huge JSON
// payload. 1 MiB matches Anthropic's tool_use accumulator limit and
// keeps the two providers symmetric.
func parseJSONL(r io.Reader, onLine func(string) error) error {
	sc := bufio.NewScanner(r)
	buf := make([]byte, 0, 64*1024)
	sc.Buffer(buf, 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if err := onLine(line); err != nil {
			return err
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("ollama/jsonl: scan: %w", err)
	}
	return nil
}
