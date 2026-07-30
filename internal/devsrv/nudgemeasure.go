package devsrv

import (
	"context"
	"net/http"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/guidestar"
)

// Measuring what a nudge actually achieved.
//
// A dither is commanded in pixels and delivered in whatever the gears feel like. On a German equatorial
// the declination axis gives back only part of a small reversal — the simulator models this, and real
// AVXs are worse — so a planner that assumes its commands landed is planning around a fiction, and the
// low-discrepancy spread it went to the trouble of computing slowly stops being one.
//
// internal/dither has had Planner.Achieved since it was written, with a comment explaining exactly this.
// Nothing ever called it, because nothing could measure the answer: the sequencer hands frames straight
// to disk and never sees a pixel. The star tracker built for periodic-error training can, and it lives
// in this process alongside the camera, so the measurement costs two short exposures and no round trips.

// nudgeMeasureExposureSec is the default for the two framing shots. Short: this is measuring a
// displacement of a few pixels, not taking a picture.
const nudgeMeasureExposureSec = 1.0

// nudgeSettleSec is the pause after the move, before the second look. Long enough for the tube to stop
// ringing, short enough not to spend the night on it.
const nudgeSettleSec = 2.0

func (s *Server) nudgeMeasured(
	w http.ResponseWriter, r *http.Request,
	mount device.Mount, raArcsec, decArcsec, exposureSec float64,
) {
	if exposureSec <= 0 {
		exposureSec = nudgeMeasureExposureSec
	}
	cam := s.currentCamera()
	if cam == nil {
		deviceError(w, device.ErrNotConnected)
		return
	}
	// The measurement drives the camera itself.
	s.live.stop()

	before, err := s.starNow(r.Context(), cam, exposureSec, nil)
	if err != nil {
		// A dither that cannot be measured is still a dither. Fall back rather than losing the frame.
		s.nudgeUnmeasured(w, r, mount, raArcsec, decArcsec, "no star to measure against: "+err.Error())
		return
	}
	if err := mount.Nudge(r.Context(), raArcsec, decArcsec); err != nil {
		deviceError(w, err)
		return
	}
	sleepCtx(r.Context(), nudgeSettleSec)

	after, err := s.starNow(r.Context(), cam, exposureSec, &before)
	if err != nil {
		s.nudgeUnmeasured(w, r, mount, 0, 0, "the star was lost across the move: "+err.Error())
		return
	}

	st, _ := mount.State(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"mount":    st,
		"measured": true,
		// Pixels, in sensor coordinates — the units the dither planner thinks in.
		"achieved_dx_px": after.X - before.X,
		"achieved_dy_px": after.Y - before.Y,
	})
}

// nudgeUnmeasured performs the move without a measurement, explaining why.
func (s *Server) nudgeUnmeasured(
	w http.ResponseWriter, r *http.Request,
	mount device.Mount, raArcsec, decArcsec float64, reason string,
) {
	if raArcsec != 0 || decArcsec != 0 {
		if err := mount.Nudge(r.Context(), raArcsec, decArcsec); err != nil {
			deviceError(w, err)
			return
		}
	}
	st, _ := mount.State(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"mount": st, "measured": false, "reason": reason})
}

// starNow takes one exposure and finds a star: a fresh one when expect is nil, otherwise the same one
// again. Re-finding rather than re-picking is what stops a dither from being "measured" against a
// different star, which would report a move of tens of pixels that never happened.
func (s *Server) starNow(ctx context.Context, cam device.Camera, exposureSec float64, expect *guidestar.Star) (guidestar.Star, error) {
	if err := cam.SetControl(device.ControlExposure, int64(exposureSec*1e6), false); err != nil {
		return guidestar.Star{}, err
	}
	frame, err := s.live.exposeOnce(ctx, cam)
	if err != nil {
		return guidestar.Star{}, err
	}
	im, ok := guidestar.ImageFrom(frame.Pix, frame.Width, frame.Height)
	if !ok {
		return guidestar.Star{}, guidestar.ErrNoStar
	}
	if expect == nil {
		return guidestar.Pick(im, guidestar.Options{})
	}
	// A dither is a handful of pixels; searching much further would find a neighbour instead.
	return guidestar.Refind(im, expect.X, expect.Y, ditherSearchPx, guidestar.Options{})
}

// ditherSearchPx bounds the search for the star after a dither. Generous enough for the largest
// dither anyone sets plus the backlash that made it land short, tight enough to exclude neighbours.
const ditherSearchPx = 40

// sleepCtx waits, but gives up if the request is cancelled.
func sleepCtx(ctx context.Context, seconds float64) {
	select {
	case <-ctx.Done():
	case <-time.After(time.Duration(seconds * float64(time.Second))):
	}
}
