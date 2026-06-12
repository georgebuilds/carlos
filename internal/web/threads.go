package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/georgebuilds/carlos/internal/agent"
)

// summaryFromSession projects an agent.Session into the wire
// ThreadSummary, overlaying web-only state (attachment, frame, group).
func (s *Server) summaryFromSession(sess agent.Session, groupID *string) ThreadSummary {
	return ThreadSummary{
		ID:           sess.ID,
		Title:        sess.Title,
		Model:        sess.Model,
		State:        wireState(sess.State),
		Attached:     s.backend.Attached(sess.ID),
		CreatedAt:    rfc3339(sess.CreatedAt),
		UpdatedAt:    rfc3339(sess.UpdatedAt),
		Preview:      sess.Preview,
		UserMsgs:     sess.UserMsgs,
		Frame:        s.backend.Frame(sess.ID),
		Backend:      "carlos",
		GroupID:      groupID,
		Capabilities: s.backend.Caps(),
	}
}

// handleListThreads: GET /api/threads. Every top-level thread, with the
// group-membership overlay and per-thread attachment flag.
func (s *Server) handleListThreads(w http.ResponseWriter, r *http.Request) {
	sessions, err := agent.ListUserSessions(r.Context(), s.log, "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	members := map[string]string{}
	if s.groups != nil {
		if m, err := s.groups.MembershipMap(r.Context()); err == nil {
			members = m
		}
	}
	out := make([]ThreadSummary, 0, len(sessions))
	for _, sess := range sessions {
		// The roster is conversations only. ListUserSessions returns every
		// top-level (parent_id IS NULL) agent, which also catches research
		// roots, headless `please` runs, and sub-agent task roots: those
		// spawn with a self root_id and NO parent, so they slip past the
		// parent filter despite being programmatic activity, not threads
		// the user chatted with.
		//
		// Show a thread when EITHER it is a real conversation (>=1 user
		// message) OR it is a still-live blank one (non-terminal state and
		// no content yet). The second clause keeps a freshly created thread
		// the user has not typed into visible (create, switch away, switch
		// back), per the blank-stays-unless-app-closed rule. It hides:
		//   - content-bearing spawns (research roots, completed headless
		//     runs): zero user messages but real assistant/tool/research
		//     events;
		//   - terminal empties (orphaned/abandoned blank sessions, done
		//     task roots): the "app closed" case the user is fine losing.
		// UserMsgs>0 short-circuits, so the content query only runs for the
		// rare zero-message non-terminal thread.
		keep := sess.UserMsgs > 0 ||
			(!sess.State.IsTerminal() && !s.hasContentEvents(r.Context(), sess.ID))
		if !keep {
			continue
		}
		var gid *string
		if g, ok := members[sess.ID]; ok {
			g := g
			gid = &g
		}
		out = append(out, s.summaryFromSession(sess, gid))
	}
	writeJSON(w, http.StatusOK, out)
}

// hasContentEvents reports whether the thread has produced any assistant,
// tool, or research events: the signature of a programmatic run (research
// root, headless `please`, scheduled job) as opposed to a blank, freshly
// created conversation that carries only lifecycle bookkeeping. Used to
// keep blank conversations visible while hiding spawned roots. Best-effort:
// a query error returns false (show the thread) rather than hiding it.
func (s *Server) hasContentEvents(ctx context.Context, agentID string) bool {
	var exists int
	err := s.log.DB().QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM events
			 WHERE agent_id = ?
			   AND type IN ('assistant_message', 'tool_call', 'tool_result', 'research_phase')
			 LIMIT 1)`, agentID).Scan(&exists)
	if err != nil {
		return false
	}
	return exists == 1
}

// handleGetThread: GET /api/threads/{id}.
func (s *Server) handleGetThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sessions, err := agent.ListUserSessions(r.Context(), s.log, "")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	for _, sess := range sessions {
		if sess.ID != id {
			continue
		}
		var gid *string
		if s.groups != nil {
			if m, err := s.groups.MembershipMap(r.Context()); err == nil {
				if g, ok := m[id]; ok {
					gid = &g
				}
			}
		}
		writeJSON(w, http.StatusOK, s.summaryFromSession(sess, gid))
		return
	}
	writeErr(w, http.StatusNotFound, "not_found", "no such thread")
}

// handleCreateThread: POST /api/threads. Mints + ensures a new thread.
func (s *Server) handleCreateThread(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title string `json:"title"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	sess, err := s.backend.CreateThread(r.Context(), body.Title)
	if err != nil {
		s.writeBackendErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, s.summaryFromSession(sess, nil))
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
	events, err := s.log.Read(r.Context(), id, from)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read_failed", err.Error())
		return
	}
	out := make([]WireEvent, 0, len(events))
	for _, ev := range events {
		if we, ok := eventToWire(ev); ok {
			out = append(out, we)
		}
		if limit > 0 && int64(len(out)) >= limit {
			break
		}
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
	sessions, _ := agent.ListUserSessions(r.Context(), s.log, "")
	for _, sess := range sessions {
		if sess.ID == id {
			writeJSON(w, http.StatusOK, s.summaryFromSession(sess, nil))
			return
		}
	}
	writeErr(w, http.StatusNotFound, "not_found", "no such thread")
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
