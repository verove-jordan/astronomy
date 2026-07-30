package guide

import (
	"fmt"
	"math"
)

// Gates a calibration must clear before it is allowed to steer anything.
const (
	// minCalibDriftPx is how far the star must move on an axis for the fit to mean anything. The
	// periodic-error calibration uses the same floor: below a few pixels the centroid noise is a large
	// fraction of the signal, and the resulting scale is confidently wrong.
	minCalibDriftPx = 5.0
	// minOrthogonality is the smallest |sin| of the angle between the fitted axes that is still
	// separable — about 14°. Two nearly parallel axes make the 2×2 inverse blow up, so a small offset
	// in one axis is read as an enormous offset in the other. That is what an axis that never actually
	// moved looks like, and it must be refused rather than amplified.
	minOrthogonality = 0.25
	// maxCosDecBoost caps the 1/cos(dec) rescale so a target near the pole cannot turn a modest pixel
	// error into an unbounded axis command. 5 corresponds to about 78° declination.
	maxCosDecBoost = 5.0
)

// CalibObservation is one step of a calibration run: the axis rotation that was commanded, and where
// the star ended up as a result.
//
// DX and DY are cumulative from that axis's starting position rather than per-step, because the fit
// wants the whole lever arm. Per-step deltas would each carry the full centroid noise on a much
// smaller signal.
type CalibObservation struct {
	Axis          AxisID
	CommandArcsec float64 // signed, axis arcseconds
	DX, DY        float64 // pixels, cumulative from this axis's first frame
}

// Calibration maps a star offset in pixels onto the two mount axes.
//
// It generalises the single-axis PECCalibration the periodic-error trainer already uses: same idea,
// same cos(dec)-absorbed scale, but with a declination axis as well, so a pixel offset can be split
// between the two instead of only projected onto RA.
type Calibration struct {
	// ArcsecPerPx is how much rotation of each axis one pixel of star motion represents. Like
	// PECCalibration.AxisArcsecPerPx it absorbs cos(dec) by construction, so no declination term
	// appears downstream — but see AtDec for what happens when the mount moves.
	RAArcsecPerPx  float64 `json:"ra_arcsec_per_px"`
	DecArcsecPerPx float64 `json:"dec_arcsec_per_px"`

	// The unit vectors point along the direction a POSITIVE rotation of that axis moves the star.
	RAUnitX  float64 `json:"ra_unit_x"`
	RAUnitY  float64 `json:"ra_unit_y"`
	DecUnitX float64 `json:"dec_unit_x"`
	DecUnitY float64 `json:"dec_unit_y"`

	// DecAtCalibDeg is the declination the run was calibrated at. RA rates scale as 1/cos(dec), so
	// without this a calibration done at the celestial equator under-corrects badly near the pole.
	DecAtCalibDeg float64 `json:"dec_at_calib_deg"`

	// Orthogonality is |sin| of the angle between the fitted axes: 1 is perpendicular, 0 parallel.
	// Kept because it is the single best summary of whether a calibration is trustworthy.
	Orthogonality float64 `json:"orthogonality"`

	// Drift distances are how far the star actually moved on each axis, kept so a user can see why a
	// calibration was imprecise rather than only being told that it was.
	RADriftPx  float64 `json:"ra_drift_px"`
	DecDriftPx float64 `json:"dec_drift_px"`

	// DecBacklashArcsec is how much of a declination reversal is swallowed before the axis moves. Used
	// to size the resist-switch guard, not to add a compensating over-push: over-pushing a backlash
	// estimate that is slightly too large produces an oscillation that is worse than the backlash.
	DecBacklashArcsec float64 `json:"dec_backlash_arcsec"`
}

