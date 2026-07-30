package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/pipeline"
	"github.com/verove-jordan/astronomy/internal/preset"
)

// This file is the processing-preset manager: the built-in "best params per situation" catalog
// (internal/preset, served read-only) merged with the user's saved presets (processing_presets table).
// A preset is a partial /api/jobs body the UI re-applies to the launch form; on save its Params are
// validated through the SAME pipeline.ApplyParamPatch a real run uses, so a saved preset can never carry
// a knob the engine would silently drop.

// presetBody is the create/update payload. Payload is the recipe (only present on save); Name is
// required on save, optional on update (an update may only toggle Favorite).
type presetBody struct {
	Name     string          `json:"name"`
	Payload  json.RawMessage `json:"payload"`
	Favorite *bool           `json:"favorite"`
}

// listPresets returns the built-in catalog followed by the user's saved presets. GET /api/presets
func (s *Server) listPresets(w http.ResponseWriter, r *http.Request) {
	items := preset.Builtins()
	rows, err := s.store.ListPresets(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	for _, row := range rows {
		items = append(items, preset.Item{
			ID:        row.ID,
			Name:      row.Name,
			Builtin:   false,
			Favorite:  row.Favorite,
			Payload:   json.RawMessage(row.Payload),
			CreatedAt: row.CreatedAt,
			UpdatedAt: row.UpdatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"presets": items})
}

// savePreset upserts a user preset by name (re-saving the same name overwrites). POST /api/presets
func (s *Server) savePreset(w http.ResponseWriter, r *http.Request) {
	var b presetBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		badRequest(w, "invalid body")
		return
	}
	name := strings.TrimSpace(b.Name)
	if name == "" {
		badRequest(w, "name is required")
		return
	}
	if err := validatePresetPayload(b.Payload); err != nil {
		badRequest(w, err.Error())
		return
	}
	id, err := s.store.SavePreset(r.Context(), name, b.Payload)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

// updatePreset renames and/or stars a saved preset — send name, favorite, or both. PUT /api/presets/{id}
func (s *Server) updatePreset(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid id")
		return
	}
	var b presetBody
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
		if err := s.store.RenamePreset(r.Context(), id, name); err != nil {
			if isUniqueViolation(err) {
				writeJSON(w, http.StatusConflict,
					map[string]string{"error": "a preset with that name already exists"})
				return
			}
			serverError(w, err)
			return
		}
	}
	if b.Favorite != nil {
		if err := s.store.SetPresetFavorite(r.Context(), id, *b.Favorite); err != nil {
			serverError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// deletePreset removes a saved preset. DELETE /api/presets/{id}
func (s *Server) deletePreset(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid id")
		return
	}
	if err := s.store.DeletePreset(r.Context(), id); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// validatePresetPayload runs the same checks the enqueue path does: the mode must parse, and any Params
// must apply cleanly with no unknown knobs — so a saved preset is guaranteed to reproduce on a real run.
func validatePresetPayload(raw json.RawMessage) error {
	if len(raw) == 0 {
		return fmt.Errorf("payload is required")
	}
	var p preset.Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fmt.Errorf("invalid payload: %w", err)
	}
	m, err := mode.ParseMode(p.Mode)
	if err != nil {
		return fmt.Errorf("invalid mode %q", p.Mode)
	}
	if len(p.Params) == 0 {
		return nil
	}
	scratch := mode.For(m)
	res, err := pipeline.ApplyParamPatch(&scratch, p.Params)
	if err != nil {
		return fmt.Errorf("invalid params: %w", err)
	}
	if len(res.Ignored) > 0 {
		return fmt.Errorf("unknown knobs for mode %s: %s", m, strings.Join(res.Ignored, ", "))
	}
	return nil
}

// isUniqueViolation reports whether err is a Postgres unique-constraint violation (SQLSTATE 23505) — a
// duplicate preset name, which the rename handler maps to 409.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
