// Guide pulses: the endpoint an autoguider drives the mount through.
//
// It is separate from /mount/nudge on purpose. Nudge is the dither primitive — one fixed guide speed,
// both axes, and its arguments are tangent-plane offsets. A guide correction is finer, is expressed in
// AXIS arcseconds so the cos(dec) factor lives in exactly one place (the guider's calibration), and
// wants the rate the mount is actually configured for rather than a constant chosen for dithering.
package devsrv

import (
	"net/http"

	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/guide"
)

// guideMount returns the connected mount's guiding capability, or nil when there is none. Guiding is an
// optional interface for the same reason PEC is: a mount that cannot do it should make the absence
// visible rather than fail obscurely somewhere deeper.
func (s *Server) guideMount() device.GuideMount {
	mount := s.currentMount()
	if mount == nil {
		return nil
	}
	gm, ok := mount.(device.GuideMount)
	if !ok {
		return nil
	}
	return gm
}

// guideStatus reports whether guiding is available and at what rate. A guide session reads this once at
// the start and then passes the rate back with every pulse, so the 9600-baud link is not asked for the
// same byte hundreds of times a night.
//
// GET /mount/guide
func (s *Server) guideStatus(w http.ResponseWriter, r *http.Request) {
	gm := s.guideMount()
	if gm == nil {
		writeJSON(w, http.StatusOK, map[string]any{"supported": false})
		return
	}
	fraction, err := gm.GuideRate(r.Context())
	if err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"supported":           true,
		"rate_fraction":       fraction,
		"rate_arcsec_per_sec": guide.GuideRateArcsecPerSec(fraction),
	})
}

// guidePulse issues one correction on each axis.
//
// POST /mount/guide
func (s *Server) guidePulse(w http.ResponseWriter, r *http.Request) {
	var body struct {
		// RAArcsec and DecArcsec are the AXIS rotations to apply, signed. Zero on an axis means leave it
		// alone, which is the normal case whenever the servo's deadband or direction guard withheld one.
		RAArcsec  float64 `json:"ra_arcsec"`
		DecArcsec float64 `json:"dec_arcsec"`
		// RateArcsecPerSec is the speed to deliver them at; 0 falls back to half sidereal rather than
		// reading the mount, so a pulse never costs an extra round trip.
		RateArcsecPerSec float64 `json:"rate_arcsec_per_sec"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	gm := s.guideMount()
	if gm == nil {
		deviceError(w, device.ErrUnsupported)
		return
	}
	rate := body.RateArcsecPerSec
	if rate <= 0 {
		rate = guide.GuideRateArcsecPerSec(0)
	}

	applied := make(map[string]any, 2)
	for _, c := range []struct {
		axis   device.GuideAxis
		arcsec float64
	}{
		{device.GuideAxisRA, body.RAArcsec},
		{device.GuideAxisDec, body.DecArcsec},
	} {
		pulseRate, d, ok := guide.PulseFor(c.arcsec, rate)
		if !ok {
			continue
		}
		if err := gm.PulseGuide(r.Context(), c.axis, pulseRate, d); err != nil {
			deviceError(w, err)
			return
		}
		applied[c.axis.String()] = map[string]any{
			"arcsec":         c.arcsec,
			"arcsec_per_sec": pulseRate,
			"duration_ms":    d.Milliseconds(),
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"applied": applied})
}

// guideSetRate configures the mount's own autoguide rate.
//
// POST /mount/guide-rate
func (s *Server) guideSetRate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Fraction float64 `json:"fraction"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	gm := s.guideMount()
	if gm == nil {
		deviceError(w, device.ErrUnsupported)
		return
	}
	if err := gm.SetGuideRate(r.Context(), body.Fraction); err != nil {
		deviceError(w, err)
		return
	}
	// Read it back rather than echoing the request: the driver clamps and quantises to what the wire can
	// carry, and the caller should see what the mount will really do.
	fraction, err := gm.GuideRate(r.Context())
	if err != nil {
		deviceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rate_fraction":       fraction,
		"rate_arcsec_per_sec": guide.GuideRateArcsecPerSec(fraction),
	})
}
