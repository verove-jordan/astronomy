package api

import (
	"encoding/json"
	"net/http"

	"github.com/verove-jordan/astronomy/internal/capture"
)

// The calibration wizard's server side.
//
// The plan is built here rather than in the browser because the matching rules are the substance of
// the feature — a dark that does not match its lights is worse than no dark at all — and they belong
// next to the sequencer that will shoot them, not duplicated in TypeScript.

// calibrationPlan turns light settings into an ordered calibration sequence.
// POST /api/capture/calibration/plan
func (s *Server) calibrationPlan(w http.ResponseWriter, r *http.Request) {
	var req capture.CalibrationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "invalid body")
		return
	}
	plan, err := capture.BuildCalibrationPlan(req)
	if err != nil {
		// A refusal here is guidance, not a fault: "measure the flat exposure first" is the wizard
		// working correctly.
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, plan)
}
