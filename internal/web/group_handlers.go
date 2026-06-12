package web

import (
	"encoding/json"
	"errors"
	"net/http"
)

// handleListGroups: GET /api/groups.
func (s *Server) handleListGroups(w http.ResponseWriter, r *http.Request) {
	if s.groups == nil {
		writeJSON(w, http.StatusOK, []Group{})
		return
	}
	gs, err := s.groups.List(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, gs)
}

// handleCreateGroup: POST /api/groups {name}.
func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request) {
	if s.groups == nil {
		writeErr(w, http.StatusNotImplemented, "no_groups", "group store not wired")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Name == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", "name is required")
		return
	}
	g, err := s.groups.Create(r.Context(), body.Name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "create_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// handlePatchGroup: PATCH /api/groups/{id} {name?, pos?}.
func (s *Server) handlePatchGroup(w http.ResponseWriter, r *http.Request) {
	if s.groups == nil {
		writeErr(w, http.StatusNotImplemented, "no_groups", "group store not wired")
		return
	}
	var body struct {
		Name *string `json:"name"`
		Pos  *int    `json:"pos"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	g, err := s.groups.Patch(r.Context(), r.PathValue("id"), body.Name, body.Pos)
	if err != nil {
		s.writeGroupErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// handleDeleteGroup: DELETE /api/groups/{id}. Members revert to ungrouped.
func (s *Server) handleDeleteGroup(w http.ResponseWriter, r *http.Request) {
	if s.groups == nil {
		writeErr(w, http.StatusNotImplemented, "no_groups", "group store not wired")
		return
	}
	if err := s.groups.Delete(r.Context(), r.PathValue("id")); err != nil {
		s.writeGroupErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSetThreadGroup: PUT /api/threads/{id}/group {group_id|null}.
func (s *Server) handleSetThreadGroup(w http.ResponseWriter, r *http.Request) {
	if s.groups == nil {
		writeErr(w, http.StatusNotImplemented, "no_groups", "group store not wired")
		return
	}
	var body struct {
		GroupID *string `json:"group_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad_request", "invalid body")
		return
	}
	if err := s.groups.SetThreadGroup(r.Context(), r.PathValue("id"), body.GroupID); err != nil {
		s.writeGroupErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writeGroupErr(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrGroupNotFound) {
		writeErr(w, http.StatusNotFound, "not_found", err.Error())
		return
	}
	writeErr(w, http.StatusInternalServerError, "group_error", err.Error())
}
