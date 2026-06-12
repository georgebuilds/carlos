// Large-paste clipping heuristics (roadmap slice I-2).
//
// When a bracketed paste crosses the clip threshold the composer turns
// it into a single inline chip (see composer.go InsertChip) instead of
// flooding the textarea. The chip needs a short, human-meaningful
// nickname, so clipNickname sniffs the paste and labels it by class:
// traceback, json, diff, sql, urls, shell, html - falling back to a
// compact size label ("1.2k·86L") when nothing matches.
//
// Everything in this file is a pure function of the paste text:
// deterministic, no Model access, no styling. Classification reads
// only the first ~400 runes (clipHead) so a multi-megabyte paste stays
// cheap; the count suffixes (keys / files / urls / cmds) scan the full
// content because they are single linear passes.

package chat

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Clip threshold: a paste becomes a chip when it EXCEEDS
// pasteClipChars runes or spans pasteClipLines lines or more. The char
// bound (~3-4 terminal rows of text) keeps short one-liners inline;
// the line bound catches the "small but structurally multi-line" paste
// (stack frames, diffs) that wrecks the 3-row composer even when it is
// byte-cheap.
const (
	pasteClipChars = 280 // clip strictly above this rune count
	pasteClipLines = 3   // clip at or above this line count
)

// normalizePaste canonicalizes line endings (CRLF / bare CR -> LF) so
// threshold math, classification, and the persisted attachment all see
// one newline convention regardless of the source clipboard.
func normalizePaste(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\r", "\n")
}

// pasteLineCount counts logical lines the way an editor does: newline
// SEPARATORS plus one, so "a\nb" is 2 lines and a trailing newline
// adds a (deliberately counted) final empty line.
func pasteLineCount(s string) int {
	return strings.Count(s, "\n") + 1
}

// shouldClipPaste is the threshold predicate: >pasteClipChars runes OR
// >=pasteClipLines lines. Exactly 280 chars on one line stays inline;
// 281 clips. Two lines stay inline; three clip.
func shouldClipPaste(s string) bool {
	return utf8.RuneCountInString(s) > pasteClipChars ||
		pasteLineCount(s) >= pasteClipLines
}

// clipHeadRunes bounds how much of the paste classification inspects.
const clipHeadRunes = 400

// clipHead returns the first clipHeadRunes runes of s.
func clipHead(s string) string {
	if utf8.RuneCountInString(s) <= clipHeadRunes {
		return s
	}
	return string([]rune(s)[:clipHeadRunes])
}

// clipNickname classifies a (normalized) paste and returns its chip
// nickname. Order matters: the structurally unambiguous classes
// (tracebacks, diffs, parseable JSON) run before the looser keyword /
// prefix sniffs so e.g. a diff of a .sql file labels as diff, not sql.
func clipNickname(content string) string {
	head := clipHead(content)
	switch {
	case isGoTraceback(head):
		return "traceback (panic)"
	case isPyTraceback(head):
		return "traceback (python)"
	case isUnifiedDiff(head):
		return "diff (" + countNoun(diffFileCount(content), "file", "files") + ")"
	}
	if label, ok := jsonNickname(content); ok {
		return label
	}
	if verb, ok := sqlVerb(head); ok {
		return "sql (" + verb + ")"
	}
	if n, ok := urlListCount(content); ok {
		return "urls (" + strconv.Itoa(n) + ")"
	}
	if n, ok := shellCmdCount(content); ok {
		return "shell (" + countNoun(n, "cmd", "cmds") + ")"
	}
	if isHTML(head) {
		return "html"
	}
	return sizeNickname(content)
}

// countNoun renders "1 file" / "3 files" style count phrases.
func countNoun(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return strconv.Itoa(n) + " " + plural
}

// goroutineRe matches a Go stack-dump goroutine header at line start.
var goroutineRe = regexp.MustCompile(`^goroutine \d+ \[`)

// isGoTraceback detects Go panics / stack dumps: a "panic:" line or a
// "goroutine N [state]:" header anywhere in the head.
func isGoTraceback(head string) bool {
	for _, ln := range strings.Split(head, "\n") {
		if strings.HasPrefix(ln, "panic:") || goroutineRe.MatchString(ln) {
			return true
		}
	}
	return false
}

// isPyTraceback detects CPython's canonical traceback banner.
func isPyTraceback(head string) bool {
	return strings.Contains(head, "Traceback (most recent call last)")
}

// isUnifiedDiff detects git/unified diffs: a "diff --git" header, or
// the "--- " / "+++ " file-marker pair (both required, so a lone
// markdown horizontal rule never matches).
func isUnifiedDiff(head string) bool {
	hasMinus, hasPlus := false, false
	for _, ln := range strings.Split(head, "\n") {
		switch {
		case strings.HasPrefix(ln, "diff --git "):
			return true
		case strings.HasPrefix(ln, "--- "):
			hasMinus = true
		case strings.HasPrefix(ln, "+++ "):
			hasPlus = true
		}
	}
	return hasMinus && hasPlus
}

// diffFileCount counts the files a diff touches: "diff --git" headers
// when present (git form), otherwise "+++ " markers (plain unified
// form). Floors at 1 because isUnifiedDiff already proved a file pair
// exists somewhere (possibly past the classification head).
func diffFileCount(content string) int {
	git, plus := 0, 0
	for _, ln := range strings.Split(content, "\n") {
		switch {
		case strings.HasPrefix(ln, "diff --git "):
			git++
		case strings.HasPrefix(ln, "+++ "):
			plus++
		}
	}
	if git > 0 {
		return git
	}
	if plus > 0 {
		return plus
	}
	return 1
}

