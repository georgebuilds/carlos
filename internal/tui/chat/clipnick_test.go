package chat

import (
	"strings"
	"testing"
)

// --- threshold predicate -------------------------------------------------

// TestShouldClipPaste_CharBoundary pins the exact char threshold:
// 280 chars stays inline, 281 clips.
func TestShouldClipPaste_CharBoundary(t *testing.T) {
	if shouldClipPaste(strings.Repeat("a", 280)) {
		t.Error("exactly 280 chars must NOT clip (threshold is strictly greater)")
	}
	if !shouldClipPaste(strings.Repeat("a", 281)) {
		t.Error("281 chars must clip")
	}
	// Rune-counted, not byte-counted: 281 two-byte runes clip, 280 don't.
	if shouldClipPaste(strings.Repeat("é", 280)) {
		t.Error("280 multibyte runes must not clip (count runes, not bytes)")
	}
	if !shouldClipPaste(strings.Repeat("é", 281)) {
		t.Error("281 multibyte runes must clip")
	}
}

// TestShouldClipPaste_LineBoundary pins the exact line threshold:
// 2 lines stay inline, 3 clip - even when the paste is byte-tiny.
func TestShouldClipPaste_LineBoundary(t *testing.T) {
	if shouldClipPaste("a\nb") {
		t.Error("2 short lines must not clip")
	}
	if !shouldClipPaste("a\nb\nc") {
		t.Error("3 lines must clip even when tiny")
	}
	// A trailing newline counts as starting a final (empty) line.
	if !shouldClipPaste("ab\ncd\n") {
		t.Error("2 lines + trailing newline = 3 logical lines, must clip")
	}
	if shouldClipPaste("one line\n") {
		t.Error("single line + trailing newline must not clip")
	}
}

// TestNormalizePaste canonicalizes CRLF and bare CR to LF.
func TestNormalizePaste(t *testing.T) {
	if got := normalizePaste("a\r\nb\rc\nd"); got != "a\nb\nc\nd" {
		t.Errorf("normalizePaste = %q, want %q", got, "a\nb\nc\nd")
	}
}

// --- classifier, one realistic fixture per class -------------------------

const goPanicFixture = `panic: runtime error: invalid memory address or nil pointer dereference
[signal SIGSEGV: segmentation violation code=0x1 addr=0x0 pc=0x102f4a]

goroutine 1 [running]:
main.main()
	/Users/george/Code/carlos/cmd/carlos/main.go:42 +0x1d
exit status 2`

const goDumpFixture = `goroutine 7 [chan receive]:
github.com/georgebuilds/carlos/internal/agent.(*Loop).run(0x14000123456)
	/Users/george/Code/carlos/internal/agent/loop.go:88 +0x44`

const pyTracebackFixture = `Traceback (most recent call last):
  File "train.py", line 14, in <module>
    main()
  File "train.py", line 9, in main
    return 1 / 0
ZeroDivisionError: division by zero`

const gitDiffFixture = `diff --git a/internal/agent/loop.go b/internal/agent/loop.go
index 1a2b3c4..5d6e7f8 100644
--- a/internal/agent/loop.go
+++ b/internal/agent/loop.go
@@ -10,6 +10,7 @@ func run() {
+	log.Println("hi")
diff --git a/internal/agent/loop_test.go b/internal/agent/loop_test.go
--- a/internal/agent/loop_test.go
+++ b/internal/agent/loop_test.go
@@ -1,3 +1,4 @@
+// new`

const plainDiffFixture = `--- old.txt	2026-06-12 10:00:00
+++ new.txt	2026-06-12 10:05:00
@@ -1 +1 @@
-alpha
+beta`

func TestClipNickname_GoTraceback(t *testing.T) {
	if got := clipNickname(goPanicFixture); got != "traceback (panic)" {
		t.Errorf("go panic = %q, want %q", got, "traceback (panic)")
	}
	// A goroutine dump without a panic line is still a Go traceback.
	if got := clipNickname(goDumpFixture); got != "traceback (panic)" {
		t.Errorf("goroutine dump = %q, want %q", got, "traceback (panic)")
	}
}

