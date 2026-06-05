package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func httpReqExec(t *testing.T, tool *HTTPRequestTool, in any) httpRequestResult {
	t.Helper()
	buf, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	out, err := tool.Execute(context.Background(), buf)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	var res httpRequestResult
	if err := json.Unmarshal(out, &res); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}
	return res
}

func httpReqExecErr(t *testing.T, tool *HTTPRequestTool, in any) error {
	t.Helper()
	buf, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	_, err = tool.Execute(context.Background(), buf)
	return err
}

func TestHTTPRequest_GETDefaultsAndStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "want GET", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	tool := &HTTPRequestTool{AllowPrivate: true}
	res := httpReqExec(t, tool, map[string]any{"url": srv.URL})

	if res.Method != "GET" {
		t.Errorf("default method = %q, want GET", res.Method)
	}
	if res.Status != 200 {
		t.Errorf("status = %d, want 200", res.Status)
	}
	if !strings.Contains(res.Body, `"ok":true`) {
		t.Errorf("body = %q, want JSON with ok:true", res.Body)
	}
	if res.Binary {
		t.Error("binary flag set for UTF-8 JSON body")
	}
	if res.Headers["Content-Type"] != "application/json" {
		t.Errorf("Content-Type header missing or wrong: %v", res.Headers)
	}
	if res.ElapsedMs < 0 {
		t.Errorf("ElapsedMs negative: %d", res.ElapsedMs)
	}
}

func TestHTTPRequest_AllVerbsAccepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Method))
	}))
	defer srv.Close()
	tool := &HTTPRequestTool{AllowPrivate: true}
	for _, m := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		res := httpReqExec(t, tool, map[string]any{"url": srv.URL, "method": m})
		if res.Status != 200 {
			t.Errorf("%s: status = %d", m, res.Status)
		}
		if res.Method != m {
			t.Errorf("response method = %q, want %q", res.Method, m)
		}
		// HEAD has no body by spec.
		if m != "HEAD" && res.Body != m {
			t.Errorf("%s: body = %q, want %q", m, res.Body, m)
		}
	}
}

func TestHTTPRequest_RejectsUnsupportedMethod(t *testing.T) {
	tool := &HTTPRequestTool{}
	err := httpReqExecErr(t, tool, map[string]any{"url": "https://example.com", "method": "PROPFIND"})
	if err == nil || !strings.Contains(err.Error(), "unsupported method") {
		t.Errorf("expected unsupported-method error, got %v", err)
	}
}

func TestHTTPRequest_SendsHeadersAndBody(t *testing.T) {
	var gotMethod, gotAuth, gotBody, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"created":true}`))
	}))
	defer srv.Close()
	tool := &HTTPRequestTool{AllowPrivate: true}
	res := httpReqExec(t, tool, map[string]any{
		"url":    srv.URL,
		"method": "POST",
		"headers": map[string]string{
			"Authorization": "Bearer secret-token",
			"Content-Type":  "application/json",
		},
		"body": `{"name":"thing"}`,
	})
	if gotMethod != "POST" {
		t.Errorf("server saw method %q, want POST", gotMethod)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization header lost: %q", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("Content-Type lost: %q", gotCT)
	}
	if gotBody != `{"name":"thing"}` {
		t.Errorf("body lost: %q", gotBody)
	}
	if res.Status != 201 {
		t.Errorf("status = %d, want 201", res.Status)
	}
}

func TestHTTPRequest_NonOKStatusReturnsNotErrors(t *testing.T) {
	// 4xx/5xx aren't tool errors — the model sees the status + body and
	// reacts. Surfacing them as Go errors would deny the model the
	// chance to handle API-side validation failures gracefully.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"errors":["bad field"]}`))
	}))
	defer srv.Close()
	tool := &HTTPRequestTool{AllowPrivate: true}
	res := httpReqExec(t, tool, map[string]any{"url": srv.URL, "method": "POST"})
	if res.Status != 422 {
		t.Errorf("status = %d, want 422", res.Status)
	}
	if !strings.Contains(res.Body, "bad field") {
		t.Errorf("body should carry server message, got %q", res.Body)
	}
}