// jsonNickname labels a paste that parses wholesale as a JSON object
// or array - "json (24 keys)" / "json (3 items)". Anything that merely
// LOOKS like JSON but fails to parse (truncated copy, JS source) falls
// through to the later classes.
func jsonNickname(content string) (string, bool) {
	t := strings.TrimSpace(content)
	switch {
	case strings.HasPrefix(t, "{"):
		var obj map[string]json.RawMessage
		if json.Unmarshal([]byte(t), &obj) != nil {
			return "", false
		}
		return "json (" + countNoun(len(obj), "key", "keys") + ")", true
	case strings.HasPrefix(t, "["):
		var arr []json.RawMessage
		if json.Unmarshal([]byte(t), &arr) != nil {
			return "", false
		}
		return "json (" + countNoun(len(arr), "item", "items") + ")", true
	}
	return "", false
}

// sqlKeywords are the statement-opening verbs sqlVerb recognizes.
// "with" is deliberately absent: prose starts sentences with it far
// too often for a prefix sniff.
var sqlKeywords = []string{
	"select", "insert", "update", "delete", "create", "alter", "drop", "explain",
}

// sqlSignals are the secondary tokens that must ALSO appear in the
// head before a keyword-opening paste is called SQL - "delete the old
// branch please" starts with a keyword but carries no SQL grammar.
var sqlSignals = []string{
	" from ", " into ", " set ", " table ", " values ", " where ", " join ", ";",
}

// sqlVerb classifies SQL: the first code line (skipping "--" comment
// lines) must open with a statement keyword, and the head must carry a
// secondary SQL signal. Returns the lowercase verb for the label.
func sqlVerb(head string) (string, bool) {
	lower := strings.ToLower(head)
	first := ""
	for _, ln := range strings.Split(lower, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "--") {
			continue
		}
		first = ln
		break
	}
	for _, kw := range sqlKeywords {
		if first == kw || strings.HasPrefix(first, kw+" ") {
			for _, sig := range sqlSignals {
				if strings.Contains(lower, sig) {
					return kw, true
				}
			}
			return "", false
		}
	}
	return "", false
}

// urlListCount classifies a paste whose every non-empty line is a
// bare http(s) URL, returning the URL count.
func urlListCount(content string) (int, bool) {
	n := 0
	for _, ln := range strings.Split(content, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if !strings.HasPrefix(ln, "http://") && !strings.HasPrefix(ln, "https://") {
			return 0, false
		}
		n++
	}
	return n, n > 0
}

// shellPrompts are the line prefixes shellCmdCount treats as shell
// prompts. "# " (root) and "> " (continuation) are deliberately
// excluded - they collide with markdown headings and blockquotes.
var shellPrompts = []string{"$ ", "% ", "❯ "}

// hasShellPromptPrefix reports whether one line opens with a prompt.
func hasShellPromptPrefix(ln string) bool {
	ln = strings.TrimLeft(ln, " \t")
	for _, p := range shellPrompts {
		if strings.HasPrefix(ln, p) {
			return true
		}
	}
	return false
}

// shellCmdCount classifies a copied shell session: the first non-empty
// line must be prompt-prefixed (sessions start at a prompt; output
// never does), and the count is the number of prompt lines (= commands
// run). Output lines between prompts are free.
func shellCmdCount(content string) (int, bool) {
	lines := strings.Split(content, "\n")
	first := ""
	for _, ln := range lines {
		if strings.TrimSpace(ln) != "" {
			first = ln
			break
		}
	}
	if !hasShellPromptPrefix(first) {
		return 0, false
	}
	n := 0
	for _, ln := range lines {
		if hasShellPromptPrefix(ln) {
			n++
		}
	}
	return n, true
}

// htmlTagRe matches an opening tag at the very start of the paste.
// "<?xml" and "<!--" deliberately don't match: XML and a leading
// comment are too ambiguous to call html.
var htmlTagRe = regexp.MustCompile(`^<[a-z][a-z0-9-]*(\s|>|/>)`)

// isHTML detects markup pastes: a doctype, an <html> root, or any
// opening tag as the first non-space byte.
func isHTML(head string) bool {
	t := strings.ToLower(strings.TrimSpace(head))
	return strings.HasPrefix(t, "<!doctype") ||
		strings.HasPrefix(t, "<html") ||
		htmlTagRe.MatchString(t)
}

// sizeNickname is the fallback label: compact char count + line count,
// e.g. "1.2k·86L". Mid-dot, not a dash, so it scans as one token in a
// chip.
func sizeNickname(content string) string {
	return compactCount(utf8.RuneCountInString(content)) + "·" +
		strconv.Itoa(pasteLineCount(content)) + "L"
}

// compactCount renders n as "812", "1.2k", or "3.4M" - one decimal in
// the scaled forms, with a trailing ".0" trimmed so round numbers stay
// short ("12k", not "12.0k").
func compactCount(n int) string {
	switch {
	case n < 1_000:
		return strconv.Itoa(n)
	case n < 1_000_000:
		return trimTrailingZero(float64(n)/1_000) + "k"
	default:
		return trimTrailingZero(float64(n)/1_000_000) + "M"
	}
}

// trimTrailingZero formats v with one decimal, dropping ".0".
func trimTrailingZero(v float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(v, 'f', 1, 64), ".0")
}
