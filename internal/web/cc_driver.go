package web

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"strings"
	"sync"
)

// cc_driver.go: drives one Claude Code session as a long-lived `claude`
// subprocess over stream-json (B-3). The preflight confirmed: one process
// accepts repeated stdin turns, and tool approval routes through a
// PreToolUse hook injected via --settings (B-4). The driver pumps user
// turns to stdin and reads stdout only for the ephemeral live signal
// (token deltas, turn boundaries), publishing them to the SSE hub exactly
// as carlos's WebTextSource does. Persisted events keep coming from the
// file-tail (cc.go Subscribe), so the SSE seq cursor stays consistent with
// ReadEvents.

// ccHookTimeoutSec is the PreToolUse hook timeout written into the spawned
// session's --settings. Large by design: the hook blocks while the human
// decides in the browser, so this is the "wait as long as the user does"
// parity with the TUI (the preflight flagged the ~60s default as too short).
const ccHookTimeoutSec = 86400 // 24h

// ccDriver is the live handle for one driven CC thread.
type ccDriver struct {
	threadID string
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	publish  func(WireEvent)
	cancel   context.CancelFunc

	mu  sync.Mutex
	buf strings.Builder // live assistant text, for the SSE reconnect snapshot
}

// startCCDriver spawns the claude process for a resumed session and begins
// pumping its stdout. hookCmd, when non-empty, is the PreToolUse hook
// command injected via --settings so every gated tool routes to the browser
// approval bridge; cwd is the session's working directory (--add-dir +
// process Dir). The returned driver outlives the call; stop() tears it down.
func startCCDriver(ctx context.Context, threadID, uuid, cwd, hookCmd string, publish func(WireEvent), newSession bool) (*ccDriver, error) {
	args := []string{
		"-p",
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
		"--permission-mode", "default",
	}
	// A fresh session uses --session-id to mint it; an existing one resumes.
	if newSession {
		args = append(args, "--session-id", uuid)
	} else {
		args = append(args, "--resume", uuid)
	}
	if cwd != "" {
		args = append(args, "--add-dir", cwd)
	}
	if hookCmd != "" {
		args = append(args, "--settings", ccHookSettings(hookCmd))
	}

	dctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(dctx, "claude", args...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	d := &ccDriver{threadID: threadID, cmd: cmd, stdin: stdin, publish: publish, cancel: cancel}
	go d.readLoop(stdout)
	return d, nil
}

// readLoop translates the stream into ephemeral wire events: a `delta` per
// assistant text token (accumulated into the live buffer), and a
// `delta_reset` at each new assistant turn (clearing it). The sealed
// persisted messages arrive separately via the file-tail.
func (d *ccDriver) readLoop(stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	// claude lines can be large (a full assistant message echoed as one
	// line); raise the scanner ceiling well above the 64KB default.
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		sig, text, _ := parseCCStream(sc.Bytes())
		switch sig {
		case ccSigDelta:
			d.mu.Lock()
			d.buf.WriteString(text)
			d.mu.Unlock()
			d.publish(WireEvent{
				Thread: d.threadID, TS: rfc3339(nowUTC()), Kind: "delta",
				Data: map[string]any{"text": text},
			})
		case ccSigTurnStart:
			d.mu.Lock()
			d.buf.Reset()
			d.mu.Unlock()
			d.publish(WireEvent{
				Thread: d.threadID, TS: rfc3339(nowUTC()), Kind: "delta_reset",
				Data: map[string]any{},
			})
		}
	}
}

// send writes one user turn to the process stdin as a stream-json line.
func (d *ccDriver) send(text string) error {
	payload, err := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": text},
	})
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	_, err = d.stdin.Write(append(payload, '\n'))
	return err
}

// liveText returns the in-flight assistant text for the SSE reconnect
// snapshot (spec §9.3 step 4), mirroring carlos's WebTextSource.Get.
func (d *ccDriver) liveText() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.buf.String()
}

// stop kills the process and reaps it. Idempotent via the cancel func.
func (d *ccDriver) stop() {
	d.cancel()
	_ = d.stdin.Close()
	_ = d.cmd.Wait()
}

// ccHookSettings builds the --settings JSON string that installs hookCmd as
// the PreToolUse hook for every tool, with the long timeout so the human has
// time to decide. Injected per-invocation so the user's real
// ~/.claude/settings.json is never touched.
func ccHookSettings(hookCmd string) string {
	s := map[string]any{
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{
					"matcher": "*",
					"hooks": []any{
						map[string]any{"type": "command", "command": hookCmd, "timeout": ccHookTimeoutSec},
					},
				},
			},
		},
	}
	b, _ := json.Marshal(s)
	return string(b)
}
