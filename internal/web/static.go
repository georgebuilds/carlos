package web

import (
	cryptorand "crypto/rand"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"strings"
)

// distFS embeds the built SPA. A placeholder index.html ships in the tree
// so the binary always compiles; `cd web && npm run build` overwrites
// dist with the real Vue bundle (plan §5, D8). The `all:` prefix includes
// files Vite may emit with a leading underscore or dot.
//
//go:embed all:dist
var distFS embed.FS

// SPA returns an http.Handler that serves the embedded SPA. Unknown paths
// fall back to index.html so client-side routing works (single-page app).
// It is mounted at "/" WITHOUT the bearer gate: the bundle is non-secret
// and bootstraps by reading the token from the URL fragment, which never
// reaches the server (D9). The /api surface keeps the token gate.
func SPA() http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Should never happen (dist is embedded); serve a 500 stub.
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "embedded SPA unavailable", http.StatusInternalServerError)
		})
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Conservative security headers for the document/bundle. CSP has
		// no remote sources because everything is embedded.
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// Serve the asset if it exists; otherwise hand back index.html so
		// a deep link (e.g. /thread/01J...) loads the app shell.
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			serveIndex(w, r, sub)
			return
		}
		if _, err := fs.Stat(sub, p); err != nil {
			serveIndex(w, r, sub)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
	b, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "index.html missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

// NewToken mints a 256-bit per-launch bearer token (D9). Hex-encoded so it
// is URL-fragment safe.
func NewToken() (string, error) {
	var b [32]byte
	if _, err := cryptorand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
