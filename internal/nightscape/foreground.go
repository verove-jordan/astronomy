package nightscape

// foreground.go exposes the landscape's own tone curve, for the panorama assembler.
//
// A nightscape is graded as two layers and composited last: the sky gets autoStretch, the landscape
// gets asinhStretch, and only then are they mixed (see gradeCompose). That is not a stylistic
// preference. A beach lit by nothing but a town on the horizon sits one and a half orders of
// magnitude below the sky glow it stands under — measured at 0.0018 against 0.066 on the run this
// was written for — so a single curve calibrated to put the SKY background at its target maps the
// whole landscape into the bottom couple of values and returns it as black.
//
// The white point is passed IN rather than measured here, and that is the hard-won part. Measured on
// the reprojected canvas it is wrong twice over: the landscape covers a few per cent of a mostly
// empty array, and — worse — Render divides accumulated colour by summed weight, so the panel's
// feathered edge divides by nearly nothing and arrives enormously amplified. A percentile over that
// is set by the blow-up, and normalising by it crushes the real landscape to black. Measure it where
// gradeCompose does: on the whole source frame, sky included, before any reprojection.

import "github.com/verove-jordan/astronomy/internal/fits"

// ForegroundWhitePoint is the white point gradeCompose normalises a foreground by: the normPct
// percentile over the WHOLE frame. The sky in that frame is most of what sets it, which is the
// point — it is what keeps a dark landscape rendering dark instead of being stretched to fill the
// range on its own.
func ForegroundWhitePoint(fg *fits.Image, normPct float64) float64 {
	if fg == nil {
		return 0
	}
	return percentile(allPixels(fg), normPct)
}

// StretchForeground applies the landscape curve to im in place: normalise by white, then the same
// asinh the per-panel composite uses. intensity is Look.AsinhIntensityFG. A white point of zero or
// less leaves im alone rather than guessing one.
func StretchForeground(im *fits.Image, white, intensity float64) {
	if im == nil || white <= 0 {
		return
	}
	clampZero(im)
	inv := float32(1.0 / white)
	for c := 0; c < im.C; c++ {
		p := im.Pix[c]
		for i := range p {
			v := p[i] * inv
			if v < 0 {
				v = 0
			} else if v > 1 {
				v = 1
			}
			p[i] = v
		}
	}
	if intensity <= 1.0 {
		return
	}
	applyAsinh(im, intensity)
}
