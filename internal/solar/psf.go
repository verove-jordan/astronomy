package solar

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// psf.go measures what a solar capture actually resolved, from the one calibrated target every
// solar frame carries: its own limb.
//
// This exists because the finish cannot be tuned by a constant. Sharpening is only ever a bet about
// which spatial scales hold signal, and that answer moves by a factor of three between an afocal
// phone clip at 1x and the same scope at full digital zoom — the disc lands anywhere from 500 to
// 2000 px across. A fixed deconvolution width is therefore right for one capture and wrong for the
// rest, and wrong in the worst possible way: a kernel WIDER than the true point spread function does
// not blur, it over-corrects, manufacturing texture at scales the optics never delivered while
// leaving the band that is genuinely blurred untouched. The result reads as noisy AND soft at the
// same time, which is exactly how it is usually described.
//
// At this plate scale the chromosphere's own scale height is far below a pixel, so the true limb is
// a step. Everything that smears it — aperture, seeing, the phone's own processing, the stack's
// resampling — is the system PSF, and it can simply be read off.

const (
	// psfHalfPx is how far either side of the limb the edge is sampled, and psfStepPx how finely.
	// Sampling in pixels rather than in fractions of the radius matters: the edge is a few pixels
	// wide whatever the disc's size, and a step defined against R would resolve it on a large disc
	// and miss it entirely on a small one.
	psfHalfPx = 12.0
	psfStepPx = 0.25
	// psfRays is how many radii the edge is sampled along.
	psfRays = 2880
	// psfSectors is how many independent azimuthal wedges the edge is measured in.
	//
	// Measuring per sector and taking the median of the WIDTHS is the whole design, and the obvious
	// alternative — average the whole ring, then measure that one profile — is what makes this
	// unmeasurable on a large disc. The fitted circle is not the limb: on a real 900 px master it
	// misses by about 2.5 px RMS, from ellipticity, from the sub-pixel centring, and from the disc
	// drifting during the stack. A ring average therefore spreads the edge over that residual and
	// reports it as blur, so the same image measured against two slightly different fitted circles
	// came back anywhere between 1.3 and 4.9 px. Within one wedge the residual is a SHIFT of the edge,
	// not a smearing of it, and a shift does not change a width.
	psfSectors = 72
	// psfMinSectorRays is the smallest number of radii a sector needs to be measured.
	psfMinSectorRays = 8
	// psfMinSectors is how many wedges must yield a width. A limb close-up shows only an arc, which is
	// still plenty; a handful of wedges is not.
	psfMinSectors = 12
	// psfSigmaMin and psfSigmaMax bound the answer. Below the lower bound the edge is at the sampling
	// limit and no deconvolution is meaningful; above the upper one the fit has locked onto something
	// that is not the limb.
	psfSigmaMin, psfSigmaMax = 0.5, 5.0
)

// PSF is the measured resolution of a solar image.
type PSF struct {
	// SigmaPx is the Gaussian-equivalent half-width of the line-spread function, in pixels of the
	// image it was measured on.
	SigmaPx float64 `json:"sigma_px"`
	// FWHMArcsec is the same figure in arcseconds, which is what makes two captures comparable.
	FWHMArcsec float64 `json:"fwhm_arcsec"`
	// Overshoot is the bright shelf just inside the limb, above the smooth limb-darkening trend, as
	// a fraction of it. An optical edge cannot overshoot; a sharpened one does. It is therefore a
	// direct measurement of processing applied before the pixels ever reached us — which for a phone
	// clip is the camera's own pipeline, and which every phone applies whether or not it is asked to.
	Overshoot float64 `json:"overshoot"`
	OK        bool    `json:"ok"`
}

// Sharpened reports whether the source had a sharpener run over it before we saw it.
func (p PSF) Sharpened() bool { return p.OK && p.Overshoot >= psfOvershootFloor }

// psfOvershootFloor is the smallest overshoot worth acting on. Below it the shelf is within what a
// slightly imperfect limb fit or the stack's own resampling can produce.
const psfOvershootFloor = 0.01

// MeasurePSF reads the system point spread function off the limb.
//
// The width is the MEDIAN of the per-sector widths. Taking a median of measurements — rather than
// measuring one averaged profile — is also what keeps a prominence, a plage running to the limb or a
// filament crossing it from counting: each is confined to a wedge or two, and a median over
// seventy-two of them steps over any of that.
func MeasurePSF(im *fits.Image, l Limb) PSF {
	if l.R <= 0 {
		return PSF{}
	}
	return MeasureEdge(im, l, edgeFalling, sunAngularDiameterArcsec/(2*l.R))
}

