package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/verove-jordan/astronomy/internal/store"
)

// Equipment setups (named telescope + camera rigs) live server-side so the same rig is available on
// the desktop where a mosaic is planned AND on the phone at the telescope where it is executed — the
// browser-local version silently gave the two devices different optics, which moves every tile.

// equipmentJSON is the wire shape. It exists because store.EquipmentSetup carries the eyepieces
// JSONB as []byte, which encoding/json would base64-encode — the same projection mosaicplans.go
// does for its JSONB columns.
type equipmentJSON struct {
	ID         int64           `json:"id"`
	Name       string          `json:"name"`
	FocalMM    float64         `json:"focal_mm"`
	ApertureMM float64         `json:"aperture_mm"`
	PixelUm    float64         `json:"pixel_um"`
	SensorWpx  int             `json:"sensor_w_px"`
	SensorHpx  int             `json:"sensor_h_px"`
	BarlowX    float64         `json:"barlow_x"`
	CameraName string          `json:"camera_name"`
	Eyepieces  json.RawMessage `json:"eyepieces"`
	Favorite   bool            `json:"favorite"`
	CreatedAt  int64           `json:"created_at"`
	UpdatedAt  int64           `json:"updated_at"`
}

func equipmentJSONOf(e store.EquipmentSetup) equipmentJSON {
	eyepieces := json.RawMessage(e.Eyepieces)
	if len(eyepieces) == 0 {
		eyepieces = json.RawMessage("[]")
	}
	return equipmentJSON{
		ID: e.ID, Name: e.Name, FocalMM: e.FocalMM, ApertureMM: e.ApertureMM,
		PixelUm: e.PixelUm, SensorWpx: e.SensorWpx, SensorHpx: e.SensorHpx,
		BarlowX: e.BarlowX, CameraName: e.CameraName, Eyepieces: eyepieces,
		Favorite: e.Favorite, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
	}
}

// equipmentBody is the create/update payload. Optics fields are pointers so an update can leave a
// value untouched rather than resetting it to zero.
type equipmentBody struct {
	Name       string          `json:"name"`
	FocalMM    *float64        `json:"focal_mm"`
	ApertureMM *float64        `json:"aperture_mm"`
	PixelUm    *float64        `json:"pixel_um"`
	SensorWpx  *int            `json:"sensor_w_px"`
	SensorHpx  *int            `json:"sensor_h_px"`
	BarlowX    *float64        `json:"barlow_x"`
	CameraName *string         `json:"camera_name"`
	Eyepieces  json.RawMessage `json:"eyepieces"`
	Favorite   *bool           `json:"favorite"`
}

// listEquipment returns every saved rig. GET /api/equipment
func (s *Server) listEquipment(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListEquipmentSetups(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}
	out := make([]equipmentJSON, 0, len(rows))
	for _, row := range rows {
		out = append(out, equipmentJSONOf(row))
	}
	writeJSON(w, http.StatusOK, map[string]any{"setups": out})
}

// saveEquipment upserts a rig by name (re-saving the same name overwrites, matching the old
// browser-side behaviour). POST /api/equipment
func (s *Server) saveEquipment(w http.ResponseWriter, r *http.Request) {
	var b equipmentBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		badRequest(w, "invalid body")
		return
	}
	name := strings.TrimSpace(b.Name)
	if name == "" {
		badRequest(w, "name is required")
		return
	}
	setup := store.EquipmentSetup{Name: name}
	applyEquipmentBody(&setup, b)
	id, err := s.store.SaveEquipmentSetup(r.Context(), setup)
	if err != nil {
		serverError(w, err)
		return
	}
	row, err := s.store.GetEquipmentSetup(r.Context(), id)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"setup": equipmentJSONOf(row)})
}

// updateEquipment edits one rig in place — including a rename, which 409s on collision.
// PUT /api/equipment/{id}
func (s *Server) updateEquipment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid id")
		return
	}
	var b equipmentBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		badRequest(w, "invalid body")
		return
	}
	setup, err := s.store.GetEquipmentSetup(r.Context(), id)
	if err != nil {
		badRequest(w, "unknown setup")
		return
	}
	if name := strings.TrimSpace(b.Name); name != "" {
		setup.Name = name
	}
	applyEquipmentBody(&setup, b)
	if err := s.store.UpdateEquipmentSetup(r.Context(), setup); err != nil {
		if isUniqueViolation(err) {
			writeJSON(w, http.StatusConflict,
				map[string]string{"error": "a setup with that name already exists"})
			return
		}
		serverError(w, err)
		return
	}
	row, err := s.store.GetEquipmentSetup(r.Context(), id)
	if err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"setup": equipmentJSONOf(row)})
}

// deleteEquipment forgets a rig. DELETE /api/equipment/{id}
func (s *Server) deleteEquipment(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		badRequest(w, "invalid id")
		return
	}
	if err := s.store.DeleteEquipmentSetup(r.Context(), id); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// applyEquipmentBody copies the fields the caller actually sent onto setup, leaving the rest as they
// were (create starts from a zero setup, so absent fields simply stay zero).
func applyEquipmentBody(setup *store.EquipmentSetup, b equipmentBody) {
	if b.FocalMM != nil {
		setup.FocalMM = *b.FocalMM
	}
	if b.ApertureMM != nil {
		setup.ApertureMM = *b.ApertureMM
	}
	if b.PixelUm != nil {
		setup.PixelUm = *b.PixelUm
	}
	if b.SensorWpx != nil {
		setup.SensorWpx = *b.SensorWpx
	}
	if b.SensorHpx != nil {
		setup.SensorHpx = *b.SensorHpx
	}
	if b.BarlowX != nil {
		setup.BarlowX = *b.BarlowX
	}
	if b.CameraName != nil {
		setup.CameraName = *b.CameraName
	}
	if len(b.Eyepieces) > 0 {
		setup.Eyepieces = b.Eyepieces
	}
	if b.Favorite != nil {
		setup.Favorite = *b.Favorite
	}
}
