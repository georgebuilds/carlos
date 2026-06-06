// Package tools — WebFetchTool (Slice 11a).
//
// HTTP GET with HTML-to-text extraction, exposed as the `web_fetch`
// tool to the model. Pragmatic guard-rails:
//
//   - Scheme allowlist: http + https only. file:// and friends are
//     refused so a tool call can't trivially exfiltrate local files.
//   - Private-address refusal (RFC1918 / loopback / link-local).
//     Disabled when config.WebFetch.AllowPrivate=true.
//   - robots.txt honoured by default. The model can flip
//     respect_robots=false on a per-call basis when the user has
//     explicitly authorised it.
//   - 5 MiB raw response cap, 256 KiB extracted-text cap. Truncation
//     is marked clearly so the model knows it only saw part.
//   - text/* content-types only. Refuses image/video/binary; the
//     model can use `read` on a downloaded file if it needs bytes.
//   - No JavaScript execution. If a page looks JS-only (extracted
//     text is empty but the body had a non-trivial size), we surface
//     a clean "requires JavaScript" hint so the model adapts.
//
// The implementation mirrors verifier_urls.go's HEAD-then-GET pattern
// for the cheap content-type / size pre-check.
package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

// WebFetchTool registers as the `web_fetch` tool.
type WebFetchTool struct {
	// Client is the HTTP client. Default (nil) builds a client with
	// the per-request Timeout below and follows up to 5 redirects.
	// Tests inject custom clients to swap in httptest transports.
	Client *http.Client
	// Timeout is the per-request hard ceiling. Default 15s.
	Timeout time.Duration
	// MaxBodyBytes caps the raw response body. Default 5 MiB.
	MaxBodyBytes int64
	// MaxTextBytes caps the extracted text after HTML walking.
	// Default 256 KiB.
	MaxTextBytes int
	// AllowPrivate, when true, allows fetches to RFC1918 / loopback
	// / link-local addresses. The factory wires this from config.
	AllowPrivate bool
	// MaxRedirects caps the redirect chain. Default 5.
	MaxRedirects int

	// UserAgent, when non-empty, overrides the default polite-bot UA
	// (webFetchUserAgent). Used by `carlos research` to identify as
	// a real browser since many listing sites (Yelp, DoorDash,
	// Superpages, YellowPages) return HTTP 403 to anything that
	// announces itself as a bot. Model-side tool calls leave this
	// empty so the model's fetches stay transparently labelled.
	UserAgent string

	// robotsCache is an in-process robots.txt cache (5-min TTL).
	robotsCache sync.Map // host → robotsEntry
}

// NewWebFetchTool constructs a WebFetchTool with default caps.
func NewWebFetchTool() *WebFetchTool {
	return &WebFetchTool{}
}

// defaults — exported as constants so tests can reference them.
const (
	defaultWebFetchTimeout      = 15 * time.Second
	defaultWebFetchMaxBodyBytes = 5 * 1024 * 1024 // 5 MiB raw
	defaultWebFetchMaxTextBytes = 256 * 1024      // 256 KiB extracted
	defaultWebFetchMaxRedirects = 5
	webFetchUserAgent           = "carlos-web_fetch/1.0 (+phase11a)"
	robotsCacheTTL              = 5 * time.Minute
)

func (*WebFetchTool) Name() string { return "web_fetch" }

func (*WebFetchTool) Description() string {
	return "Fetch an absolute http(s) URL and return its extracted text. Honors robots.txt by default; refuses non-text content (images/binaries) and private-network addresses. Body capped at 5 MiB, extracted text capped at 256 KiB. Use when the model needs to read a page's content; for images/binaries the model should use `read` on a locally-downloaded file instead."
}

func (*WebFetchTool) Schema() []byte {
	return []byte(`{
		"type": "object",
		"properties": {
			"url": {
				"type": "string",
				"description": "Absolute http:// or https:// URL to fetch."
			},
			"respect_robots": {
				"type": "boolean",
				"description": "Honor robots.txt (default true). Set false only when the user has explicitly authorized fetching despite robots."
			}
		},
		"required": ["url"]
	}`)
}

