package solar

import (
	"fmt"
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// autotune.go resolves the finish settings that cannot honestly be constants.
//
// Deconvolution width is the clearest case. It is the one parameter in this whole pipeline that has
// a true value rather than a tasteful one — it is the width of the point spread function — and the
// measurement is available for free on every solar frame. Guessing it is not a small error either:
// a kernel wider than the truth over-corrects, inventing texture at scales the optics never
// delivered, while the band that really is blurred goes uncorrected. That combination is the classic
// "noisy and soft at the same time" result, and no amount of adjusting the sharpening gains fixes
// it, because the damage is done a stage earlier.
//
// The same measurement answers a second question. A phone hands over pixels its own pipeline has
// already sharpened, and a sharpener leaves a signature an optical edge cannot produce: a bright
// shelf just inside the limb. Deconvolving on top of that double-counts. Rather than keeping a list
// of camera models — which would be wrong the moment the phone, the app or the firmware changes —
// the run measures the overshoot and backs off in proportion to what it finds.

const (
	// psfIterOvershootFull is the limb overshoot at which the iteration count is cut as far as it
	// goes. Measured on real iPhone ProRes clips, a few percent is typical.
	psfIterOvershootFull = 0.06
	// psfIterCutMax is the largest fraction of the iteration count a detected pre-sharpen may remove.
	// It never reaches zero: the camera's sharpening is a local contrast trick, not a deconvolution,
	// so it does not undo the actual blur and some correction is still owed.
	psfIterCutMax = 0.70
	// deconvSigmaMax bounds the resolved width. It is psfSigmaMax on purpose: the measurement already
	// refuses anything wider as a fit that has locked onto something other than the limb, so a second,
	// tighter ceiling here can only ever discard a width the measurement was willing to stand behind.
	// It was 4.0, and a real capture measured 4.33 and was quietly clamped — deliberately
	// under-deconvolved at exactly the plate scale where the deconvolution matters most.
	deconvSigmaMax = psfSigmaMax
)

// ResolveFinish fills in the finish settings that depend on what the capture actually resolved,
// returning the resolved options, the measurement they came from, and an account of what changed.
//
// It is called by the run and by every re-finish, rather than folded into Finish itself, so that
// Finish stays a pure function of its options — the Refine panel and the supervised auto-tuner both
// depend on being able to replay a render exactly from the knobs they were given.
func ResolveFinish(master *fits.Image, l Limb, o FinishOptions) (FinishOptions, PSF, []string) {
	if !o.DeconvAuto {
		return o, PSF{}, nil
	}
	psf := MeasurePSF(master, l)
	if !psf.OK {
		return o, psf, []string{
			"sun: the limb was too soft or too broken to measure the point spread function; " +
				"deconvolution stays at its default width"}
	}
	var notes []string
	before := o.DeconvSigma
	o.DeconvSigma = clampF(psf.SigmaPx, 0, deconvSigmaMax)
	notes = append(notes, fmt.Sprintf(
		"sun: measured resolution FWHM %.1f\" (PSF sigma %.2f px) — deconvolving at that width, not the %.2f px default",
		psf.FWHMArcsec, o.DeconvSigma, before))

	if psf.Sharpened() {
		cut := math.Min(psf.Overshoot/psfIterOvershootFull, 1) * psfIterCutMax
		was := o.DeconvIters
		o.DeconvIters = int(math.Round(float64(was) * (1 - cut)))
		if o.DeconvIters < 1 {
			o.DeconvIters = 1
		}
		notes = append(notes, fmt.Sprintf(
			"sun: the camera sharpened these frames before we saw them (+%.1f%% overshoot at the limb) — "+
				"deconvolution cut from %d to %d iterations so the two do not compound",
			100*psf.Overshoot, was, o.DeconvIters))
	}

	// The returned options own their gains. Nothing here rewrites them today, but the caller's preset
	// is reused for every window of the time-lapse and every candidate the supervisor renders, so a
	// resolved copy must never be able to reach back into the settings the next render starts from.
	o.Sharpen.Gains = append([]float64(nil), o.Sharpen.Gains...)

	// The starlet gains are deliberately left ALONE.
	//
	// An earlier version capped them below the measured FWHM, reasoning that a scale the optics never
	// resolved can hold only noise. That reasoning ignores where the starlet pass sits: it runs AFTER
	// deconvolution, which has just spent fifty iterations putting signal back into exactly that band.
	// Capping there does not suppress noise, it undoes the deconvolution — and the softer the capture,
	// the wider the band it suppressed, so the worst seeing got the flattest rendering. Measured on a
	// 1.06"/px capture with 9.7 px FWHM it pushed every gain below unity and produced a final image
	// visibly flatter than the stack it came from. The scale-0 suppression and the per-scale noise
	// thresholds already in DefaultSharpen are the right place for that job, and they do it on the
	// MEASURED noise rather than on an assumption about what the optics could have delivered.
	return o, psf, notes
}
