package solar

import (
	"fmt"
	"math"
	"sort"
)

// regmodel.go regularises the two registration terms that are measured per frame but are not free to
// vary per frame.
//
// Translation is genuinely per frame — the disc really does wander across the sensor — and it is
// taken as measured. Scale and rotation are not: within one clip the optics do not move and the Sun
// does not change size, so their per-frame scatter is measurement error, and applying it warps every
// frame differently. Both errors displace a feature in proportion to its RADIUS, which is why they
// produce a stack that is crisp at disc centre and soft towards the limb — the signature that sent us
// looking for a focus problem that was not there. See scaleModel and ModelRotations below.
//
// Rotation is the one registration term the limb cannot supply — a circle is rotation-invariant — so
// it is correlated off disc structure, and that makes it the one term that can be badly wrong without
// anything noticing. The correlation is over a mid-disc annulus, and the Sun's annulus profile is not
// a single sharp feature but a handful of plage patches, so the correlation surface has more than one
// maximum. Near the reference the right peak wins comfortably. A couple of degrees away the wrong one
// starts winning, and it wins silently: the answer is inside the search bound, so nothing rejects it.
//
// Measured on a real two-clip session, with the reference frame in the second clip:
//
//	clip holding the reference   median -0.078°   scatter 0.070°   =  1.1 px at a 900 px limb
//	the other clip               median -2.557°   scatter 0.708°   = 11.2 px, with frames at -6.4°
//
// Ten times the scatter, and outliers at three times the true value. The two clips genuinely sat
// 2.5° apart — 39 px at the limb — because the phone was re-seated between them, and that is normal.
//
// The fix is to stop treating the estimates as independent. Rotation is a physical quantity that
// varies SMOOTHLY in time: field rotation on an alt-az mount is about a third of a degree per minute,
// so within one clip of twenty seconds the true value moves by hundredths of a degree. Between clips
// it can step by anything, because the camera was touched. So the model is a robust line per SOURCE:
// the line absorbs the drift, the per-source intercept absorbs the step, and a robust fit ignores the
// frames where the correlator picked the wrong peak.

const (
	// rotModelMinFrames is the fewest frames a source needs before a line is fitted to it. Below this
	// the median alone is the honest model — two or three points can define a slope that is entirely
	// noise, and a wrong slope extrapolates into a worse error than the scatter it replaced.
	rotModelMinFrames = 8
	// rotModelMaxResidDeg is the residual scatter, in degrees, above which the model is reported as
	// untrustworthy rather than quietly used.
	rotModelMaxResidDeg = 0.5
)

// StabiliseScale replaces each frame's fitted radius with a robust constant per source, returning
// the adjusted frames and an account of what it found.
//
// A CONSTANT, not a trend, and that is the physics rather than a simplification: the plate scale is
// set by the optics and the distance, both fixed for the length of a clip, and the Sun's angular
// diameter varies by under two percent over a YEAR. So the fitted radius measures one number, and
// every departure from it is error — from seeing softening the edge, from a cloud moving the
// threshold the limb is found at, from the disc drifting onto a different part of the field.
//
// Leaving those departures in is expensive because the stack does not treat the radius as a
// measurement, it treats it as the frame's scale: `Scale = canonical.R / frame.R`. A radius wrong by
// δ therefore stretches the whole frame by δ/R, displacing a feature at radius r by r·δ/R — nothing
// at the centre, everything at the limb. Measured on a real clip the fitted radius drifted from
// 904.0 to 908.6 px across seventeen seconds, which is 4.9 px of smear at 0.9 R, and the finished
// image lost sixty percent of its fine contrast between disc centre and limb while a single frame
// showed none of that fall-off.
//
// A source whose radius genuinely moves — someone refocused, or zoomed mid-clip — is reported rather
// than silently flattened, because that is a capture problem the operator can fix and this can only
// paper over.
func StabiliseScale(frames []Frame) ([]Frame, []string) {
	if len(frames) == 0 {
		return frames, nil
	}
	bySource := map[string][]int{}
	for i, f := range frames {
		bySource[f.Source] = append(bySource[f.Source], i)
	}
	names := make([]string, 0, len(bySource))
	for k := range bySource {
		names = append(names, k)
	}
	sort.Strings(names)

	out := append([]Frame(nil), frames...)
	var notes []string
	for _, name := range names {
		idx := bySource[name]
		radii := make([]float64, 0, len(idx))
		for _, i := range idx {
			if frames[i].Limb.R > 0 {
				radii = append(radii, frames[i].Limb.R)
			}
		}
		if len(radii) < scaleModelMinFrames {
			continue // too few to tell a constant from its scatter; leave them as measured
		}
		fixed := median(radii)
		dev := make([]float64, len(radii))
		for i, r := range radii {
			dev[i] = math.Abs(r - fixed)
		}
		scatter := 1.4826 * median(dev)
		for _, i := range idx {
			if out[i].Limb.R > 0 {
				out[i].Limb.R = fixed
			}
		}
		if fixed > 0 && scatter/fixed > scaleModelWarnFrac {
			notes = append(notes, fmt.Sprintf(
				"%s: the fitted disc radius scattered by %.1f px (%.2f%%) about %.0f px — held at the median, "+
					"but that much movement usually means the focus or the zoom shifted during the clip",
				shortSource(name), scatter, 100*scatter/fixed, fixed))
		}
	}
	return out, notes
}

