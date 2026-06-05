//go:build !nochroma

// Package diff's syntax-highlighting layer. Build with `-tags nochroma`
// to compile this file out and use highlight_stub.go instead — useful
// for binary-size-sensitive builds (chroma + its transitive
// `github.com/dlclark/regexp2` add ~1 MiB to the static binary).
//
// Performance: chroma lexes the line each call and emits a small
// terminal-256 ANSI string. For typical 80-cell diff lines this is
// sub-millisecond on the M-series machines we develop on; for the
// approval pane (handful of files, hundreds of lines max) the cost is
// invisible. If you find yourself rendering megabytes of diff with
// Highlight=true, batch the work or memoize at the call site — this
// package intentionally does not cache.

package diff

import (
	"bytes"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// lexerCache memoizes chroma lexer lookups per language alias. chroma
// already maintains its own internal registry but Get does a linear
// scan of registered lexers each call; caching avoids that hot-path
// cost when we highlight many lines for the same file.
var (
	lexerCache sync.Map // map[string]chroma.Lexer
	formatter  = formatters.Get("terminal256")
	styleTheme = pickStyle()
)

// pickStyle selects a chroma style at init. We prefer "github" because
// its palette plays well with the diff's red/green tints; if it's
// unavailable in the installed chroma version we fall back to
// "monokai" and then to the no-op default.
func pickStyle() *chroma.Style {
	for _, name := range []string{"github", "monokai", "swapoff"} {
		if s := styles.Get(name); s != nil && s.Name != "" {
			return s
		}
	}
	return styles.Fallback
}

// highlightLine runs `line` through chroma's lexer for `lang` and
// returns terminal-256-colored output. On any error (unknown lexer,
// formatter failure) we return "" so the caller falls back to the
// uncolored line — highlighting must never break rendering.
//
// We strip trailing newlines from chroma's output because the
// formatter adds one and we're emitting per-line.
func highlightLine(lang, line string) string {
	if line == "" {
		return ""
	}
	lex := getLexer(lang)
	if lex == nil {
		return ""
	}
	it, err := lex.Tokenise(nil, line)
	if err != nil {
		return ""
	}
	var buf bytes.Buffer
	if err := formatter.Format(&buf, styleTheme, it); err != nil {
		return ""
	}
	return strings.TrimRight(buf.String(), "\n")
}

// getLexer returns a cached chroma.Lexer for `lang`, or nil if chroma
// doesn't know it. We don't call lexers.Analyse — we already mapped
// the file extension in detectLanguage; passing through chroma's
// fallback would only slow us down.
func getLexer(lang string) chroma.Lexer {
	if v, ok := lexerCache.Load(lang); ok {
		if lex, ok := v.(chroma.Lexer); ok {
			return lex
		}
		return nil
	}
	lex := lexers.Get(lang)
	// chroma returns the fallback lexer ("plaintext") when the alias
	// is unknown; that emits a single token with no color, which is
	// wasted work. Treat fallback as a miss.
	if lex == nil || lex.Config().Name == "fallback" {
		lexerCache.Store(lang, chroma.Lexer(nil))
		return nil
	}
	lexerCache.Store(lang, lex)
	return lex
}