// MeasureEdge is MeasurePSF over any circular edge, in either direction and at a stated plate scale.
//
// It exists for the OCCULTER'S limb, which is the better probe of the two whenever there is one. The
// solar limb is not a step: it carries limb darkening on the way in, a chromospheric skirt and
// prominences on the way out, and the profile has to be read carefully to keep those from being
// counted as blur. The Moon's edge has none of that — it is an opaque body against the Sun, so the
// only thing that spreads it is the system. Handing back a plate scale rather than deriving one is
// part of the same idea: the occulter's radius is not the Sun's, and 2R is only the Sun's angular
// diameter for the Sun.
func MeasureEdge(im *fits.Image, l Limb, dir edgeDirection, arcsecPerPx float64) PSF {
	if im == nil || len(im.Pix) == 0 || l.R <= 0 {
		return PSF{}
	}
	prof := make([]float64, 2*int(psfHalfPx/psfStepPx)+1)
	var widths, overshoots []float64
	for s := 0; s < psfSectors; s++ {
		if !sectorProfile(im, l, s, prof) {
			continue
		}
		if dir == edgeRising {
			// A rising edge read outward is a falling edge read inward, and a width is unchanged by
			// which way it was walked — so the one estimator serves both.
			reverseProfile(prof)
		}
		sigma, over, ok := edgeWidth(prof)
		if !ok || sigma < psfSigmaMin || sigma > psfSigmaMax {
			continue
		}
		widths = append(widths, sigma)
		overshoots = append(overshoots, over)
	}
	if len(widths) < psfMinSectors {
		return PSF{}
	}
	sigma := median(widths)
	return PSF{
		SigmaPx:    sigma,
		FWHMArcsec: 2.355 * sigma * arcsecPerPx,
		Overshoot:  median(overshoots),
		OK:         true,
	}
}

// reverseProfile flips an edge-spread function end for end, in place.
func reverseProfile(v []float64) {
	for i, j := 0, len(v)-1; i < j; i, j = i+1, j-1 {
		v[i], v[j] = v[j], v[i]
	}
}

// sectorProfile fills prof with the mean edge-spread function across one azimuthal wedge, and
// reports whether the wedge carried enough of the limb to measure.
func sectorProfile(im *fits.Image, l Limb, sector int, prof []float64) bool {
	rays := psfRays / psfSectors
	a0 := 2 * math.Pi * float64(sector) / float64(psfSectors)
	da := 2 * math.Pi / float64(psfRays)
	used := 0
	for i := range prof {
		prof[i] = 0
	}
	for k := 0; k < rays; k++ {
		ang := a0 + float64(k)*da
		cos, sin := math.Cos(ang), math.Sin(ang)
		ok := true
		for i := range prof {
			r := l.R + (float64(i)*psfStepPx - psfHalfPx)
			x, y := l.CX+r*cos, l.CY+r*sin
			if x < 2 || y < 2 || x >= float64(im.W-3) || y >= float64(im.H-3) {
				ok = false
				break
			}
		}
		if !ok {
			continue // a ray that leaves the raster part-way would contribute a truncated edge
		}
		for i := range prof {
			r := l.R + (float64(i)*psfStepPx - psfHalfPx)
			prof[i] += float64(imgops.SampleCubic(im.Pix[0], im.W, im.H, l.CX+r*cos, l.CY+r*sin))
		}
		used++
	}
	if used < psfMinSectorRays {
		return false
	}
	inv := 1 / float64(used)
	for i := range prof {
		prof[i] *= inv
	}
	return true
}

// edgeWidth turns an edge profile into a PSF sigma and an overshoot fraction.
//
// It works on the DERIVATIVE of the edge rather than on where the edge crosses fractions of its own
// height. Crossings sound simpler and are wrong here, because the disc is limb-darkened: the profile
// is already falling well before the limb arrives, so a level defined as a fraction of the disc
// brightness is reached far inside it and the edge measures several times wider than it is — on a
// real master, three times wider. Limb darkening is smooth, so in the derivative it is a low
// baseline beneath a sharp peak, and the peak's own width is the line-spread function once that
// baseline is subtracted instead of mistaken for signal.
func edgeWidth(esf []float64) (sigma, overshoot float64, ok bool) {
	if len(esf) < 16 {
		return 0, 0, false
	}
	// Central difference: brightness falls outward, so this comes out positive across the limb.
	lsf := make([]float64, len(esf)-2)
	for i := range lsf {
		lsf[i] = (esf[i] - esf[i+2]) / (2 * psfStepPx)
	}
	peak, at := 0.0, 0
	for i, v := range lsf {
		if v > peak {
			peak, at = v, i
		}
	}
	// The baseline is the limb-darkening slope, read from the inner flank far enough in that the
	// edge's own peak has died away.
	base := median(lsf[:len(lsf)/6])
	if peak <= base {
		return 0, 0, false
	}
	half := base + 0.5*(peak-base)
	cross := func(step int) float64 {
		for i := at; i >= 0 && i < len(lsf)-1 && i+step >= 0 && i+step < len(lsf); i += step {
			if lsf[i] >= half && lsf[i+step] < half {
				return float64(i) + (lsf[i]-half)/(lsf[i]-lsf[i+step])*float64(step)
			}
		}
		return math.NaN()
	}
	lo, hi := cross(-1), cross(1)
	if math.IsNaN(lo) || math.IsNaN(hi) || hi <= lo {
		return 0, 0, false
	}
	sigma = (hi - lo) * psfStepPx / 2.355

	// Overshoot: extrapolate the limb-darkening trend from the inner flank and look for brightness
	// standing above it on the way to the edge.
	inner := esf[:len(esf)/4]
	slope := (inner[len(inner)-1] - inner[0]) / float64(len(inner)-1)
	for i := len(inner); i < at && i < len(esf); i++ {
		if want := inner[0] + slope*float64(i); want > 0 {
			overshoot = math.Max(overshoot, esf[i]/want-1)
		}
	}
	return sigma, overshoot, true
}
