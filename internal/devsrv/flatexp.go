package devsrv

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
)

// Finding the right flat exposure, by measuring rather than guessing.
//
// A flat must land in the sensor's linear range: bright enough that its own shot noise is
// negligible, dim enough that no pixel clips. The convention is about half full well — for a 16-bit
// ADU scale, a median near 32000. That exposure cannot be predicted, because it depends on the light
// panel's brightness, the filter's transmission (an Ha flat needs perhaps 30× an L flat) and the
// f-ratio. So: take a frame, look at it, and scale.
//
// The search is a ratio step rather than a bisection because the sensor is LINEAR — doubling the
// exposure doubles the signal — so one measurement predicts the answer directly. Two or three
// iterations converge from any starting point, and the loop exists only to absorb non-linearity
// near saturation and any panel warm-up.

// FlatTargetADU is half of a 16-bit full scale: bright, comfortably unclipped.
const FlatTargetADU = 32000

// FlatExposureRequest asks for a measurement.
type FlatExposureRequest struct {
	Filter string `json:"filter"`
	Slot   int    `json:"slot"`
	Gain   int64  `json:"gain"`
	Offset int64  `json:"offset"`
	Bin    int    `json:"bin"`
	// StartUs seeds the search; 0 → 100 ms, a sensible guess for a panel through luminance.
	StartUs int64 `json:"start_us"`
	// TargetADU overrides the half-well default, for a sensor with a different full scale.
	TargetADU float64 `json:"target_adu"`
	MaxTries  int     `json:"max_tries"`
}

// FlatExposureAttempt records one measurement, so the UI can show the convergence.
type FlatExposureAttempt struct {
	ExposureUs int64   `json:"exposure_us"`
	MedianADU  float64 `json:"median_adu"`
	Clipped    bool    `json:"clipped"`
}

// FlatExposureResult is the answer.
type FlatExposureResult struct {
	ExposureUs int64                 `json:"exposure_us"`
	MedianADU  float64               `json:"median_adu"`
	Converged  bool                  `json:"converged"`
	Attempts   []FlatExposureAttempt `json:"attempts"`
	Message    string                `json:"message,omitempty"`
}

// measureFlatExposure ramps the exposure until the frame's median sits near the target.
func (s *Server) measureFlatExposure(ctx context.Context, req FlatExposureRequest) (FlatExposureResult, error) {
	cam := s.currentCamera()
	if cam == nil {
		return FlatExposureResult{}, device.ErrNotConnected
	}
	target := req.TargetADU
	if target <= 0 {
		target = FlatTargetADU
	}
	exposure := req.StartUs
	if exposure <= 0 {
		exposure = 100_000 // 100 ms
	}
	tries := req.MaxTries
	if tries <= 0 {
		tries = 5
	}

	if req.Slot > 0 {
		s.mu.Lock()
		wheel := s.wheel
		s.mu.Unlock()
		if wheel != nil {
			if err := wheel.SetPosition(req.Slot); err != nil {
				return FlatExposureResult{}, err
			}
			if err := wheel.WaitSettled(ctx); err != nil {
				return FlatExposureResult{}, err
			}
		}
	}
	if req.Gain > 0 {
		if err := cam.SetControl(device.ControlGain, req.Gain, false); err != nil {
			return FlatExposureResult{}, err
		}
	}
	if req.Offset > 0 {
		_ = cam.SetControl(device.ControlOffset, req.Offset, false)
	}

	caps := cam.Caps()
	var res FlatExposureResult
	for i := 0; i < tries; i++ {
		exposure = clampExposure(exposure, caps)
		median, clipped, err := s.medianOfOneFrame(ctx, cam, exposure)
		if err != nil {
			return res, err
		}
		res.Attempts = append(res.Attempts, FlatExposureAttempt{
			ExposureUs: exposure, MedianADU: median, Clipped: clipped,
		})
		res.ExposureUs, res.MedianADU = exposure, median

		// Within 10 % of target is close enough: the master flat is normalised anyway, so the exact
		// level does not matter — only that it is linear and unclipped.
		if !clipped && math.Abs(median-target)/target < 0.10 {
			res.Converged = true
			return res, nil
		}

		next, err := nextFlatExposure(exposure, median, target, clipped)
		if err != nil {
			res.Message = err.Error()
			return res, nil
		}
		if next == exposure {
			// Already at a limit and still wrong — stop rather than loop.
			res.Message = "the exposure is at the camera's limit; adjust the light panel instead"
			return res, nil
		}
		exposure = next
	}
	res.Message = "did not settle within the allowed tries — check that the panel brightness is steady"
	// Fall back to the closest usable attempt rather than whatever the loop happened to end on: an
	// exposure that was merely 20 % off is far more useful than the last probe, which may be the one
	// that overshot.
	if best, ok := bestAttempt(res.Attempts, target); ok {
		res.ExposureUs, res.MedianADU = best.ExposureUs, best.MedianADU
	}
	return res, nil
}