func TestHTTPRequest_BodyTruncated(t *testing.T) {
	big := strings.Repeat("a", 6*1024*1024) // 6 MiB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(big))
	}))
	defer srv.Close()
	tool := &HTTPRequestTool{AllowPrivate: true, MaxBodyBytes: 1024 * 1024} // 1 MiB cap
	res := httpReqExec(t, tool, map[string]any{"url": srv.URL})
	if !res.Truncated {
		t.Error("truncated flag should be set when response exceeds MaxBodyBytes")
	}
	if len(res.Body) != 1024*1024 {
		t.Errorf("body length = %d, want exactly 1 MiB", len(res.Body))
	}
}

func TestHTTPRequest_BinaryBodyBase64(t *testing.T) {
	// Bytes that aren't valid UTF-8: 0xff 0xfe sequence.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte{0xff, 0xfe, 0xfd, 0xfc})
	}))
	defer srv.Close()
	tool := &HTTPRequestTool{AllowPrivate: true}
	res := httpReqExec(t, tool, map[string]any{"url": srv.URL})
	if !res.Binary {
		t.Error("binary flag should be set for non-UTF-8 body")
	}
	dec, err := base64.StdEncoding.DecodeString(res.Body)
	if err != nil {
		t.Fatalf("body not valid base64: %v", err)
	}
	if string(dec) != "\xff\xfe\xfd\xfc" {
		t.Errorf("decoded body = %x, want ff fe fd fc", dec)
	}
}

func TestHTTPRequest_PrivateAddressRefusedByDefault(t *testing.T) {
	tool := &HTTPRequestTool{}
	err := httpReqExecErr(t, tool, map[string]any{"url": "http://127.0.0.1:9/probe"})
	if err == nil || !strings.Contains(err.Error(), "private") && !strings.Contains(err.Error(), "loopback") {
		t.Errorf("expected private-address refusal, got %v", err)
	}
}

func TestHTTPRequest_PrivateAddressAllowedPerCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	// Tool default AllowPrivate=false; per-call override flips it.
	tool := &HTTPRequestTool{}
	res := httpReqExec(t, tool, map[string]any{
		"url":           srv.URL,
		"allow_private": true,
	})
	if res.Status != 200 {
		t.Errorf("status = %d, want 200 with per-call allow_private", res.Status)
	}
}

func TestHTTPRequest_BadScheme(t *testing.T) {
	tool := &HTTPRequestTool{}
	err := httpReqExecErr(t, tool, map[string]any{"url": "file:///etc/hosts"})
	if err == nil || !strings.Contains(err.Error(), "scheme") {
		t.Errorf("expected scheme refusal, got %v", err)
	}
}

func TestHTTPRequest_EmptyURL(t *testing.T) {
	tool := &HTTPRequestTool{}
	err := httpReqExecErr(t, tool, map[string]any{"url": "  "})
	if err == nil || !strings.Contains(err.Error(), "empty url") {
		t.Errorf("expected empty-url error, got %v", err)
	}
}

func TestHTTPRequest_TimeoutPerCall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte("late"))
	}))
	defer srv.Close()
	tool := &HTTPRequestTool{AllowPrivate: true}
	// Can't send sub-second timeouts (the per-call field is integer
	// seconds); test the boundary by using a very short tool-default
	// instead.
	tool.Timeout = 50 * time.Millisecond
	err := httpReqExecErr(t, tool, map[string]any{"url": srv.URL})
	if err == nil {
		t.Error("expected timeout error")
	}
}

func TestHTTPRequest_MultiValueHeadersFlattened(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("X-Custom", "first")
		w.Header().Add("X-Custom", "second")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	tool := &HTTPRequestTool{AllowPrivate: true}
	res := httpReqExec(t, tool, map[string]any{"url": srv.URL})
	got := res.Headers["X-Custom"]
	if got != "first, second" {
		t.Errorf("multi-value flatten = %q, want %q", got, "first, second")
	}
}

func TestHTTPRequest_RegisteredInDefaultRegistry(t *testing.T) {
	reg := NewDefaultRegistry()
	if _, ok := reg.Get("http_request"); !ok {
		t.Error("http_request not registered in NewDefaultRegistry")
	}
}