// FitCalibration solves for the pixel→axis mapping from a set of commanded moves.
//
// Each axis is fitted independently by least squares through the origin: the star's displacement is
// proportional to the commanded rotation, and the constant of proportionality is a vector whose
// direction is the axis's direction on the sensor and whose length is pixels per arcsecond. Fitting
// several steps rather than differencing two frames is what makes the result robust to one bad
// centroid.
func FitCalibration(obs []CalibObservation, decDeg float64) (Calibration, error) {
	raVec, raDrift, raOK := fitAxis(obs, AxisRA)
	decVec, decDrift, decOK := fitAxis(obs, AxisDec)
	if !raOK || !decOK {
		return Calibration{}, fmt.Errorf("%w: need at least two moved samples on each axis", ErrCalibrationTooSmall)
	}
	if raDrift < minCalibDriftPx || decDrift < minCalibDriftPx {
		return Calibration{}, fmt.Errorf("%w: moved %.1f px in RA and %.1f px in Dec, need %.0f",
			ErrCalibrationTooSmall, raDrift, decDrift, minCalibDriftPx)
	}

	// Length is pixels per arcsecond, so the reciprocal is the arcsec-per-pixel scale.
	raLen := math.Hypot(raVec[0], raVec[1])
	decLen := math.Hypot(decVec[0], decVec[1])
	if raLen == 0 || decLen == 0 {
		return Calibration{}, ErrCalibrationDegenerate
	}
	c := Calibration{
		RAArcsecPerPx:  1 / raLen,
		DecArcsecPerPx: 1 / decLen,
		RAUnitX:        raVec[0] / raLen,
		RAUnitY:        raVec[1] / raLen,
		DecUnitX:       decVec[0] / decLen,
		DecUnitY:       decVec[1] / decLen,
		DecAtCalibDeg:  decDeg,
		RADriftPx:      raDrift,
		DecDriftPx:     decDrift,
	}
	// The cross product of two unit vectors is |sin| of the angle between them.
	c.Orthogonality = math.Abs(c.RAUnitX*c.DecUnitY - c.RAUnitY*c.DecUnitX)
	if c.Orthogonality < minOrthogonality {
		return Calibration{}, fmt.Errorf("%w: axes only %.1f° apart",
			ErrCalibrationDegenerate, math.Asin(c.Orthogonality)*180/math.Pi)
	}
	return c, nil
}

// fitAxis regresses the star displacement against the commanded rotation for one axis, through the
// origin. It returns pixels per arcsecond as a vector, and how far the star moved in total.
func fitAxis(obs []CalibObservation, axis AxisID) (vec [2]float64, driftPx float64, ok bool) {
	var sumCC, sumCX, sumCY float64
	var moved int
	for _, o := range obs {
		if o.Axis != axis || o.CommandArcsec == 0 {
			continue
		}
		sumCC += o.CommandArcsec * o.CommandArcsec
		sumCX += o.CommandArcsec * o.DX
		sumCY += o.CommandArcsec * o.DY
		moved++
		if d := math.Hypot(o.DX, o.DY); d > driftPx {
			driftPx = d
		}
	}
	if moved < 2 || sumCC == 0 {
		return vec, driftPx, false
	}
	return [2]float64{sumCX / sumCC, sumCY / sumCC}, driftPx, true
}

// AtDec re-scales the RA axis for a declination other than the one calibrated at.
//
// A given RA-axis rotation sweeps the sky by cos(dec) times as much, so the further from the
// calibration declination the mount points, the more axis rotation one pixel of error represents.
// Only RA is affected; the declination axis moves the sky one-for-one wherever it is pointed.
func (c Calibration) AtDec(decDeg float64) Calibration {
	cosCalib := math.Cos(c.DecAtCalibDeg * math.Pi / 180)
	cosNow := math.Cos(decDeg * math.Pi / 180)
	if cosCalib <= 0 || cosNow <= 0 {
		return c
	}
	boost := cosCalib / cosNow
	if boost > maxCosDecBoost {
		boost = maxCosDecBoost
	} else if boost < 1/maxCosDecBoost {
		boost = 1 / maxCosDecBoost
	}
	c.RAArcsecPerPx *= boost
	return c
}

// Axes splits a star offset in pixels into rotations of the two mount axes.
//
// This solves the 2×2 system rather than projecting onto each axis separately. Projection is only
// correct when the axes are exactly perpendicular, and they never are — non-perpendicularity leaks
// declination error into the RA channel, where the servo faithfully corrects for something that is
// not there.
func (c Calibration) Axes(dx, dy float64) (raArcsec, decArcsec float64, ok bool) {
	if c.RAArcsecPerPx == 0 || c.DecArcsecPerPx == 0 {
		return 0, 0, false
	}
	// Forward matrix: axis arcseconds → pixels.
	a := c.RAUnitX / c.RAArcsecPerPx
	b := c.DecUnitX / c.DecArcsecPerPx
	d := c.RAUnitY / c.RAArcsecPerPx
	e := c.DecUnitY / c.DecArcsecPerPx
	det := a*e - b*d
	if math.Abs(det) < 1e-12 {
		return 0, 0, false
	}
	return (e*dx - b*dy) / det, (a*dy - d*dx) / det, true
}

// Valid reports whether the calibration is usable.
func (c Calibration) Valid() bool {
	return c.RAArcsecPerPx > 0 && c.DecArcsecPerPx > 0 && c.Orthogonality >= minOrthogonality
}

// ScaleArcsecPerPx is the sky scale implied by the calibration, for display and for converting a
// dither in pixels. It is the declination axis's scale, which needs no cos(dec) undoing.
func (c Calibration) ScaleArcsecPerPx() float64 { return c.DecArcsecPerPx }
