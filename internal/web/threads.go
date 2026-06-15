package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/georgebuilds/carlos/internal/agent"
)

// groupOverlay stamps the web-owned group membership onto a summary. The
// grouping is roster metadata that spans backends, so it is applied at the
// handler layer, never inside a backend (which is ignorant of groups).
func (s *Server) groupOverlay(ctx context.Context, summaries []ThreadSummary) {
	if s.groups == nil {
		return
	}
	m, err := s.groups.MembershipMap(ctx)
	if err != nil {
		return
	}
	for i := range summaries {
		if g, ok := m[summaries[i].ID]; ok {
			g := g
			summaries[i].GroupID = &g
		}
	}
}

// handleListThreads: GET /api/threads. The backend owns the roster
// projection (conversations-only filter + per-thread attachment overlay);
// the handler adds the web-owned group-membership overlay.
func (s *Server) handleListThreads(w http.ResponseWriter, r *http.Request) {
	out, err := s.backend.ListThreads(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	s.groupOverlay(r.Context(), out)
	writeJSON(w, http.StatusOK, out)
}

// handleGetThread: GET /api/threads/{id}.
func (s *Server) handleGetThread(w http.ResponseWriter, r *http.Request) {
	summary, ok, err := s.backend.GetThread(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no such thread")
		return
	}
	one := []ThreadSummary{summary}
	s.groupOverlay(r.Context(), one)
	writeJSON(w, http.StatusOK, one[0])
}

// handleCreateThread: POST /api/threads. Mints + ensures a new thread.
func (s *Server) handleCreateThread(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	summary, err := s.backend.CreateThread(r.Context(), body.Title)
	if err != nil {
		s.writeBackendErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// handleDeleteThread: DELETE /api/threads/{id}. Hard-deletes the thread and
// its sub-agent lineage (events, artifacts, agent rows). Refuses with 409
// thread_live when another live process is driving it.
func (s *Server) handleDeleteThread(w http.ResponseWriter, r *http.Request) {
	n, err := s.backend.Delete(r.PathValue("id"))
	if err != nil {
		switch {
		case errors.Is(err, ErrUnsupported):
			writeErr(w, http.StatusNotImplemented, "unsupported", err.Error())
		case errors.Is(err, agent.ErrSessionLive):
			writeErr(w, http.StatusConflict, "thread_live", "thread is live; detach it first, then delete")
		case errors.Is(err, agent.ErrSessionNotFound):
			writeErr(w, http.StatusNotFound, "not_found", "no such thread")
		default:
			writeErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": n})
}

// handleEvents: GET /api/threads/{id}/events?from=<seq>&limit=<n>. Returns
// persisted wire events with seq > from. Read caps internally at 100k;
// limit slices the head of that window (D-A).
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	from := parseInt(r.URL.Query().Get("from"), 0)
	limit := parseInt(r.URL.Query().Get("limit"), 0)
	out, err := s.backend.ReadEvents(r.Context(), id, from, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	if limit > 0 && int64(len(out)) > limit {
		out = out[:limit]
	}
	writeJSON(w, http.StatusOK, out)
}

// handleChildren: GET /api/threads/{id}/children. Poll fallback for the
// SSE children kind (spec §9.1).
func (s *Server) handleChildren(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	kids := s.backend.Children(r.Context(), id)
	if kids == nil {
		kids = []ChildSnap{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"children": kids})
}

// handleAttach: POST /api/threads/{id}/attach.
func (s *Server) handleAttach(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.backend.Attach(r.Context(), id); err != nil {
		s.writeBackendErr(w, err)
		return
	}
	summary, ok, err := s.backend.GetThread(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no such thread")
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

// handleDetach: POST /api/threads/{id}/detach.
func (s *Server) handleDetach(w http.ResponseWriter, r *http.Request) {
	if err := s.backend.Detach(r.PathValue("id")); err != nil {
		s.writeBackendErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMessage: POST /api/threads/{id}/messages. Appends an
// EvtUserMessage and returns its seq; does NOT wait for the turn (§9.2).
func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Text == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "text is required")
		return
	}
	seq, err := s.backend.Send(r.Context(), id, body.Text)
	if err != nil {
		s.writeBackendErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"seq": seq})
}

// handleApproval: POST /api/threads/{id}/approvals/{rid}. Resolves a
// pending tool-approval request (spec §10).
func (s *Server) handleApproval(w http.ResponseWriter, r *http.Request) {
	id, rid := r.PathValue("id"), r.PathValue("rid")
	var body struct {
		Decision string `json:"decision"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "decision is required")
		return
	}
	switch body.Decision {
	case "deny", "allow", "allow_always":
	default:
		writeErr(w, http.StatusBadRequest, "bad_decision", "decision must be deny|allow|allow_always")
		return
	}
	if err := s.backend.Resolve(id, rid, body.Decision); err != nil {
		s.writeBackendErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMeta: GET /api/meta.
func (s *Server) handleMeta(w http.ResponseWriter, r *http.Request) {
	var m Meta
	if s.metaFn != nil {
		m = s.metaFn()
	}
	if m.BackendCaps == nil {
		m.BackendCaps = s.backend.Caps()
	}
	writeJSON(w, http.StatusOK, m)
}

// writeBackendErr maps backend sentinel errors to HTTP status codes.
func (s *Server) writeBackendErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrUnsupported):
		writeErr(w, http.StatusNotImplemented, "unsupported", err.Error())
	case errors.Is(err, ErrThreadOwned):
		writeErr(w, http.StatusConflict, "thread_owned", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "backend_error", err.Error())
	}
}

func parseInt(s string, def int64) int64 {
	if s == "" {
		return def
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return n
}
