package telegram

import "strings"

// MarkdownV2 — Telegram's stricter markdown flavor — requires every
// reserved character to be backslash-escaped, even inside what would
// otherwise be a benign literal. The Bot API rejects the whole message
// if a stray reserved char appears unescaped, which is a footgun the
// moment a user-supplied title contains, say, a period.
//
// We escape conservatively: every byte in markdownV2ReservedSet gets a
// preceding backslash. We do NOT try to preserve formatting (no "escape
// outside of bold/italic spans") because the agent's outbound text is
// not authored with MarkdownV2 syntax in mind — Title and Body are
// treated as plaintext and rendered as such.
//
// Reserved set per the Bot API docs:
//
//	_ * [ ] ( ) ~ ` > # + - = | { } . !  and backslash itself
//
// Callers that want to inject *actual* MarkdownV2 formatting (e.g.
// bolding a section header) should build the formatting markers
// themselves and escape only the user-supplied substrings.
var markdownV2ReservedSet = map[byte]struct{}{
	'_':  {},
	'*':  {},
	'[':  {},
	']':  {},
	'(':  {},
	')':  {},
	'~':  {},
	'`':  {},
	'>':  {},
	'#':  {},
	'+':  {},
	'-':  {},
	'=':  {},
	'|':  {},
	'{':  {},
	'}':  {},
	'.':  {},
	'!':  {},
	'\\': {},
}

// EscapeMarkdownV2 prepends a backslash before every MarkdownV2 reserved
// byte in s. Operates on bytes, not runes, because every reserved char
// is single-byte ASCII; multi-byte sequences (emoji, accented chars)
// pass through untouched because they cannot contain any reserved byte
// (UTF-8 continuation bytes have the high bit set).
func EscapeMarkdownV2(s string) string {
	// First pass: count how much we'll grow. Avoids a reallocation in
	// the common case where the text contains a few reserved chars.
	extra := 0
	for i := 0; i < len(s); i++ {
		if _, ok := markdownV2ReservedSet[s[i]]; ok {
			extra++
		}
	}
	if extra == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + extra)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if _, ok := markdownV2ReservedSet[c]; ok {
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	return b.String()
}
