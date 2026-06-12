package web

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/georgebuilds/carlos/internal/agent"
)

// Meta is the GET /api/meta payload: enough environment context for the
// SPA to label the session and gate features (spec §9.1).
type Meta struct {
	Version     string          `json:"version"`
	Frames      []string        `json:"frames"`
	ActiveFrame string          `json:"active_frame"`
	Model       string          `json:"model"`
	Provider    string          `json:"provider"`
	BackendCaps map[string]bool `json:"backend_caps"`
}

// Options configures a Server. Token and Log are required; the rest have
// sane zero-value behavior (a read-only backend, an empty meta).
type Options struct {
	// Log is the shared event log (~/.carlos/state.db).
	Log *agent.SQLiteEventLog
	// Groups is the thread-grouping store over the same DB.
	Groups *GroupStore
	// Backend drives interactive operations. Nil => read-only.
	Backend Backend
	// Token is the per-launch bearer token (D9). Required.
	Token string
	// BoundAddr is the host:port the server is bound to, used for the
	// Host/Origin DNS-rebinding check. Empty disables the check (tests).
	BoundAddr string
	// MetaFn supplies the /api/meta payload. Nil => minimal meta.
	MetaFn func() Meta
}

// Server is the HTTP + SSE surface. Construct with NewServer and mount
// Handler() on an http.Server bound to 127.0.0.1.
type Server struct {
	log     *agent.SQLiteEventLog
	groups  *GroupStore
	backend Backend
	token   string
	bound   string
	metaFn  func() Meta
	hub     *ephemeralHub
	mux     *http.ServeMux
}

// NewServer wires the routes. The returned Server's Handler() is the
// auth-wrapped mux.
func NewServer(opts Options) *Server {
	b := opts.Backend
	if b == nil {
		b = readOnlyBackend{}
	}
	s := &Server{
		log:     opts.Log,
		groups:  opts.Groups,
		backend: b,
		token:   opts.Token,
		bound:   opts.BoundAddr,
		metaFn:  opts.MetaFn,
		hub:     newEphemeralHub(),
	}
	s.routes()
	return s
}

// Hub exposes the ephemeral fan-out so the interactive backend (W-2/W-3)
// can publish deltas, approvals, and children snapshots that the SSE
// handlers stream.
func (s *Server) Hub() *ephemeralHub { return s.hub }

// SetBackend swaps the interactive backend after construction. Needed
// because the runtime-backed backend depends on the server's hub, which
// only exists once NewServer has run (chicken-and-egg). A nil argument is
// ignored so the read-only default stays in place.
func (s *Server) SetBackend(b Backend) {
	if b != nil {
		s.backend = b
	}
}

func (s *Server) routes() {
	m := http.NewServeMux()
	// Read paths (work in read-only mode).
	m.HandleFunc("GET /api/threads", s.handleListThreads)
	m.HandleFunc("POST /api/threads", s.handleCreateThread)
	m.HandleFunc("GET /api/threads/{id}", s.handleGetThread)
	m.HandleFunc("DELETE /api/threads/{id}", s.handleDeleteThread)
	m.HandleFunc("GET /api/threads/{id}/events", s.handleEvents)
	m.HandleFunc("GET /api/threads/{id}/stream", s.handleStream)
	m.HandleFunc("GET /api/threads/{id}/children", s.handleChildren)
	// Interactive paths (501 in read-only mode).
	m.HandleFunc("POST /api/threads/{id}/attach", s.handleAttach)
	m.HandleFunc("POST /api/threads/{id}/detach", s.handleDetach)
	m.HandleFunc("POST /api/threads/{id}/messages", s.handleMessage)
	m.HandleFunc("POST /api/threads/{id}/approvals/{rid}", s.handleApproval)
	// Groups (web-owned).
	m.HandleFunc("GET /api/groups", s.handleListGroups)
	m.HandleFunc("POST /api/groups", s.handleCreateGroup)
	m.HandleFunc("PATCH /api/groups/{id}", s.handlePatchGroup)
	m.HandleFunc("DELETE /api/groups/{id}", s.handleDeleteGroup)
	m.HandleFunc("PUT /api/threads/{id}/group", s.handleSetThreadGroup)
	// Meta.
	m.HandleFunc("GET /api/meta", s.handleMeta)
	s.mux = m
}

// Handler returns the auth-wrapped mux. The SPA (embedded static assets)
// is mounted by the caller alongside this under "/".
func (s *Server) Handler() http.Handler {
	return s.authMiddleware(s.mux)
}

// authMiddleware enforces the bearer token (D9) and the Host/Origin
// DNS-rebinding defense. SSE requests authenticate via the ?token= query
// param because EventSource cannot set headers; everything else uses the
// Authorization header. Constant-time comparison throughout.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.originOK(r) {
			writeErr(w, http.StatusForbidden, "bad_origin", "origin/host not allowed")
			return
		}
		if !s.tokenOK(r) {
			writeErr(w, http.StatusUnauthorized, "unauthorized", "missing or invalid token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) tokenOK(r *http.Request) bool {
	got := ""
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		got = strings.TrimPrefix(h, "Bearer ")
	} else if q := r.URL.Query().Get("token"); q != "" {
		got = q
	}
	if got == "" || s.token == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

// originOK rejects requests whose Origin or Host points somewhere other
// than the bound localhost address. The token is the real gate; this is
// belt-and-braces against a malicious page issuing requests through the
// user's browser (D9). An empty BoundAddr disables the check (tests).
func (s *Server) originOK(r *http.Request) bool {
	if s.bound == "" {
		return true
	}
	// Origin, when present, must match the bound address. Browsers omit
	// Origin on same-origin GETs, so absence is allowed.
	if o := r.Header.Get("Origin"); o != "" {
		if !strings.HasSuffix(o, "://"+s.bound) &&
			!strings.HasSuffix(o, "://localhost"+portOf(s.bound)) {
			return false
		}
	}
	// Host must be the bound address or the localhost alias.
	host := r.Host
	if host != s.bound && host != "localhost"+portOf(s.bound) {
		return false
	}
	return true
}

// portOf returns ":<port>" from a host:port, or "" if none.
func portOf(hostport string) string {
	if i := strings.LastIndex(hostport, ":"); i >= 0 {
		return hostport[i:]
	}
	return ""
}

// --- JSON helpers --------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": msg},
	})
}
