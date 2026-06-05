// Package tools — HTTPRequestTool.
//
// Method-parametric HTTP for API consumption, exposed as the
// `http_request` tool. Companion to web_fetch (Slice 11a, which is
// GET + HTML→text for human-readable pages); http_request is for
// the model talking to JSON APIs, GraphQL endpoints, REST services,
// webhooks, anything that wants method + headers + raw body.
//
// Pragmatic guard-rails kept in sync with web_fetch:
//
//   - Scheme allowlist: http + https only.
//   - Private-address refusal (RFC1918 / loopback / link-local) unless
//     the per-call allow_private override or config.WebFetch.AllowPrivate
//     opts in. Same rationale as web_fetch: a tool call shouldn't be
//     able to probe the user's intranet without explicit consent.
//   - 5 MiB raw response cap, declared via the truncated field on the
//     response so the model knows it didn't get everything.
//   - No robots.txt check. robots.txt is the web-page convention; APIs
//     don't ship them. Use web_fetch for pages, http_request for APIs.
//   - No HTML extraction. The body is returned raw (UTF-8 string when
//     valid, base64 + binary=true otherwise).
//
// Auth: the tool takes a headers map. The model passes
// `Authorization: Bearer $TOKEN` (or whatever). Skills are the
// canonical place for the model to learn which env var an API expects
// its token in; the tool itself is auth-agnostic.
package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// HTTPRequestTool registers as the `http_request` tool.
type HTTPRequestTool struct {
	// Client is the HTTP client. Default (nil) builds a client with
	// the per-request Timeout and follows up to MaxRedirects redirects.
	// Tests inject custom clients to swap in httptest transports.
	Client *http.Client
	// Timeout is the per-request hard ceiling. Default 30s — APIs are
	// often slower than static pages; tune via the request's timeout
	// field for hot-path calls.
	Timeout time.Duration
	// MaxBodyBytes caps the raw response body. Default 5 MiB. The
	// truncated flag on the response signals when this fired.
	MaxBodyBytes int64
	// AllowPrivate, when true, permits requests to private addresses
	// without the per-call override. Wired from config.WebFetch.AllowPrivate
	// so http_request and web_fetch share one policy lever.
	AllowPrivate bool
	// MaxRedirects caps the redirect chain. Default 5.
	MaxRedirects int
}

// NewHTTPRequestTool constructs a HTTPRequestTool with default caps.
func NewHTTPRequestTool() *HTTPRequestTool {
	return &HTTPRequestTool{}
}

const (
	defaultHTTPRequestTimeout      = 30 * time.Second
	defaultHTTPRequestMaxBodyBytes = 5 * 1024 * 1024 // 5 MiB
	defaultHTTPRequestMaxRedirects = 5
	httpRequestUserAgent           = "carlos-http_request/1.0"
)

func (*HTTPRequestTool) Name() string { return "http_request" }

func (*HTTPRequestTool) Description() string {
	return "Make a raw HTTP request to an API. Supports any method (GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS), custom headers, and a request body. Returns status + response headers + raw body. Use this for API calls (REST, GraphQL, webhooks). For reading web PAGES rendered as text, use web_fetch instead."
}

func (*HTTPRequestTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"method": {
				"type": "string",
				"description": "HTTP method. Defaults to GET if omitted. One of: GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS."
			},
			"url": {
				"type": "string",
				"description": "Absolute http:// or https:// URL."
			},
			"headers": {
				"type": "object",
				"description": "Request headers as a name→value map. Use this for Authorization, Content-Type, Accept, X-Api-Key, etc.",
				"additionalProperties": {"type": "string"}
			},
			"body": {
				"type": "string",
				"description": "Request body as a string. For JSON, set headers Content-Type: application/json and pass the JSON-encoded body here."
			},
			"timeout_seconds": {
				"type": "integer",
				"description": "Per-request timeout in seconds. Defaults to 30. Hard cap is 300."
			},
			"allow_private": {
				"type": "boolean",
				"description": "Allow requests to private / loopback / link-local addresses. Defaults to false; set true only when the user has explicitly authorized hitting an internal service."
			}
		},
		"required": ["url"]
	}`)
}

type httpRequestInput struct {
	Method         string            `json:"method"`
	URL            string            `json:"url"`
	Headers        map[string]string `json:"headers"`
	Body           string            `json:"body"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	AllowPrivate   *bool             `json:"allow_private"`
}

// httpRequestResult is the JSON returned to the model.
type httpRequestResult struct {
	URL        string            `json:"url"`
	FinalURL   string            `json:"final_url"`
	Method     string            `json:"method"`
	Status     int               `json:"status"`
	StatusText string            `json:"status_text"`
	Headers    map[string]string `json:"headers"`
	Body       string            `json:"body"`
	Binary     bool              `json:"binary,omitempty"`
	Truncated  bool              `json:"truncated,omitempty"`
	ElapsedMs  int64             `json:"elapsed_ms"`
}