// bestAttempt picks the unclipped measurement closest to the target.
func bestAttempt(attempts []FlatExposureAttempt, target float64) (FlatExposureAttempt, bool) {
	var best FlatExposureAttempt
	found := false
	for _, a := range attempts {
		if a.Clipped || a.MedianADU <= 0 {
			continue
		}
		if !found || math.Abs(a.MedianADU-target) < math.Abs(best.MedianADU-target) {
			best, found = a, true
		}
	}
	return best, found
}

// nextFlatExposure scales the exposure toward the target, exploiting the sensor's linearity.
func nextFlatExposure(current int64, median, target float64, clipped bool) (int64, error) {
	if clipped {
		// A clipped frame carries no information about HOW over-exposed it is — the median is a
		// floor, not a measurement — so back off by a fixed factor rather than computing a ratio
		// from a number that is known to be wrong.
		next := current / 4
		if next < 1 {
			// Already at the floor and still clipping: no exposure can fix this, only the panel.
			return 0, fmt.Errorf("the panel is too bright even at the shortest exposure — dim it")
		}
		return next, nil
	}
	if median <= 1 {
		// Essentially no signal: either the panel is off or the cap is still on. Multiplying by the
		// ratio here would ask for an absurd exposure, so step up boldly but boundedly instead.
		return current * 8, nil
	}
	ratio := target / median
	// Bound the jump: a single wild measurement (a passing shadow) should not send the next
	// exposure somewhere that takes minutes to read back.
	ratio = math.Max(0.1, math.Min(10, ratio))
	next := int64(float64(current) * ratio)
	if next <= 0 {
		return 0, fmt.Errorf("the panel is too bright even at the shortest exposure — dim it")
	}
	return next, nil
}

// isFlatClipped decides whether a flat is over-exposed.
//
// Counting saturated pixels alone is NOT a safe test: every sensor has hot and stuck pixels sitting
// at full scale in every single frame, so a small count is normal and says nothing about exposure.
// Judging by that count rejects perfectly good flats — which shows up as a search that oscillates
// around the right answer and never accepts it.
//
// The median cannot be shifted by isolated defects, so it is the honest measure of whether the frame
// as a whole has clipped. The saturation fraction is kept only as a second test at a level no defect
// population could reach, to catch a vignetted flat whose bright centre clips while its dim corners
// keep the median down.
func isFlatClipped(median, saturatedPct float64) bool {
	const fullScale = 65535.0
	return median >= 0.95*fullScale || saturatedPct > 10
}

// clampExposure keeps a request inside what the camera reports it can do.
func clampExposure(us int64, caps device.CameraCaps) int64 {
	if caps.MinExposureUs > 0 && us < caps.MinExposureUs {
		return caps.MinExposureUs
	}
	if caps.MaxExposureUs > 0 && us > caps.MaxExposureUs {
		return caps.MaxExposureUs
	}
	return us
}

// medianOfOneFrame exposes once and measures. The median rather than the mean because a few hot
// pixels or a dust mote must not shift the reading.
func (s *Server) medianOfOneFrame(ctx context.Context, cam device.Camera, exposureUs int64) (median float64, clipped bool, err error) {
	if err := cam.SetControl(device.ControlExposure, exposureUs, false); err != nil {
		return 0, false, err
	}
	if err := cam.StartExposure(ctx, false); err != nil {
		return 0, false, err
	}
	frame, err := waitForFrame(ctx, cam, exposureUs)
	if err != nil {
		return 0, false, err
	}
	st := measureFrame(frame)
	if st == nil {
		return 0, false, fmt.Errorf("the frame could not be measured")
	}
	return float64(st.Median), isFlatClipped(float64(st.Median), st.SaturatedPct), nil
}

// waitForFrame polls one still exposure to completion and downloads it.
func waitForFrame(ctx context.Context, cam device.Camera, exposureUs int64) (*device.Frame, error) {
	deadline := time.Now().Add(time.Duration(exposureUs)*time.Microsecond + 60*time.Second)
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		st, err := cam.ExposureState()
		if err != nil {
			return nil, err
		}
		switch st {
		case device.ExposureSuccess:
			return cam.Download(ctx)
		case device.ExposureFailed:
			return nil, errFailedExposure
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("the exposure did not complete in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// flatExposure measures and returns the recommended flat exposure. POST /flat-exposure
func (s *Server) flatExposure(w http.ResponseWriter, r *http.Request) {
	var req FlatExposureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	s.live.stop() // the measurement drives the camera itself
	res, err := s.measureFlatExposure(r.Context(), req)
	if err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}