func TestClipNickname_PythonTraceback(t *testing.T) {
	if got := clipNickname(pyTracebackFixture); got != "traceback (python)" {
		t.Errorf("python traceback = %q, want %q", got, "traceback (python)")
	}
}

func TestClipNickname_Diff(t *testing.T) {
	if got := clipNickname(gitDiffFixture); got != "diff (2 files)" {
		t.Errorf("git diff = %q, want %q", got, "diff (2 files)")
	}
	if got := clipNickname(plainDiffFixture); got != "diff (1 file)" {
		t.Errorf("plain unified diff = %q, want %q", got, "diff (1 file)")
	}
}

func TestClipNickname_JSON(t *testing.T) {
	obj := `{
  "name": "carlos",
  "version": "0.7.7",
  "private": true
}`
	if got := clipNickname(obj); got != "json (3 keys)" {
		t.Errorf("json object = %q, want %q", got, "json (3 keys)")
	}
	if got := clipNickname(`{"only": 1}`); got != "json (1 key)" {
		t.Errorf("single-key object = %q, want %q", got, "json (1 key)")
	}
	if got := clipNickname(`[{"a":1},{"b":2}]`); got != "json (2 items)" {
		t.Errorf("json array = %q, want %q", got, "json (2 items)")
	}
	// Truncated copy: looks like JSON, doesn't parse - falls through to
	// the size fallback instead of lying about the class.
	broken := `{"name": "carlos", "vers`
	if got := clipNickname(broken); got != sizeNickname(broken) {
		t.Errorf("broken json = %q, want size fallback %q", got, sizeNickname(broken))
	}
}

func TestClipNickname_SQL(t *testing.T) {
	sel := "SELECT id, name\nFROM users\nWHERE active = 1;"
	if got := clipNickname(sel); got != "sql (select)" {
		t.Errorf("select = %q, want %q", got, "sql (select)")
	}
	ins := "INSERT INTO events (kind, payload) VALUES ('x', '{}');"
	if got := clipNickname(ins); got != "sql (insert)" {
		t.Errorf("insert = %q, want %q", got, "sql (insert)")
	}
	// Leading "--" comment lines are skipped to find the verb.
	commented := "-- nightly cleanup\nDELETE FROM sessions WHERE expired_at < now();"
	if got := clipNickname(commented); got != "sql (delete)" {
		t.Errorf("commented sql = %q, want %q", got, "sql (delete)")
	}
}

// TestClipNickname_SQLAmbiguity: prose that merely STARTS with a SQL
// verb but carries no SQL grammar must not classify as sql.
func TestClipNickname_SQLAmbiguity(t *testing.T) {
	prose := "delete the old branch\nthen push to origin\nand tag the release"
	if got := clipNickname(prose); got != sizeNickname(prose) {
		t.Errorf("verb-leading prose = %q, want size fallback %q", got, sizeNickname(prose))
	}
}

func TestClipNickname_URLList(t *testing.T) {
	urls := "https://pkg.go.dev/strings\nhttp://example.com/a\n\nhttps://go.dev/blog/error-handling"
	if got := clipNickname(urls); got != "urls (3)" {
		t.Errorf("url list = %q, want %q", got, "urls (3)")
	}
	// One non-URL line disqualifies the class.
	mixed := "https://example.com\nsee also the docs\nhttps://example.org"
	if got := clipNickname(mixed); got != sizeNickname(mixed) {
		t.Errorf("mixed lines = %q, want size fallback %q", got, sizeNickname(mixed))
	}
}

func TestClipNickname_ShellSession(t *testing.T) {
	sess := "$ go test ./...\nok  \tgithub.com/georgebuilds/carlos\t0.41s\n$ git status\nnothing to commit"
	if got := clipNickname(sess); got != "shell (2 cmds)" {
		t.Errorf("shell session = %q, want %q", got, "shell (2 cmds)")
	}
	one := "❯ make build\ngo build -o bin/carlos ./cmd/carlos"
	if got := clipNickname(one); got != "shell (1 cmd)" {
		t.Errorf("single command = %q, want %q", got, "shell (1 cmd)")
	}
}

