package api

import (
	"encoding/json"
	"net/http"

	"github.com/verove-jordan/astronomy/internal/filters"
)

// What is physically in each filter wheel slot.
//
// The wheel reports slot NUMBERS and nothing else — it has no idea a piece of glass is Ha. That
// mapping lives here because it is a property of the observatory, not of the session: it survives
// restarts, and every frame captured writes it into the FILTER header. Per-filter flats, channel
// detection and the whole stacking path key off that header, so getting it wrong is not cosmetic —
// it mislabels every sub for the night.
//
// Stored in app_settings rather than a table of its own: it is one short list, changed on the rare
// occasions a filter is swapped.
const filterSlotsKey = "capture.filter_slots"

// filterSlots returns the saved slot → filter mapping. GET /api/capture/filters
func (s *Server) filterSlots(w http.ResponseWriter, r *http.Request) {
	raw, ok, err := s.store.Setting(r.Context(), filterSlotsKey)
	if err != nil {
		serverError(w, err)
		return
	}
	names := []string{}
	if ok && raw != "" {
		if err := json.Unmarshal([]byte(raw), &names); err != nil {
			// A corrupt setting must not break the capture page; an empty list is recoverable by
			// re-entering the names, a 500 here is not.
			names = []string{}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"names": names})
}

// saveFilterSlots records the mapping. POST /api/capture/filters
func (s *Server) saveFilterSlots(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Names []string `json:"names"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		badRequest(w, "invalid body")
		return
	}
	// Canonicalize what we recognise ("s2"/"sulfur"/" SII " all become "SII") and keep anything else
	// verbatim, so a custom filter still works. This matters beyond tidiness: the sequencer resolves a
	// step's filter NAME against these slot names, ingest re-reads the same token out of the FILTER
	// header, and "S2" would otherwise collide with inspect's "S<n>" unnamed-slot placeholder.
	//
	// Empty slots stay empty strings: slot 4 being blank is meaningful (nothing fitted), and dropping
	// it would silently renumber every filter after it.
	names := make([]string, 0, len(body.Names))
	for _, n := range body.Names {
		names = append(names, filters.Normalize(n))
	}
	if len(names) > 16 {
		badRequest(w, "a filter wheel has at most 16 slots")
		return
	}

	encoded, err := json.Marshal(names)
	if err != nil {
		serverError(w, err)
		return
	}
	if err := s.store.SetSetting(r.Context(), filterSlotsKey, string(encoded)); err != nil {
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"names": names})
}
