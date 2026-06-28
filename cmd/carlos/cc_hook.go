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

	// Fail safe: a stdin we cannot read is treated exactly like a tool we
	// cannot get a human decision for - deny. We do NOT proceed to ask
	// carlos about a zero-valued (empty tool name) payload.
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		raw = nil
	}

	decision, reason := ccHookDecision(raw, func(name string, input json.RawMessage) (string, string, bool) {
		return askCarlos(url, token, thread, name, input)
	})
	fmt.Println(ccHookResponse(decision, reason))
	return nil
}

// ccHookDecision resolves the PreToolUse decision for a raw stdin payload.
// It fails safe: a payload that does not parse (or whose tool the human
// declines to approve) yields "deny". Extracted so the fail-safe path is
// unit-testable without driving os.Stdin.
func ccHookDecision(raw []byte, ask func(name string, input json.RawMessage) (string, string, bool)) (decision, reason string) {
	var hookIn struct {
		ToolName  string          `json:"tool_name"`
		ToolInput json.RawMessage `json:"tool_input"`
	}
	// claude passes {hook_event_name, tool_name, tool_input, session_id, cwd}.
	// A read error or malformed payload denies rather than asking about an
	// empty tool name.
	if err := json.Unmarshal(raw, &hookIn); err != nil {
		return "deny", "carlos approval unavailable"
	}
	if d, r, ok := ask(hookIn.ToolName, hookIn.ToolInput); ok {
		return d, r
	}
	return "deny", "carlos approval unavailable"
}

// ccHookResponse renders the hook's permissionDecision JSON. Marshal cannot
// fail for these string-only fields, but we guard it anyway and fall back
// to a hand-written deny so a marshal bug can never emit a blank line that
// claude might read as "no decision".
func ccHookResponse(decision, reason string) string {
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":            "PreToolUse",
			"permissionDecision":       decision,
			"permissionDecisionReason": reason,
		},
	}
	b, err := json.Marshal(out)
	if err != nil {
		return `{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny","permissionDecisionReason":"carlos approval unavailable"}}`
	}
	return string(b)
}

// askCarlos posts the tool to the carlos web server and waits for the
// decision. No client timeout: the server blocks legitimately while the
// human decides, and claude's own hook timeout bounds the wait. Returns
// ok=false on any transport/status error so the caller fails safe (deny).
func askCarlos(url, token, thread, name string, input json.RawMessage) (decision, reason string, ok bool) {
	if url == "" {
		return "", "", false
	}
	body, err := json.Marshal(map[string]any{
		"thread": thread, "tool_name": name, "tool_input": input,
	})
	if err != nil {
		// Fail safe: never POST an empty body the server would
		// misread; deny instead.
		return "", "", false
	}
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
