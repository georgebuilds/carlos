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
