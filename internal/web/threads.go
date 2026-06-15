package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/georgebuilds/carlos/internal/agent"
)

// roster builds the merged thread list the SPA sees. carlos backends
// contribute their full ListThreads; the Claude Code backend is scoped to
// the threads carlos web OWNS (web_cc_origin), resolved one-by-one via
// GetThread rather than the recency scan, so old on-disk sessions carlos
// never started stay out of the console (plan WA-1). A missing group store
// (read-only mode) falls back to the unscoped registry fan-out.
func (s *Server) roster(ctx context.Context) ([]ThreadSummary, error) {
	if s.groups == nil {
		return s.registry.ListThreads(ctx)
	}
	origin, err := s.groups.CCOriginSet(ctx)
	if err != nil {
		// Best-effort: an origin-set failure should not blank the roster.
		// Treat it as "no CC origins" so carlos threads still list.
		origin = map[string]bool{}
	}
	out := []ThreadSummary{}
	var errs []error
	for _, b := range s.registry.All() {
		if cc, ok := b.(*CCBackend); ok {
			out = append(out, s.ccOriginRoster(ctx, cc, origin)...)
			continue
		}
		ts, err := b.ListThreads(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", b.Name(), err))
			continue // a single backend's failure must not blank the roster
		}
		out = append(out, ts...)
	}
	// Preserve the registry's contract: surface the error only when the whole
	// roster came up empty AND a backend failed (a single-backend list failure
	// still 500s), so a partial roster is served rather than failed.
	if len(out) == 0 && len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

// ccOriginRoster resolves each web-owned CC id via GetThread (NOT the
// recency ListThreads), so the CC contribution is exactly carlos's own
// sessions. An id whose file has vanished is silently skipped.
func (s *Server) ccOriginRoster(ctx context.Context, cc *CCBackend, origin map[string]bool) []ThreadSummary {
	out := []ThreadSummary{}
	for id := range origin {
		summary, ok, err := cc.GetThread(ctx, id)
		if err != nil || !ok {
			continue
		}
		out = append(out, summary)
	}
	return out
}

// rosterIDs returns the set of thread ids currently in the roster, used to
// gate manual-group member counts on what the user actually sees (plan
// WB-3), so janitor-pruned threads still drop out of the counts.
func (s *Server) rosterIDs(ctx context.Context) map[string]bool {
	out := map[string]bool{}
	summaries, err := s.roster(ctx)
	if err != nil {
		return out
	}
	for _, t := range summaries {
		out[t.ID] = true
	}
	return out
}

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

// hiddenOverlay stamps the web-owned roster blacklist onto summaries. Like
// groups, it is handler-layer metadata that spans backends; the SPA folds
// hidden threads out of the default view (a "show hidden" toggle reveals
// them). Best-effort: a store error leaves everything visible.
func (s *Server) hiddenOverlay(ctx context.Context, summaries []ThreadSummary) {
	if s.groups == nil {
		return
	}
	hidden, err := s.groups.HiddenSet(ctx)
	if err != nil || len(hidden) == 0 {
		return
	}
	for i := range summaries {
		if hidden[summaries[i].ID] {
			summaries[i].Hidden = true
		}
	}
}

// repoOverlay stamps the web-owned per-thread repo onto each summary, exactly
// as groupOverlay stamps the group (plan WB-1). The repo is roster metadata
// that spans backends, written at create/import; a thread with no stored repo
// omits it. Best-effort: a store error leaves repos unset.
func (s *Server) repoOverlay(ctx context.Context, summaries []ThreadSummary) {
	if s.groups == nil {
		return
	}
	m, err := s.groups.RepoMap(ctx)
	if err != nil || len(m) == 0 {
		return
	}
	for i := range summaries {
		if r, ok := m[summaries[i].ID]; ok {
			r := r
			summaries[i].Repo = &r
		}
	}
}

// handleHideThread: POST/DELETE /api/threads/{id}/hide. Adds or removes the
// thread from the roster blacklist (web-local; no data is touched).
func (s *Server) handleHideThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if s.groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "no_store", "grouping store unavailable")
		return
	}
	var err error
	if r.Method == http.MethodDelete {
		err = s.groups.Unhide(r.Context(), id)
	} else {
		err = s.groups.Hide(r.Context(), id)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "hide_failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListThreads: GET /api/threads. The backend owns the roster
// projection (conversations-only filter + per-thread attachment overlay);
// the handler adds the web-owned group-membership overlay.
func (s *Server) handleListThreads(w http.ResponseWriter, r *http.Request) {
	out, err := s.roster(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	s.groupOverlay(r.Context(), out)
	s.hiddenOverlay(r.Context(), out)
	s.repoOverlay(r.Context(), out)
	writeJSON(w, http.StatusOK, out)
}

// handleGetThread: GET /api/threads/{id}.
func (s *Server) handleGetThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, ok := s.resolveBackend(w, id)
	if !ok {
		return
	}
	summary, ok, err := b.GetThread(r.Context(), id)
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
	s.repoOverlay(r.Context(), one)
	writeJSON(w, http.StatusOK, one[0])
}

// handleCreateThread: POST /api/threads. Mints + ensures a new thread.
func (s *Server) handleCreateThread(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title   string `json:"title"`
		Backend string `json:"backend"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	// Default to carlos; an explicit backend (from the "+ new" menu) routes
	// to that agent's create path (e.g. "cc" starts a Claude Code session).
	be := s.registry.Default()
	if body.Backend != "" {
		b, ok := s.registry.Backend(body.Backend)
		if !ok {
			writeErr(w, http.StatusNotFound, "unknown_backend", "no backend "+body.Backend)
			return
		}
		be = b
	}
	summary, err := be.CreateThread(r.Context(), body.Title)
	if err != nil {
		s.writeBackendErr(w, err)
		return
	}
	// A web-created Claude Code thread is carlos-owned: record it in the
	// origin set (so the roster lists it) and resolve+store its repo from the
	// create cwd, exactly as an import would (plan WA-1 / WR-0). Best-effort:
	// a store hiccup never fails the create itself.
	if cc, ok := be.(*CCBackend); ok && s.groups != nil {
		_ = s.groups.MarkCCOrigin(r.Context(), summary.ID)
		if root, name, ok := gitRepoRoot(cc.CreateCwd()); ok {
			_ = s.groups.SetThreadRepo(r.Context(), summary.ID, root, name)
		}
	}
	writeJSON(w, http.StatusOK, summary)
}

// ccBackend returns the registered Claude Code backend, or nil when none is
// wired (read-only mode, or a deployment without CC).
func (s *Server) ccBackend() *CCBackend {
	if b, ok := s.registry.Backend(ccBackendName); ok {
		if cc, ok := b.(*CCBackend); ok {
			return cc
		}
	}
	return nil
}

// handleImportableCC: GET /api/cc/importable. Lists the recent on-disk CC
// sessions (the recency scan, now the import window) that are NOT already
// web-owned, as import candidates for the "+ new -> open existing" menu
// (plan WA-2). Each candidate carries a repo overlay so the picker can show
// it. Returns {"sessions": [...]}; an empty list when no CC backend.
func (s *Server) handleImportableCC(w http.ResponseWriter, r *http.Request) {
	cc := s.ccBackend()
	if cc == nil || s.groups == nil {
		writeJSON(w, http.StatusOK, map[string]any{"sessions": []ThreadSummary{}})
		return
	}
	cands, err := cc.ImportCandidates(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	origin, err := s.groups.CCOriginSet(r.Context())
	if err != nil {
		origin = map[string]bool{}
	}
	out := []ThreadSummary{}
	for _, c := range cands {
		if origin[c.Summary.ID] {
			continue // already imported; not a candidate
		}
		out = append(out, c.Summary)
	}
	s.repoOverlay(r.Context(), out)
	writeJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// handleImportCC: POST /api/cc/import {id}. Marks an on-disk CC session
// web-owned (so it joins the roster) and resolves+stores its repo from the
// session's cwd (plan WA-2). Idempotent: re-importing returns 200. 404 when
// the id resolves to no on-disk session. Returns the imported ThreadSummary.
func (s *Server) handleImportCC(w http.ResponseWriter, r *http.Request) {
	cc := s.ccBackend()
	if cc == nil || s.groups == nil {
		writeErr(w, http.StatusServiceUnavailable, "no_cc", "claude code backend unavailable")
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ID == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "id is required")
		return
	}
	summary, ok, err := cc.GetThread(r.Context(), body.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "import_failed", err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "not_found", "no such cc session")
		return
	}
	if err := s.groups.MarkCCOrigin(r.Context(), body.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "import_failed", err.Error())
		return
	}
	// Resolve the repo from the session's recorded cwd. Best-effort: a
	// session outside any git tree simply lands in the "No repository" group.
	if cwd, ok := cc.sessionCwd(body.ID); ok {
		if root, name, ok := gitRepoRoot(cwd); ok {
			_ = s.groups.SetThreadRepo(r.Context(), body.ID, root, name)
		}
	}
	one := []ThreadSummary{summary}
	s.repoOverlay(r.Context(), one)
	writeJSON(w, http.StatusOK, one[0])
}

// handleDeleteThread: DELETE /api/threads/{id}. Hard-deletes the thread and
// its sub-agent lineage (events, artifacts, agent rows). Refuses with 409
// thread_live when another live process is driving it.
func (s *Server) handleDeleteThread(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, ok := s.resolveBackend(w, id)
	if !ok {
		return
	}
	n, err := b.Delete(id)
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
	b, ok := s.resolveBackend(w, id)
	if !ok {
		return
	}
	from := parseInt(r.URL.Query().Get("from"), 0)
	limit := parseInt(r.URL.Query().Get("limit"), 0)
	out, err := b.ReadEvents(r.Context(), id, from, 0)
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
	b, ok := s.resolveBackend(w, id)
	if !ok {
		return
	}
	kids := b.Children(r.Context(), id)
	if kids == nil {
		kids = []ChildSnap{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"children": kids})
}

// handleAttach: POST /api/threads/{id}/attach.
func (s *Server) handleAttach(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, ok := s.resolveBackend(w, id)
	if !ok {
		return
	}
	if err := b.Attach(r.Context(), id); err != nil {
		s.writeBackendErr(w, err)
		return
	}
	summary, ok, err := b.GetThread(r.Context(), id)
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
	id := r.PathValue("id")
	b, ok := s.resolveBackend(w, id)
	if !ok {
		return
	}
	if err := b.Detach(id); err != nil {
		s.writeBackendErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMessage: POST /api/threads/{id}/messages. Appends an
// EvtUserMessage and returns its seq; does NOT wait for the turn (§9.2).
func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	b, ok := s.resolveBackend(w, id)
	if !ok {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Text == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "text is required")
		return
	}
	seq, err := b.Send(r.Context(), id, body.Text)
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
	b, ok := s.resolveBackend(w, id)
	if !ok {
		return
	}
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
	if err := b.Resolve(id, rid, body.Decision); err != nil {
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
		m.BackendCaps = s.registry.Default().Caps()
	}
	if m.Agents == nil {
		for _, b := range s.registry.All() {
			m.Agents = append(m.Agents, AgentInfo{
				Name:      b.Name(),
				Display:   agentDisplay(b.Name()),
				CanCreate: b.Caps()["create"],
			})
		}
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
