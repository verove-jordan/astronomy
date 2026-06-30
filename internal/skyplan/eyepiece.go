package skyplan

import "math"

// Eyepiece is one ocular in the visual-observing kit: focal length (mm), apparent field of view (deg)
// and a short display label.
type Eyepiece struct {
	FocalMM float64
	AFOVDeg float64
	Label   string
}

// EyepieceView is an eyepiece evaluated against a scope: its magnification, true field of view (deg)
// and exit pupil (mm). Mag = scopeFocal/epFocal; TrueFOV = AFOV/Mag; ExitPupil = aperture/Mag.
type EyepieceView struct {
	Eyepiece
	MagX        float64
	TrueFOVDeg  float64
	ExitPupilMM float64
}

// Eyepiece selection tunables.
const (
	exitPupilMin      = 0.5 // mm — below this magnification is empty and the image too dim
	exitPupilMax      = 7.0 // mm — above this the dark-adapted eye clips the cone and the sky is too bright
	exitPupilIdeal    = 2.0 // mm — comfortable all-round deep-sky exit pupil (used when the size is unknown)
	visualFramingFrac = 0.5 // an object spanning half the true field frames comfortably
)

// View evaluates eyepiece e against the optics (including any Barlow, via the effective focal length).
// It returns the zero view (Mag 0) for an unusable eyepiece or scope focal length so callers can
// filter it out.
func (o Optics) View(e Eyepiece) EyepieceView {
	f := o.EffectiveFocalMM()
	if e.FocalMM <= 0 || f <= 0 {
		return EyepieceView{Eyepiece: e}
	}
	mag := f / e.FocalMM
	return EyepieceView{
		Eyepiece:    e,
		MagX:        mag,
		TrueFOVDeg:  e.AFOVDeg / mag,
		ExitPupilMM: o.ApertureMM / mag,
	}
}

// TrueFOVMinArcmin returns the eyepiece true field in arcminutes — the visual framing constraint, the
// eyepiece analogue of Optics.FOVMinArcmin.
func (v EyepieceView) TrueFOVMinArcmin() float64 { return v.TrueFOVDeg * 60 }

// chooseEyepiece picks the best eyepiece from the kit for an object of the given apparent diameter,
// returning false when no recommendation is possible (empty kit or unusable scope focal length).
//
// With a known diameter it minimizes a ratio-symmetric framing cost so "too big" and "too small" are
// penalized equally, the minimum landing where the object fills about half the true field; ties go to
// the larger exit pupil (a brighter image helps faint deep-sky objects). With an unknown diameter it
// cannot frame, so it falls back to the eyepiece whose exit pupil is closest to a comfortable medium.
// Eyepieces outside the usable exit-pupil window are excluded unless that would leave nothing, in
// which case the whole kit is considered (the UI flags the out-of-range pupil).
func chooseEyepiece(o Optics, kit []Eyepiece, diamArcmin float64, hasDiameter bool) (EyepieceView, bool) {
	if len(kit) == 0 || o.FocalMM <= 0 {
		return EyepieceView{}, false
	}
	views := make([]EyepieceView, 0, len(kit))
	for _, e := range kit {
		if v := o.View(e); v.MagX > 0 {
			views = append(views, v)
		}
	}
	if len(views) == 0 {
		return EyepieceView{}, false
	}

	usable := make([]EyepieceView, 0, len(views))
	for _, v := range views {
		if v.ExitPupilMM >= exitPupilMin && v.ExitPupilMM <= exitPupilMax {
			usable = append(usable, v)
		}
	}
	if len(usable) == 0 {
		usable = views // degenerate kit: recommend the nearest anyway; the UI flags the exit pupil
	}

	if hasDiameter && diamArcmin > 0 {
		return bestFramedEyepiece(usable, diamArcmin), true
	}
	return nearestExitPupil(usable, exitPupilIdeal), true
}

// bestFramedEyepiece returns the eyepiece whose true field best frames an object of diamArcmin,
// minimizing the framing cost; equal-cost ties prefer the larger exit pupil (brighter image).
func bestFramedEyepiece(views []EyepieceView, diamArcmin float64) EyepieceView {
	best := views[0]
	bestCost := framingCost(best, diamArcmin)
	for _, v := range views[1:] {
		c := framingCost(v, diamArcmin)
		if c < bestCost-1e-9 || (math.Abs(c-bestCost) <= 1e-9 && v.ExitPupilMM > best.ExitPupilMM) {
			best, bestCost = v, c
		}
	}
	return best
}

// framingCost is |log2((diam/trueField) / visualFramingFrac)|: zero at the ideal fill, growing
// symmetrically (in log space) for objects too large or too small for the field.
func framingCost(v EyepieceView, diamArcmin float64) float64 {
	fov := v.TrueFOVMinArcmin()
	if fov <= 0 {
		return math.Inf(1)
	}
	return math.Abs(math.Log2((diamArcmin / fov) / visualFramingFrac))
}

// nearestExitPupil returns the view whose exit pupil is closest to target.
func nearestExitPupil(views []EyepieceView, target float64) EyepieceView {
	best := views[0]
	for _, v := range views[1:] {
		if math.Abs(v.ExitPupilMM-target) < math.Abs(best.ExitPupilMM-target) {
			best = v
		}
	}
	return best
}
