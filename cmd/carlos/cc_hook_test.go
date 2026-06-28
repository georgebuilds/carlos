package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAskCarlos_AllowAndDeny(t *testing.T) {
	var gotThread, gotTool, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Thread    string          `json:"thread"`
			ToolName  string          `json:"tool_name"`
			ToolInput json.RawMessage `json:"tool_input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotThread, gotTool = body.Thread, body.ToolName
		_ = json.NewEncoder(w).Encode(map[string]string{"decision": "allow"})
	}))
	defer srv.Close()

	d, reason, ok := askCarlos(srv.URL, "tok", "cc:t1", "Bash", json.RawMessage(`{"command":"ls"}`))
	if !ok || d != "allow" {
		t.Fatalf("askCarlos = (%q,%v), want allow/true", d, ok)
	}
	if reason == "" {
		t.Error("allow should carry a reason for the model")
	}
	if gotAuth != "Bearer tok" || gotThread != "cc:t1" || gotTool != "Bash" {
		t.Errorf("server saw auth=%q thread=%q tool=%q", gotAuth, gotThread, gotTool)
	}
}

func TestAskCarlos_DenyDecision(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"decision": "deny"})
	}))
	defer srv.Close()
	d, _, ok := askCarlos(srv.URL, "tok", "cc:t1", "Bash", nil)
	if !ok || d != "deny" {
		t.Errorf("askCarlos = (%q,%v), want deny/true", d, ok)
	}
}

// Fail-safe: any transport or status error must report ok=false so the hook
// denies the tool (never auto-run when carlos cannot reach the human).
func TestAskCarlos_FailsClosed(t *testing.T) {
	// unreachable server
	if _, _, ok := askCarlos("http://127.0.0.1:1/api/cc/hook", "tok", "cc:t1", "Bash", nil); ok {
		t.Error("unreachable carlos should fail closed (ok=false)")
	}
	// non-200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	if _, _, ok := askCarlos(srv.URL, "tok", "cc:t1", "Bash", nil); ok {
		t.Error("non-200 from carlos should fail closed")
	}
	// empty url
	if _, _, ok := askCarlos("", "tok", "cc:t1", "Bash", nil); ok {
		t.Error("empty url should fail closed")
	}
}

// A payload claude's PreToolUse hook cannot parse must deny without ever
// consulting carlos - the fail-safe contract in the package doc. Previously
// a malformed payload fell through to asking about an empty tool name.
func TestCCHookDecision_DeniesMalformedPayload(t *testing.T) {
	asked := false
	d, r := ccHookDecision([]byte("not json"), func(string, json.RawMessage) (string, string, bool) {
		asked = true
		return "allow", "should not happen", true
	})
	if asked {
		t.Error("asker must not be consulted for an unparseable payload")
	}
	if d != "deny" {
		t.Errorf("decision = %q, want deny", d)
	}
	if r == "" {
		t.Error("deny should carry a reason")
	}
}

// Empty stdin (a read that yielded nothing) is also a deny, not an ask
// about a zero-valued tool name.
func TestCCHookDecision_DeniesEmptyStdin(t *testing.T) {
	d, _ := ccHookDecision(nil, func(string, json.RawMessage) (string, string, bool) {
		t.Error("asker must not run on empty stdin")
		return "allow", "", true
	})
	if d != "deny" {
		t.Errorf("decision = %q, want deny", d)
	}
}

// A well-formed payload forwards the tool name to the asker and passes its
// allow decision through.
func TestCCHookDecision_PassesThroughAllow(t *testing.T) {
	raw := []byte(`{"tool_name":"bash","tool_input":{"command":"ls"}}`)
	var gotName string
	d, r := ccHookDecision(raw, func(name string, _ json.RawMessage) (string, string, bool) {
		gotName = name
		return "allow", "approved", true
	})
	if gotName != "bash" {
		t.Errorf("asker saw tool %q, want bash", gotName)
	}
	if d != "allow" || r != "approved" {
		t.Errorf("decision/reason = %q/%q", d, r)
	}
}

// A transport failure (asker ok=false) denies even for a valid payload.
func TestCCHookDecision_DeniesWhenAskerUnavailable(t *testing.T) {
	raw := []byte(`{"tool_name":"bash"}`)
	d, _ := ccHookDecision(raw, func(string, json.RawMessage) (string, string, bool) {
		return "", "", false
	})
	if d != "deny" {
		t.Errorf("decision = %q, want deny when asker fails", d)
	}
}

// The rendered response is always valid PreToolUse JSON.
func TestCCHookResponse_Shape(t *testing.T) {
	out := ccHookResponse("allow", "ok")
	var parsed struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("response is not valid JSON: %v (%s)", err, out)
	}
	if parsed.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Errorf("hookEventName = %q", parsed.HookSpecificOutput.HookEventName)
	}
	if parsed.HookSpecificOutput.PermissionDecision != "allow" {
		t.Errorf("decision = %q", parsed.HookSpecificOutput.PermissionDecision)
	}
}