// Execute parses input, validates URL + method, fires the request,
// captures the response. Non-2xx status codes are NOT errors here —
// they're returned in the result so the model can react to API errors
// without the tool failing the call.
func (t *HTTPRequestTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	var in httpRequestInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("http_request: parse input: %w", err)
	}
	if strings.TrimSpace(in.URL) == "" {
		return nil, errors.New("http_request: empty url")
	}

	method := strings.ToUpper(strings.TrimSpace(in.Method))
	if method == "" {
		method = http.MethodGet
	}
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch,
		http.MethodDelete, http.MethodHead, http.MethodOptions:
		// allowed
	default:
		return nil, fmt.Errorf("http_request: unsupported method %q (allowed: GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS)", in.Method)
	}

	parsed, err := url.Parse(in.URL)
	if err != nil {
		return nil, fmt.Errorf("http_request: parse url %q: %w", in.URL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("http_request: refused scheme %q (only http/https allowed)", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("http_request: url has no host: %q", in.URL)
	}

	allowPrivate := t.AllowPrivate
	if in.AllowPrivate != nil {
		allowPrivate = *in.AllowPrivate
	}
	if !allowPrivate {
		if private, why := isPrivateHost(parsed.Host); private {
			return nil, fmt.Errorf("http_request: refused %s: %s (set allow_private=true to override with user consent)", parsed.Host, why)
		}
	}

	timeout := time.Duration(in.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = t.Timeout
		if timeout <= 0 {
			timeout = defaultHTTPRequestTimeout
		}
	}
	const maxTimeout = 300 * time.Second
	if timeout > maxTimeout {
		timeout = maxTimeout
	}

	maxBody := t.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = defaultHTTPRequestMaxBodyBytes
	}

	client := t.client(timeout)

	reqBody := io.Reader(nil)
	if in.Body != "" {
		reqBody = strings.NewReader(in.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, parsed.String(), reqBody)
	if err != nil {
		return nil, fmt.Errorf("http_request: build request: %w", err)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", httpRequestUserAgent)
	}
	for k, v := range in.Headers {
		req.Header.Set(k, v)
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http_request: %s %s: %w", method, parsed.String(), err)
	}
	defer closeBody(resp)

	bodyReader := io.LimitReader(resp.Body, maxBody+1)
	raw, err := io.ReadAll(bodyReader)
	if err != nil {
		return nil, fmt.Errorf("http_request: read body: %w", err)
	}
	truncated := false
	if int64(len(raw)) > maxBody {
		raw = raw[:maxBody]
		truncated = true
	}

	bodyStr, binary := encodeBody(raw)

	out := httpRequestResult{
		URL:        in.URL,
		FinalURL:   resp.Request.URL.String(),
		Method:     method,
		Status:     resp.StatusCode,
		StatusText: http.StatusText(resp.StatusCode),
		Headers:    flattenHeaders(resp.Header),
		Body:       bodyStr,
		Binary:     binary,
		Truncated:  truncated,
		ElapsedMs:  time.Since(start).Milliseconds(),
	}
	return json.Marshal(out)
}

// client returns the configured *http.Client or builds a default one
// honoring Timeout + MaxRedirects.
func (t *HTTPRequestTool) client(timeout time.Duration) *http.Client {
	if t.Client != nil {
		return t.Client
	}
	maxRedir := t.MaxRedirects
	if maxRedir <= 0 {
		maxRedir = defaultHTTPRequestMaxRedirects
	}
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedir {
				return fmt.Errorf("stopped after %d redirects", maxRedir)
			}
			return nil
		},
	}
}

// encodeBody returns body either as a UTF-8 string (binary=false) or
// base64-encoded (binary=true). The model handles base64 fine; the
// flag lets it know not to try to parse the body as text.
func encodeBody(raw []byte) (string, bool) {
	if utf8.Valid(raw) {
		return string(raw), false
	}
	return base64.StdEncoding.EncodeToString(raw), true
}

// flattenHeaders collapses http.Header's multi-value lists into a
// single-value map. Comma-joins multi-value headers per RFC 7230 §3.2.2.
// Headers are case-canonicalized by net/http; we keep that as-is for
// the JSON response.
func flattenHeaders(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, vs := range h {
		out[k] = strings.Join(vs, ", ")
	}
	return out
}

// Compile-time: tiny shim so a future test can swap the bytes.Reader
// path without import surgery.
var _ = bytes.NewReader