const (
	// scaleModelMinFrames is the fewest frames a source needs before its radius is pinned. Below it a
	// median is barely better than any single measurement.
	scaleModelMinFrames = 8
	// scaleModelWarnFrac is the radius scatter, as a fraction of the radius, above which the operator
	// is told. 0.2% is about two pixels on a 900 px disc — already 1.6 px of smear at the limb.
	scaleModelWarnFrac = 0.002
)

// ModelRotations replaces per-frame rotation estimates with a robust model in time, fitted per
// source. raw[i] is the estimate for frames[i]; ok[i] says whether it was solved at all.
//
// The returned slice always has a value for every frame, including the ones the correlator refused:
// a frame with no estimate is not a frame with no rotation, and leaving it at zero would stack it
// several degrees out of true.
func ModelRotations(frames []Frame, raw []float64, ok []bool) ([]float64, []string) {
	out := make([]float64, len(frames))
	if len(raw) != len(frames) || len(ok) != len(frames) {
		copy(out, raw)
		return out, []string{"rotation model: mismatched inputs; per-frame estimates used as-is"}
	}
	var notes []string
	bySource := map[string][]int{}
	for i, f := range frames {
		bySource[f.Source] = append(bySource[f.Source], i)
	}
	names := make([]string, 0, len(bySource))
	for k := range bySource {
		names = append(names, k)
	}
	sort.Strings(names)

	for _, name := range names {
		idx := bySource[name]
		var t, deg []float64
		for _, i := range idx {
			if ok[i] {
				t = append(t, float64(frames[i].TimeMs))
				deg = append(deg, raw[i])
			}
		}
		if len(deg) == 0 {
			notes = append(notes, fmt.Sprintf("rotation model: %s — no frame's rotation could be solved; left unrotated", shortSource(name)))
			continue
		}
		slope, intercept := robustLine(t, deg)
		resid := make([]float64, len(deg))
		for i := range deg {
			resid[i] = math.Abs(deg[i] - (intercept + slope*t[i]))
		}
		scatter := 1.4826 * median(resid)
		for _, i := range idx {
			out[i] = intercept + slope*float64(frames[i].TimeMs)
		}
		if scatter > rotModelMaxResidDeg {
			notes = append(notes, fmt.Sprintf(
				"rotation model: %s — the per-frame estimates scatter by %.2f° about the fit; the disc may be too featureless to correlate",
				shortSource(name), scatter))
		}
	}
	return out, notes
}

// robustLine fits deg = intercept + slope*t by Theil–Sen: the median of the pairwise slopes, then
// the median offset from it.
//
// Least squares cannot be used here. The thing being removed is a minority of estimates that are
// whole degrees wrong, and least squares is steered by exactly those — one frame at -6.4° among a
// hundred at -2.5° drags the line far enough to matter. Theil–Sen ignores them by construction: a
// wrong point corrupts the slopes it takes part in and none of the others, so it never reaches the
// median. Pairs are subsampled because the exact estimator is quadratic and a clip is hundreds of
// frames; a few thousand pairs pin a median slope perfectly well.
func robustLine(t, y []float64) (slope, intercept float64) {
	n := len(t)
	if n == 0 {
		return 0, 0
	}
	if n < rotModelMinFrames {
		return 0, median(y) // too few to trust a slope; a robust constant is the honest model
	}
	step := 1
	if pairs := n * (n - 1) / 2; pairs > 4000 {
		step = int(math.Sqrt(float64(pairs)/4000)) + 1
	}
	var slopes []float64
	for i := 0; i < n; i += step {
		for j := i + 1; j < n; j += step {
			if dt := t[j] - t[i]; math.Abs(dt) > 1e-9 {
				slopes = append(slopes, (y[j]-y[i])/dt)
			}
		}
	}
	if len(slopes) == 0 {
		return 0, median(y)
	}
	slope = median(slopes)
	off := make([]float64, n)
	for i := range t {
		off[i] = y[i] - slope*t[i]
	}
	return slope, median(off)
}

// shortSource trims a source path to something a progress line can carry.
func shortSource(s string) string {
	if i := lastSep(s); i >= 0 {
		return s[i+1:]
	}
	return s
}

func lastSep(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' || s[i] == '\\' {
			return i
		}
	}
	return -1
}
