package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/verove-jordan/astronomy/internal/store"
)

// This file manages saved selections: named/starred folder-sets from the Import "Processing
// history" (saved_selections table). The read path is embedded in GET /api/processed (the
// selections ride along the groups, annotated by the same existence machinery); these handlers
// only mutate. Mirrors the preset manager handler-for-handler.

// selectionBody is the create/update payload. Paths are required on save (the backend computes the
// signature from them — the single source of truth); Name is required on save, optional on update
// (an update may only toggle Favorite).
type selectionBody struct {
	Name     string   `json:"name"`
	Paths    []string `json:"paths"`
	Mode     string   `json:"mode"`
	Format   string   `json:"format"`
	Favorite *bool    `json:"favorite"`
}

// saveSelection names a folder-set (upsert by signature — re-naming the same set renames in
// place). Favorite, when sent, stars it in the same call. POST /api/selections
func (s *Server) saveSelection(w http.ResponseWriter, r *http.Request) {
	var b selectionBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		badRequest(w, "invalid body")
		return
	}
	name := strings.TrimSpace(b.Name)
	if name == "" {
		badRequest(w, "name is required")
		return
	}
	if len(b.Paths) == 0 {
		badRequest(w, "paths are required")
		return
	}
	pathsJSON, err := json.Marshal(b.Paths)
	if err != nil {
		badRequest(w, "invalid paths")
		return
	}
	id, err := s.store.SaveSelection(r.Context(), name, pathsJSON,
		store.SelectionSignature(b.Paths), b.Mode, b.Format, b.Favorite)
	if err != nil {
		if isUniqueViolation(err) {
			writeJSON(w, http.StatusConflict,
				map[string]string{"error": "a saved selection with that name already exists"})
			return
		}
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// updateSelection renames and/or stars a saved selection — send name, favorite, or both.
// PUT /api/selections/{id}
func (s *Server) updateSelection(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid id")
		return
	}
	var b selectionBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		badRequest(w, "invalid body")
		return
	}
	name := strings.TrimSpace(b.Name)
	if name == "" && b.Favorite == nil {
		badRequest(w, "nothing to update — send name and/or favorite")
		return
	}
	if name != "" {
		if err := s.store.RenameSelection(r.Context(), id, name); err != nil {
			if isUniqueViolation(err) {
				writeJSON(w, http.StatusConflict,
					map[string]string{"error": "a saved selection with that name already exists"})
				return
			}
			serverError(w, err)
			return
		}
	}
	if b.Favorite != nil {
		if err := s.store.SetSelectionFavorite(r.Context(), id, *b.Favorite); err != nil {
			serverError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// deleteSelection forgets a saved selection (its history row reverts to a plain derived entry).
// DELETE /api/selections/{id}
func (s *Server) deleteSelection(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid id")
		return
	}
	if err := s.store.DeleteSelection(r.Context(), id); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
