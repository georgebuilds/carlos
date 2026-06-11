package tools

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

// TestStripHTMLTags — fast path (no tags) returns input unchanged; with
// tags it strips them and collapses whitespace.
func TestStripHTMLTags(t *testing.T) {
	if got := stripHTMLTags("plain text"); got != "plain text" {
		t.Errorf("no-tag fast path changed input: %q", got)
	}
	got := stripHTMLTags("a <strong>bold</strong>   word")
	if got != "a bold word" {
		t.Errorf("stripHTMLTags = %q, want 'a bold word'", got)
	}
}

// TestCollectText_DropsScriptStyle — script/style subtrees contribute no
// text; surrounding text is concatenated with single spaces.
func TestCollectText_DropsScriptStyle(t *testing.T) {
	frag := `<div>hello <script>var x=1;</script><style>.a{}</style> world</div>`
	doc, err := html.Parse(strings.NewReader(frag))
	if err != nil {
		t.Fatal(err)
	}
	got := collectText(doc)
	if got != "hello world" {
		t.Errorf("collectText = %q, want 'hello world'", got)
	}
}

// TestAttrVal_PresentAndMissing — returns the attribute value or "".
func TestAttrVal(t *testing.T) {
	doc, err := html.Parse(strings.NewReader(`<a href="/x" data-k="v">t</a>`))
	if err != nil {
		t.Fatal(err)
	}
	// Find the <a> node.
	var a *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			a = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if a == nil {
		t.Fatal("anchor not found")
	}
	if got := attrVal(a, "href"); got != "/x" {
		t.Errorf("attrVal(href) = %q, want /x", got)
	}
	if got := attrVal(a, "nope"); got != "" {
		t.Errorf("attrVal(missing) = %q, want empty", got)
	}
}

// TestNormalizeDuckDuckGoURL_Malformed — redirect wrappers missing the
// uddg param, or with an undecodable value, fall back to the raw href.
func TestNormalizeDuckDuckGoURL_Malformed(t *testing.T) {
	// Has /l/? but no uddg= -> returned unchanged.
	in1 := "//duckduckgo.com/l/?rut=abc"
	if got := normalizeDuckDuckGoURL(in1); got != in1 {
		t.Errorf("no-uddg should be unchanged; got %q", got)
	}
	// uddg with an invalid percent escape -> QueryUnescape errors, raw href returned.
	in2 := "//duckduckgo.com/l/?uddg=%zz"
	if got := normalizeDuckDuckGoURL(in2); got != in2 {
		t.Errorf("bad-escape should fall back to raw; got %q", got)
	}
}

// TestParseDuckDuckGoHTML_NoResults — a document with no result divs
// surfaces a clean parse error so the caller can fall through.
func TestParseDuckDuckGoHTML_NoResults(t *testing.T) {
	_, err := parseDuckDuckGoHTML(`<html><body><p>nothing here</p></body></html>`, 10)
	if err == nil {
		t.Fatal("expected a no-results error")
	}
	if !strings.Contains(err.Error(), "no results") {
		t.Errorf("error = %v, want no-results", err)
	}
}

// TestParseDuckDuckGoHTML_DivSnippet — a result using a <div class=
// "result__snippet"> (rather than <a>) is still captured, and the uddg
// redirect URL is unwrapped. The max cap is honored.
func TestParseDuckDuckGoHTML_DivSnippetAndCap(t *testing.T) {
	body := `<html><body>
		<div class="result">
			<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fa.example%2F1">First</a>
			<div class="result__snippet">snippet one</div>
		</div>
		<div class="result">
			<a class="result__a" href="https://b.example/2">Second</a>
			<a class="result__snippet">snippet two</a>
		</div>
	</body></html>`
	got, err := parseDuckDuckGoHTML(body, 1) // cap at 1
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("max=1 should cap to one result; got %d", len(got))
	}
	if got[0].URL != "https://a.example/1" {
		t.Errorf("first URL = %q, want unwrapped a.example/1", got[0].URL)
	}
	if got[0].Title != "First" {
		t.Errorf("title = %q, want First", got[0].Title)
	}
	if got[0].Snippet != "snippet one" {
		t.Errorf("snippet = %q, want 'snippet one'", got[0].Snippet)
	}
	if got[0].Rank != 1 || got[0].Source != "duckduckgo" {
		t.Errorf("rank/source = %d/%q, want 1/duckduckgo", got[0].Rank, got[0].Source)
	}
}