type webFetchInput struct {
	URL           string `json:"url"`
	RespectRobots *bool  `json:"respect_robots,omitempty"`
}

// webFetchResult is the JSON returned to the model.
type webFetchResult struct {
	URL       string `json:"url"`
	FinalURL  string `json:"final_url"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	FetchedAt string `json:"fetched_at"`
}

// Execute parses input, validates the URL, optionally checks robots.txt,
// fetches with HEAD+GET, extracts text, and returns the JSON payload.
func (t *WebFetchTool) Execute(ctx context.Context, input []byte) ([]byte, error) {
	var in webFetchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return nil, fmt.Errorf("web_fetch: parse input: %w", err)
	}
	if strings.TrimSpace(in.URL) == "" {
		return nil, errors.New("web_fetch: empty url")
	}

	respectRobots := true
	if in.RespectRobots != nil {
		respectRobots = *in.RespectRobots
	}

	parsed, err := url.Parse(in.URL)
	if err != nil {
		return nil, fmt.Errorf("web_fetch: parse url %q: %w", in.URL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("web_fetch: refused scheme %q (only http/https allowed)", parsed.Scheme)
	}
	if parsed.Host == "" {
		return nil, fmt.Errorf("web_fetch: url has no host: %q", in.URL)
	}
	if !t.AllowPrivate {
		if private, why := isPrivateHost(parsed.Host); private {
			return nil, fmt.Errorf("web_fetch: refused %s: %s (set web_fetch.allow_private=true in config to override)", parsed.Host, why)
		}
	}

	client := t.client()

	if respectRobots {
		allowed, err := t.checkRobots(ctx, client, parsed)
		if err != nil {
			// robots.txt failures (404, network error) are NOT fatal —
			// per RFC 9309 a missing robots.txt means "allow all".
			// We only block on an explicit Disallow.
			_ = err
		}
		if !allowed {
			return nil, fmt.Errorf("web_fetch: robots.txt disallows %s (pass respect_robots=false to override with user consent)", parsed.String())
		}
	}

	maxBody := t.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = defaultWebFetchMaxBodyBytes
	}
	maxText := t.MaxTextBytes
	if maxText <= 0 {
		maxText = defaultWebFetchMaxTextBytes
	}

	// HEAD probe — cheap pre-check of content-type and size.
	// Errors here are non-fatal; some servers refuse HEAD entirely. We
	// proceed to GET and let it bite there if the content really is
	// non-text or oversized.
	headResp, headErr := t.do(ctx, client, http.MethodHead, parsed.String())
	if headErr == nil {
		closeBody(headResp)
		if ct := headResp.Header.Get("Content-Type"); ct != "" {
			if err := requireTextContentType(ct); err != nil {
				return nil, fmt.Errorf("web_fetch: %s: %w", parsed.String(), err)
			}
		}
		// Content-Length sanity check: refuse before GET if obviously
		// huge. We still cap the GET body in case the server lies.
		if cl := headResp.ContentLength; cl > 0 && cl > maxBody*4 {
			return nil, fmt.Errorf("web_fetch: %s: declared Content-Length %d exceeds 4x cap %d", parsed.String(), cl, maxBody)
		}
	}

	resp, err := t.do(ctx, client, http.MethodGet, parsed.String())
	if err != nil {
		return nil, fmt.Errorf("web_fetch: GET %s: %w", parsed.String(), err)
	}
	defer closeBody(resp)

	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("web_fetch: %s returned HTTP %d", parsed.String(), resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "text/html" // sloppy server; assume HTML and let parser cope
	}
	if err := requireTextContentType(ct); err != nil {
		return nil, fmt.Errorf("web_fetch: %s: %w", parsed.String(), err)
	}

	// Read with hard cap. We always read maxBody+1 to detect "we hit
	// the cap" vs "this was exactly the body size".
	limited := io.LimitReader(resp.Body, maxBody+1)
	bodyBuf, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("web_fetch: read body %s: %w", parsed.String(), err)
	}
	bodyTruncated := false
	if int64(len(bodyBuf)) > maxBody {
		bodyBuf = bodyBuf[:maxBody]
		bodyTruncated = true
	}

	title, text := extractFromContentType(ct, bodyBuf)
	if bodyTruncated {
		text += "\n\n(... raw body truncated at " + humanBytes(maxBody) + ")"
	}
	textTruncated := false
	if len(text) > maxText {
		text = text[:maxText]
		textTruncated = true
	}
	if textTruncated {
		text += "\n\n(... extracted text truncated at " + humanBytes(int64(maxText)) + ")"
	}

	// Detect JS-only pages: HTML body had non-trivial size but text is
	// nearly empty. This is a hint to the model, not an error.
	if isHTMLContentType(ct) && len(bodyBuf) > 4096 && len(strings.TrimSpace(text)) < 64 {
		text = "(this page appears to require JavaScript to render; carlos does not execute scripts)\n\n" + text
	}

	finalURL := parsed.String()
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}

	out := webFetchResult{
		URL:       in.URL,
		FinalURL:  finalURL,
		Title:     title,
		Content:   text,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
	}
	return json.Marshal(out)
}

// client returns the HTTP client to use, building a default if none
// has been injected. The default follows up to MaxRedirects redirects
// and enforces Timeout per request.
func (t *WebFetchTool) client() *http.Client {
	if t.Client != nil {
		return t.Client
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = defaultWebFetchTimeout
	}
	maxRedirects := t.MaxRedirects
	if maxRedirects <= 0 {
		maxRedirects = defaultWebFetchMaxRedirects
	}
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("web_fetch: too many redirects (max %d)", maxRedirects)
			}
			return nil
		},
	}
}

// do issues a single request with the polite UA. ctx flows through.
func (t *WebFetchTool) do(ctx context.Context, client *http.Client, method, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	ua := webFetchUserAgent
	if t.UserAgent != "" {
		ua = t.UserAgent
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,text/plain,text/markdown,text/*;q=0.9,*/*;q=0.1")
	return client.Do(req)
}

// closeBody drains and closes resp.Body. Safe with nil resp.
func closeBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	_ = resp.Body.Close()
}

// requireTextContentType returns an error if ct is not text/*.
func requireTextContentType(ct string) error {
	// content-type may have a charset suffix, e.g. "text/html; charset=utf-8"
	main := strings.TrimSpace(strings.SplitN(ct, ";", 2)[0])
	main = strings.ToLower(main)
	if strings.HasPrefix(main, "text/") {
		return nil
	}
	// application/xhtml+xml is HTML-equivalent; treat as text.
	if main == "application/xhtml+xml" {
		return nil
	}
	return fmt.Errorf("non-text content type %q refused (use `read` on a locally-downloaded file for binary content)", ct)
}

func isHTMLContentType(ct string) bool {
	main := strings.TrimSpace(strings.SplitN(ct, ";", 2)[0])
	main = strings.ToLower(main)
	return main == "text/html" || main == "application/xhtml+xml"
}

// extractFromContentType picks the right extractor based on ct.
// HTML → walk the parse tree; everything else (text/plain, text/markdown
// etc.) → return as-is with whitespace normalised.
func extractFromContentType(ct string, body []byte) (title, text string) {
	if isHTMLContentType(ct) {
		return extractHTML(body)
	}
	return "", normalizeWhitespace(string(body))
}

// extractHTML walks an HTML document and returns the document title +
// the visible text. The walker drops script/style/nav/footer/aside/form
// subtrees (the typical "chrome" of a page), collapses runs of
// whitespace into single spaces, and inserts paragraph breaks at block
// boundaries so the output is roughly readable as prose.
func extractHTML(body []byte) (title, text string) {
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		// Parse errors are unusual — html.Parse is famously tolerant.
		// Fall back to a raw normalize so the model still sees something.
		return "", normalizeWhitespace(string(body))
	}

	var sb strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "script", "style", "noscript", "nav", "footer", "aside", "form", "iframe", "svg", "template":
				return
			case "title":
				if title == "" {
					title = strings.TrimSpace(textOf(n))
				}
				return
			}
			if isBlockElement(n.Data) {
				// Mark paragraph boundary before recursing so block
				// content starts on a new line.
				ensureTrailingBlock(&sb)
			}
		}
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && isBlockElement(n.Data) {
			ensureTrailingBlock(&sb)
		}
	}
	walk(doc)
	return title, normalizeWhitespace(sb.String())
}

// textOf returns the concatenated text content of n's descendants.
func textOf(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}

// isBlockElement reports whether tag is a block-level HTML element.
// Used to decide where to insert paragraph breaks in the extracted text.
func isBlockElement(tag string) bool {
	switch strings.ToLower(tag) {
	case "p", "div", "section", "article", "main", "header",
		"h1", "h2", "h3", "h4", "h5", "h6",
		"li", "ul", "ol", "dl", "dt", "dd",
		"blockquote", "pre", "hr", "br",
		"table", "tr", "td", "th", "thead", "tbody", "tfoot",
		"figure", "figcaption":
		return true
	}
	return false
}

// ensureTrailingBlock appends a newline marker if the buffer doesn't
// already end with one. We use "\n\n" as the paragraph separator so
// normalizeWhitespace can preserve it.
func ensureTrailingBlock(sb *strings.Builder) {
	s := sb.String()
	if s == "" {
		return
	}
	if !strings.HasSuffix(s, "\n\n") {
		if strings.HasSuffix(s, "\n") {
			sb.WriteString("\n")
		} else {
			sb.WriteString("\n\n")
		}
	}
}

// normalizeWhitespace collapses runs of inline whitespace to a single
// space, preserves paragraph breaks (\n\n), and trims leading/trailing
// whitespace per line.
func normalizeWhitespace(s string) string {
	// First pass: split on \n\n to get paragraphs.
	paras := strings.Split(s, "\n\n")
	cleaned := make([]string, 0, len(paras))
	for _, p := range paras {
		// Inside a paragraph: collapse any run of whitespace
		// (space/tab/newline) to a single space.
		var sb strings.Builder
		prevSpace := true // suppress leading whitespace
		for _, r := range p {
			if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
				if !prevSpace {
					sb.WriteByte(' ')
					prevSpace = true
				}
				continue
			}
			sb.WriteRune(r)
			prevSpace = false
		}
		trimmed := strings.TrimSpace(sb.String())
		if trimmed != "" {
			cleaned = append(cleaned, trimmed)
		}
	}
	return strings.Join(cleaned, "\n\n")
}

// humanBytes renders byte counts as "N KiB" or "N MiB" for log lines.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%d MiB", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%d KiB", n>>10)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// --- private-address detection ---

// isPrivateHost returns (true, reason) when host resolves to a private
// (RFC1918) / loopback / link-local / unique-local / multicast address,
// or when host is a literal local hostname (localhost). This is a
// conservative check; we err toward refusing rather than risk an
// inadvertent local-network probe via a tool the model can invoke.
func isPrivateHost(hostport string) (bool, string) {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	lower := strings.ToLower(host)
	if lower == "localhost" || lower == "localhost." || strings.HasSuffix(lower, ".localhost") {
		return true, "loopback hostname"
	}
	// Literal IP fast path.
	if ip := net.ParseIP(host); ip != nil {
		return isPrivateIP(ip)
	}
	// Hostname → resolve, but with a short timeout so a bogus DNS
	// can't hang the tool. We use the default resolver here; in
	// production a separate dialer would let us swap this out, but
	// the cost is negligible (one A lookup) and the path runs once
	// per call.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		// DNS failure is not "private" — surface it to the GET path
		// which will fail with a clearer error.
		return false, ""
	}
	for _, a := range addrs {
		if priv, why := isPrivateIP(a.IP); priv {
			return true, "resolves to " + a.IP.String() + " (" + why + ")"
		}
	}
	return false, ""
}

// isPrivateIP returns (true, reason) for loopback / private / link-local
// / unique-local / multicast / unspecified addresses.
func isPrivateIP(ip net.IP) (bool, string) {
	if ip == nil {
		return false, ""
	}
	if ip.IsLoopback() {
		return true, "loopback"
	}
	if ip.IsUnspecified() {
		return true, "unspecified"
	}
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true, "link-local"
	}
	if ip.IsMulticast() {
		return true, "multicast"
	}
	if ip.IsPrivate() {
		return true, "private (RFC1918 / ULA)"
	}
	return false, ""
}

// --- robots.txt ---

type robotsEntry struct {
	disallows []string // path prefixes disallowed for our UA
	expiresAt time.Time
}

// checkRobots returns (allowed, error). A network or parse error is
// non-fatal (per RFC 9309 "missing robots.txt" means allow); the caller
// ignores the error and treats the request as allowed.
func (t *WebFetchTool) checkRobots(ctx context.Context, client *http.Client, target *url.URL) (bool, error) {
	host := target.Host
	if v, ok := t.robotsCache.Load(host); ok {
		if e, ok := v.(robotsEntry); ok && time.Now().Before(e.expiresAt) {
			return robotsAllows(e.disallows, target.Path), nil
		}
	}

	robotsURL := &url.URL{Scheme: target.Scheme, Host: target.Host, Path: "/robots.txt"}
	resp, err := t.do(ctx, client, http.MethodGet, robotsURL.String())
	if err != nil {
		// Cache the "allow all" verdict so we don't hammer a host that
		// can't serve robots.txt.
		t.robotsCache.Store(host, robotsEntry{expiresAt: time.Now().Add(robotsCacheTTL)})
		return true, err
	}
	defer closeBody(resp)
	if resp.StatusCode == 404 || resp.StatusCode >= 500 {
		// Missing or server error → allow per spec.
		t.robotsCache.Store(host, robotsEntry{expiresAt: time.Now().Add(robotsCacheTTL)})
		return true, nil
	}
	if resp.StatusCode >= 400 {
		// 401/403 on robots.txt: be cautious and DISALLOW. This is
		// stricter than the spec but matches what most polite bots do.
		t.robotsCache.Store(host, robotsEntry{disallows: []string{"/"}, expiresAt: time.Now().Add(robotsCacheTTL)})
		return robotsAllows([]string{"/"}, target.Path), nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		t.robotsCache.Store(host, robotsEntry{expiresAt: time.Now().Add(robotsCacheTTL)})
		return true, err
	}
	disallows := parseRobots(string(body))
	t.robotsCache.Store(host, robotsEntry{
		disallows: disallows,
		expiresAt: time.Now().Add(robotsCacheTTL),
	})
	return robotsAllows(disallows, target.Path), nil
}

// parseRobots is a minimal robots.txt parser: it understands
// User-agent and Disallow directives, applied to the "*" group (we
// don't try to match our specific UA — overkill for the use case).
// Multi-group records are merged.
//
// This is intentionally not a full RFC 9309 implementation. The full
// spec includes Allow, wildcards, longest-match precedence, etc.
// Pragmatic fetch tool: we honor the common case (sites Disallow
// `/private/`) and don't try to be cleverer than that.
func parseRobots(body string) []string {
	var out []string
	inStar := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Strip trailing comment.
		if i := strings.Index(line, "#"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		colon := strings.Index(line, ":")
		if colon < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:colon]))
		val := strings.TrimSpace(line[colon+1:])
		switch key {
		case "user-agent":
			inStar = (val == "*")
		case "disallow":
			if inStar && val != "" {
				out = append(out, val)
			}
		}
	}
	return out
}

// robotsAllows returns true iff path is NOT covered by any prefix in
// disallows. An empty disallows list always allows.
func robotsAllows(disallows []string, path string) bool {
	if path == "" {
		path = "/"
	}
	for _, d := range disallows {
		if d == "" {
			continue
		}
		if strings.HasPrefix(path, d) {
			return false
		}
	}
	return true
}

// Compile-time check: WebFetchTool implements Tool.
var _ Tool = (*WebFetchTool)(nil)
