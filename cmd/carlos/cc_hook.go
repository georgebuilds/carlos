// cc_hook.go: the `carlos cc-hook` subcommand. It is the PreToolUse hook
// that a `carlos web`-driven Claude Code session runs before every tool
// (installed via --settings, see internal/web/cc_driver.go). It reads the
// tool payload claude passes on stdin, asks the carlos web process for the
// human's decision (which blocks while the browser shows the approval), and
// prints claude's permissionDecision JSON. Fail-safe: any error denies, so a
// tool never runs unapproved when carlos cannot reach the human.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

func runCCHook(args []string) error {
	var url, token, thread string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--url":
			if i+1 < len(args) {
				url = args[i+1]
				i++
			}
		case "--token":
			if i+1 < len(args) {
				token = args[i+1]
				i++
			}
		case "--thread":
			if i+1 < len(args) {
				thread = args[i+1]
				i++
			}
		}
	}

	// claude passes {hook_event_name, tool_name, tool_input, session_id, cwd}.
	var hookIn struct {
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	}
	if raw, err := io.ReadAll(os.Stdin); err == nil {
		_ = json.Unmarshal(raw, &hookIn)
	}

	decision, reason := "deny", "carlos approval unavailable"
	if d, r, ok := askCarlos(url, token, thread, hookIn.ToolName, hookIn.ToolInput); ok {
		decision, reason = d, r
	}

	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       decision,
			"permissionDecisionReason": reason,
		},
	}
	b, _ := json.Marshal(out)
	fmt.Println(string(b))
	return nil
}

// askCarlos posts the tool to the carlos web server and waits for the
// decision. No client timeout: the server blocks legitimately while the
// human decides, and claude's own hook timeout bounds the wait. Returns
// ok=false on any transport/status error so the caller fails safe (deny).
func askCarlos(url, token, thread, name string, input json.RawMessage) (decision, reason string, ok bool) {
	if url == "" {
		return "", "", false
	}
	body, _ := json.Marshal(map[string]any{
		"thread": thread, "tool_name": name, "tool_input": input,
	})
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", "", false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", false
	}
	var out struct {
		Decision string `json:"decision"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", false
	}
	if out.Decision == "allow" {
		return "allow", "approved in carlos web", true
	}
	return "deny", "denied in carlos web", true
}