// TestClipNickname_ShellAmbiguity: markdown headings ("# ") and
// blockquotes ("> ") share prefixes with root/continuation prompts and
// must NOT classify as shell.
func TestClipNickname_ShellAmbiguity(t *testing.T) {
	md := "# Release notes\n> quoted remark\nbody text"
	if got := clipNickname(md); got != sizeNickname(md) {
		t.Errorf("markdown = %q, want size fallback %q", got, sizeNickname(md))
	}
	// Output-first copies (no leading prompt) are not sessions.
	out := "ok  \tcarlos\t0.4s\n$ go vet ./...\nclean"
	if got := clipNickname(out); got != sizeNickname(out) {
		t.Errorf("output-first = %q, want size fallback %q", got, sizeNickname(out))
	}
}

func TestClipNickname_HTML(t *testing.T) {
	doc := "<!DOCTYPE html>\n<html lang=\"en\">\n<head><title>x</title></head>"
	if got := clipNickname(doc); got != "html" {
		t.Errorf("doctype = %q, want html", got)
	}
	frag := `<div class="card">
  <span>hello</span>
</div>`
	if got := clipNickname(frag); got != "html" {
		t.Errorf("fragment = %q, want html", got)
	}
	// XML prologue is deliberately ambiguous: not called html.
	xml := "<?xml version=\"1.0\"?>\n<note>\n<to>x</to>\n</note>"
	if got := clipNickname(xml); got != sizeNickname(xml) {
		t.Errorf("xml = %q, want size fallback %q", got, sizeNickname(xml))
	}
}

// TestClipNickname_PrecedenceDiffOverSQL: a diff touching a .sql file
// reads as a diff (structural classes outrank keyword sniffs).
func TestClipNickname_PrecedenceDiffOverSQL(t *testing.T) {
	d := "diff --git a/schema.sql b/schema.sql\n--- a/schema.sql\n+++ b/schema.sql\n@@ -1 +1 @@\n-SELECT 1 FROM t;\n+SELECT 2 FROM t;"
	if got := clipNickname(d); got != "diff (1 file)" {
		t.Errorf("sql diff = %q, want %q", got, "diff (1 file)")
	}
}

// TestClipNickname_SizeFallback pins the compact fallback label shape.
func TestClipNickname_SizeFallback(t *testing.T) {
	short := strings.Repeat("z", 300)
	if got := clipNickname(short); got != "300·1L" {
		t.Errorf("300 chars = %q, want %q", got, "300·1L")
	}
	long := strings.Repeat("word and more padding here ", 50) // 1350 chars, 1 line
	if got := clipNickname(long); got != "1.4k·1L" {
		t.Errorf("1350 chars = %q, want %q", got, "1.4k·1L")
	}
	multi := strings.Repeat("x\n", 85) + "x" // 171 chars, 86 lines
	if got := clipNickname(multi); got != "171·86L" {
		t.Errorf("86 lines = %q, want %q", got, "171·86L")
	}
}

// TestCompactCount covers the unit boundaries of the size formatter.
func TestCompactCount(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1k"},
		{1234, "1.2k"},
		{12000, "12k"},
		{987654, "987.7k"},
		{2500000, "2.5M"},
	}
	for _, tc := range cases {
		if got := compactCount(tc.n); got != tc.want {
			t.Errorf("compactCount(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// TestClipHead bounds classification to the first 400 runes.
func TestClipHead(t *testing.T) {
	long := strings.Repeat("é", 500)
	if got := len([]rune(clipHead(long))); got != 400 {
		t.Errorf("clipHead runes = %d, want 400", got)
	}
	if got := clipHead("short"); got != "short" {
		t.Errorf("clipHead(short) = %q", got)
	}
}

// TestClipNickname_ClassMarkerPastHeadIgnored: a paste that buries its
// distinctive marker beyond the 400-rune head classifies by what the
// head shows (the fallback) - inspection is bounded by design.
func TestClipNickname_ClassMarkerPastHeadIgnored(t *testing.T) {
	buried := strings.Repeat("log line\n", 60) + pyTracebackFixture
	if got := clipNickname(buried); got != sizeNickname(buried) {
		t.Errorf("buried traceback = %q, want size fallback %q", got, sizeNickname(buried))
	}
}
