package solar

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
	"github.com/verove-jordan/astronomy/internal/noise"
)

// finish.go turns a stacked linear master into the finished image, and is the whole of what the
// supervised re-tune and the post-run Refine panel replay. It is deliberately cheap — a second or
// two on a full-disc master — because the render/judge/re-tune loop runs it repeatedly.
//
// The order below is the design, and each step assumes what the one before it guarantees:
//
//	instrument flat  →  Richardson-Lucy  →  starlet  →  limb-darkening flatten
//	                 →  prominence composite  →  palette + tone
//
// The two that are easy to get backwards: the instrument flat comes BEFORE deconvolution, because
// it is a multiplicative corruption of the image rather than part of the scene the PSF acted on;
// and the limb-darkening flatten comes AFTER both sharpening stages, because its gain rises fastest
// exactly at the limb, where the PSF matters most, and because RL's damping and the starlet
// thresholds both assume noise that is still stationary across the frame.

// FinishOptions is the full tunable surface of the finish — the tier-A knobs.
type FinishOptions struct {
	FlatStrength float64 // 0..1, instrument-field removal
	DeconvSigma  float64 // px; 0 disables
	DeconvIters  int
	// DeconvAuto measures the point spread function off the limb and deconvolves at THAT width,
	// rather than at DeconvSigma. It is on by default because the width is the one setting here with
	// a true value rather than a tasteful one, and because the alternative — one constant across
	// captures whose disc ranges from 500 to 2000 px — is wrong for most of them. Setting the width
	// explicitly turns it off. See autotune.go.
	DeconvAuto      bool
	Sharpen         SharpenOptions
	LimbFlatten     float64 // 0..1, limb-darkening removal
	ProminenceBoost float64 // extra stretch applied off-limb, 1 = same as the disc
	// ProminenceFeather is the width of the transition band around the limb, as a fraction of the
	// radius. It is what the starlet pass's disc mask feathers over, so that sharpening fades out
	// before it reaches the limb step rather than stopping at it.
	//
	// It used to set the prominence composite's blend width as well, and that was the false bright
	// ring: at the default it reached eighteen pixels INSIDE the limb, and the off-limb curve renders
	// a disc-level pixel as pure white. The composite now gates strictly at the limb — see
	// blendProminences — and this knob no longer reaches it.
	ProminenceFeather float64
	Palette           string
	Stretch           float64 // midtone lift
	Contrast          float64
	Saturation        float64
	// BackgroundLevel is how bright the sky renders, 0..1, and BackgroundTint how much of the
	// palette's own hue it carries — 0 neutral grey, 1 the palette's deep end at that brightness.
	//
	// The sky is not black in a solar image anyone admires, and it was being driven to exactly zero
	// twice over: once by the disc tone curve's black anchor and again by the prominence composite.
	// A small warm pedestal is what the reference images have, and it costs nothing — it is a floor,
	// not a stretch, so it cannot change any contrast on the disc.
	BackgroundLevel float64
	BackgroundTint  float64
	// GlowStrength is the halo's brightness where it meets the limb, as a fraction of what the limb
	// itself renders at; GlowRadius is its e-folding scale as a fraction of the disc radius.
	//
	// This is deliberate rendering, not a restored artefact. The real scattered-light aureole is
	// measured and subtracted on purpose (offLimbProfile) and must stay subtracted, because leaving
	// it in makes every prominence render on a sloping background. Putting a controlled one back
	// afterwards is a separate decision, and one the user can turn off.
	GlowStrength float64
	GlowRadius   float64
}

// DefaultFinish is the standard Hα full-disc recipe.
func DefaultFinish() FinishOptions {
	return FinishOptions{
		FlatStrength: 0.6,
		// Deconvolution is set for a STACK, not for a frame, and that is a much larger budget than it
		// looks. A stack of a few hundred frames carries something like a seventeenth of a single
		// frame's noise, and deconvolution is precisely the operation that converts signal-to-noise
		// back into resolution. Measured on real phone video, 2.0/50 recovers ~2.7x the band-pass
		// detail of the unsharpened master with no limb ringing, where the old 1.4/12 recovered 1.6x
		// and left the result looking flatter than one noisy frame — a single frame's grain reads as
		// texture to the eye, so an honestly cleaner stack looks worse until this is turned up.
		//
		// Going further is not free: at 2.4 the plage and filaments start growing dark halos. The
		// damping term is what makes a high iteration count safe rather than reckless — it scales the
		// update by the local residual against the MEASURED noise, so the same setting self-limits on
		// a noisy master instead of amplifying it.
		// These two are the FALLBACK, used when the limb is too broken to measure. Every ordinary run
		// replaces the width with the one it measured (autotune.go), which on real captures has come
		// back anywhere between 0.8 and 1.6 px — a range no single constant covers.
		DeconvSigma:       2.0,
		DeconvIters:       50,
		DeconvAuto:        true,
		Sharpen:           DefaultSharpen(2.0),
		LimbFlatten:       0.85,
		ProminenceBoost:   1.0,
		ProminenceFeather: 0.020,
		Palette:           PaletteGold,
		Stretch:           0.5,
		Contrast:          1.0,
		Saturation:        1.0,
		BackgroundLevel:   0.03,
		BackgroundTint:    1.0,
		// 1.0 is "as bright as the limb", which is the brightest a halo can be without the finished
		// image rising across the limb. It is the natural default rather than a bold one: the ceiling
		// is set by the disc tone curve, which puts its black point well above the sky and so leaves
		// the limb itself rendering fairly dark.
		GlowStrength: 1.0,
		GlowRadius:   0.05,
	}
}

// Finish renders a stacked linear master into a display-ready RGB image.
func Finish(master *fits.Image, l Limb, o FinishOptions) *fits.Image {
	w, h := master.W, master.H
	p := append([]float32(nil), master.Pix[0]...)

	Deflat(p, w, h, l, o.FlatStrength)

	sigma := noise.Measure(&fits.Image{W: w, H: h, C: 1, Pix: [][]float32{p}}).Sigma
	if o.DeconvSigma > 0 && o.DeconvIters > 0 {
		p = RichardsonLucy(p, w, h, l, o.DeconvSigma, o.DeconvIters, sigma)
	}
	mask := DiscMask(w, h, l, math.Max(o.ProminenceFeather, 0.004)*l.R)
	p = StarletSharpen(p, w, h, o.Sharpen, sigma, mask)

	// Limb darkening is removed in LINEAR light, before any tone curve. Dividing a tone-mapped image
	// by a radial gain does not flatten it — the curve has already compressed the very differences
	// the gain is meant to cancel, so the correction lands in the wrong place and the disc comes out
	// washed out rather than flat.
	FlattenLimbDarkening(p, w, h, l, o.LimbFlatten)

	// The disc and the off-limb are then rendered separately and blended, because they differ in
	// brightness by two orders of magnitude: a prominence is a couple of percent of the disc, and no
	// single tone curve shows both without either burning the surface or losing the prominences.
	disc := toneMapDisc(p, w, h, l, o.Stretch, o.Contrast)
	// The glow goes on BEFORE the prominences so that it is a floor they stand on rather than a veil
	// laid over them: both composite by taking the brighter of the two, so a prominence brighter than
	// the halo survives at full strength and one fainter than it simply disappears into it, which is
	// what a halo does in reality.
	addDiscGlow(disc, w, h, l, o)
	if o.ProminenceBoost > 0 {
		blendProminences(disc, p, w, h, l, o)
	}
	return applyPalette(disc, w, h, o)
}

// addDiscGlow lays a soft halo outside the limb, decaying outward.
//
// Its brightness at the limb is a FRACTION of what the limb itself renders at, and it is composited
// by taking the brighter of the two rather than by adding. Both details exist for the same reason:
// the finished image must never brighten on the way out across the limb, and a glow term added on
// top of a falling edge does exactly that — it would put back, as a deliberate feature, the same
// bright ring the prominence composite was just fixed to stop producing. Anchoring below the limb's
// own rendering and taking a maximum makes the profile monotone by construction.
func addDiscGlow(disc []float32, w, h int, l Limb, o FinishOptions) {
	if o.GlowStrength <= 0 || o.GlowRadius <= 0 || l.R <= 0 {
		return
	}
	// The ceiling is what the disc renders at ON the limb. Averaging over the last percent of the
	// radius instead — which sounds equivalent and is not — reads mostly still-bright disc, and
	// measured that way the ceiling came back three times too high and the glow put the bright ring
	// straight back.
	lo, hi := (l.R-glowAnchorPx)/l.R, (l.R+glowAnchorPx)/l.R
	edge := float64(imgops.Percentile(imgops.Subsample(annulusSamples(disc, w, h, l, lo, hi), 100000), 50))
	if edge <= 0 {
		return
	}
	peak := clampF(o.GlowStrength, 0, 1) * edge
	scale := math.Max(o.GlowRadius, 1e-4) * l.R
	for y := 0; y < h; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < w; x++ {
			dx := float64(x) - l.CX
			d := math.Hypot(dx, dy)
			if d < l.R {
				continue
			}
			i := y*w + x
			if g := float32(peak * math.Exp(-(d-l.R)/scale)); g > disc[i] {
				disc[i] = g
			}
		}
	}
}

// annulusSamples collects the pixels between two radius fractions.
func annulusSamples(p []float32, w, h int, l Limb, lo, hi float64) []float32 {
	var vals []float32
	lo2, hi2 := (lo*l.R)*(lo*l.R), (hi*l.R)*(hi*l.R)
	for y := 0; y < h; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < w; x++ {
			dx := float64(x) - l.CX
			if d2 := dx*dx + dy*dy; d2 >= lo2 && d2 <= hi2 {
				vals = append(vals, p[y*w+x])
			}
		}
	}
	return vals
}

// promGateFrac is how far outside the limb, as a fraction of the radius, the off-limb rendering
// fades in.
//
// It has to complete within the PHYSICAL limb transition — the PSF plus the chromosphere, on the
// order of ten pixels at any plate scale we see — and not one pixel sooner. Everything about this
// stage's history says so: the blend used to start eighteen pixels inside the limb and finish
// eighteen outside, which put disc pixels through the off-limb curve, and that is where the bright
// ring around every finished image came from.
//
// It is a constant rather than the ProminenceFeather knob because the two want opposite things. The
// knob feathers the sharpening mask and wants to be generous; this wants to be as tight as the data
// allows, because everything it spans is a prominence's brightest, most structured part — its base.
const promGateFrac = 0.005

// glowAnchorPx is the half-width, in pixels, of the band the synthetic glow reads its ceiling from —
// a thin ring centred ON the limb.
//
// Centred there, and nowhere else, is what makes the glow safe by construction rather than by
// tuning. The disc rendering never increases outward, so its value anywhere inside the limb is at
// least its value AT the limb; a glow that starts at a fraction of that and decays outward can
// therefore never make the finished profile rise. Reading the ceiling from a band INSIDE the limb
// instead — the obvious first choice — reads a brighter number than the limb itself and puts a small
// step back at the join; reading it from a band outside reads the tone curve's cliff and makes the
// glow invisible. It is stated in pixels because the limb transition is a few pixels wide whatever
// size the disc is.
const glowAnchorPx = 1.0

// blendProminences composites an off-limb rendering, stretched for the faint stuff, over the disc
// rendering.
//
// It composites by taking whichever of the two renderings is BRIGHTER, gated to the off-limb side,
// rather than crossfading between them across the limb. The difference matters because the two
// curves do not merely differ in the transition, they diverge violently in it: on a flattened disc
// the off-limb curve reads a pixel as saturated white while the disc curve renders it at two thirds.
// Any crossfade wide enough to look smooth therefore paints a bright band exactly where the physical
// limb sits, and the narrower it is made the more it looks like a hard edge instead. Taking the
// maximum has neither failure: on the disc the off-limb curve is never consulted at all, and off the
// limb — where the background model has already removed the disc's own scattered-light skirt — it
// reads near zero in empty sky, so the composite simply follows the disc curve down to black and
// lifts only where a prominence really stands.
func blendProminences(disc, linear []float32, w, h int, l Limb, o FinishOptions) {
	if l.R <= 0 {
		return
	}
	// Off-limb brightness is expressed as a FRACTION OF THE DISC, after subtracting the sky.
	//
	// The tempting alternative — normalise the off-limb region against its own brightest pixel — is
	// wrong in the case that matters most: on a quiet limb with no prominences at all, the brightest
	// off-limb pixel is sky, so sky maps to full white and the finished image comes out with a
	// glowing halo where there is nothing. Anchoring on the disc keeps the scale physical: a
	// prominence really is a couple of percent of the disc, and empty sky really is zero.
	// The background subtracted off-limb is a FUNCTION OF RADIUS, not one number.
	//
	// A small refractor scatters light, so the sky just outside the limb is genuinely much brighter
	// than the sky further out — a smooth aureole falling off with radius. Subtracting a single sky
	// level leaves that aureole intact, and the prominence stretch then renders it as a glowing ring
	// around the whole disc, which is the most common way a solar image looks wrong. The aureole is
	// azimuthally symmetric while prominences are not, so subtracting the off-limb radial median
	// removes the halo and leaves the prominences standing on it.
	halo := offLimbProfile(linear, w, h, l)
	sky := offLimbLevel(linear, w, h, l)
	ref := imgops.Percentile(imgops.Subsample(onDiscSamples(linear, w, h, l, 0.5), 100000), 50)
	if ref-sky <= 1e-9 {
		return
	}
	// The ceiling is what the DISC renders at, not 1. Nothing outside the limb is brighter than the
	// Sun, so nothing outside the limb should render brighter than it either — and without this a
	// prominence at forty percent of disc brightness comes out whiter than the disc, which is the
	// glowing-rim look. Faint prominences, which is nearly all of them, are unaffected: the ceiling
	// only ever clips the curve's top.
	ceil := float64(imgops.Percentile(imgops.Subsample(onDiscSamples(disc, w, h, l, 0.5), 100000), 50))
	if ceil <= 0 {
		return
	}
	gate := promGateFrac * l.R
	// asinh rather than a power curve: prominences span a wide range of faintness and this keeps the
	// dim ones visible without pushing the sky off zero.
	k := 40 * math.Max(o.ProminenceBoost, 0.01)
	den := math.Asinh(k)
	for y := 0; y < h; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < w; x++ {
			dx := float64(x) - l.CX
			d := math.Hypot(dx, dy)
			// Strictly outside. Below the limb the background model is extrapolated rather than
			// measured — it is only ever sampled from 1.0 R outward — so a disc pixel put through this
			// curve is not merely rendered on the wrong scale, it is rendered against a background that
			// was never measured where it sits.
			if d < l.R {
				continue
			}
			i := y*w + x
			frac := (float64(linear[i]) - halo.at(d/l.R, math.Atan2(dy, dx))) / (ref - sky)
			prom := clampF(math.Asinh(k*math.Max(frac, 0))/den, 0, ceil)
			if v := float32(prom * smoothstep((d-l.R)/gate)); v > disc[i] {
				disc[i] = v
			}
		}
	}
}

const (
	// haloBins is how finely the off-limb background is sampled in radius.
	haloBins = 96
	// haloWarp shapes that sampling, which is not uniform in radius and must not be.
	//
	// The scattered-light skirt falls by an order of magnitude within a few pixels of the limb and
	// then by almost nothing over the next few hundred. Uniform annuli spend the same resolution on
	// both: the first bin averages the entire limb transition and reports the middle of it as "the
	// background" everywhere inside that bin. Subtracting an under-estimate leaves a positive
	// residual, the prominence stretch — whose whole job is to make small residuals visible —
	// renders it, and the result is a bright ring sitting just outside the limb.
	//
	// Sampling in t^0.75 rather than t puts roughly a one-pixel bin against the limb and ~7 px bins
	// at the far end, which is the resolution each region actually needs.
	haloWarp = 0.75
	// haloHarmonics is the highest azimuthal order fitted to the background at each radius.
	//
	// The off-limb background is not a function of radius alone. The scattered-light aureole is, but
	// the eyepiece vignette and field stop of an afocal setup are centred on the optical axis rather
	// than on the Sun, and measured on real frames the sky varied by a factor of three between
	// quadrants — more than the height of the aureole at the same radius. Subtracting only the radial
	// median leaves the bright quadrants glowing and drives the dim ones below zero, which is what
	// paints arcs and mottle into what should be empty sky.
	//
	// A low-order Fourier series is the right basis for that, and — this is the point — it is the
	// basis that CANNOT eat a prominence. Order 1 is a gradient across the field, order 2 an
	// ellipticity; a prominence a few degrees wide would need harmonics of order thirty or more to
	// represent. Raising this would start to describe them, and describing them here means
	// subtracting them.
	haloHarmonics = 2
	// haloFitSectors is how many azimuthal samples the harmonics are fitted to. Each one is a median,
	// so a prominence is already a minority within its own sector; the fit then spreads whatever
	// survives across five coefficients estimated from all of them.
	haloFitSectors = 24
	// haloCoefs is the coefficient count per radial bin: a0 plus a cosine and a sine per harmonic.
	haloCoefs = 1 + 2*haloHarmonics
)

// haloProfile is the off-limb background: a radial median plus a low-order azimuthal series at each
// radius.
type haloProfile struct {
	lo, hi float64
	bins   []float64 // radial median per bin, used where the series could not be fitted
	coef   []float64 // haloBins x haloCoefs, [a0, a1, b1, a2, b2] per bin
}

// at evaluates the modelled background at a radius fraction and angle.
func (h haloProfile) at(frac, ang float64) float64 {
	if len(h.bins) == 0 {
		return 0
	}
	// Every bin holds the median of a RANGE, so it describes the centre of that range, not its lower
	// edge. Interpolating as though it sat at the edge shifts the whole model by half a bin, and on a
	// background with any gradient that shift is subtracted as if it were signal — leaving a residue
	// shaped like the thing it was meant to remove.
	t := haloBinOf(frac, h.lo, h.hi) - 0.5
	t = clampF(t, 0, float64(haloBins-1))
	i := clampInt(int(t), 0, haloBins-1)
	fr := clampF(t-float64(i), 0, 1)
	i1 := i
	if i+1 < haloBins {
		i1 = i + 1
	}
	if len(h.coef) == 0 {
		return h.bins[i] + fr*(h.bins[i1]-h.bins[i])
	}
	return h.evalBin(i, ang) + fr*(h.evalBin(i1, ang)-h.evalBin(i, ang))
}

// haloBinOf maps a radius fraction to a fractional bin index under the warped sampling. It is the
// one place the warp is expressed, so measuring and evaluating the model cannot drift apart.
func haloBinOf(frac, lo, hi float64) float64 {
	return math.Pow(clampF((frac-lo)/(hi-lo), 0, 1), haloWarp) * haloBins
}

// evalBin sums the azimuthal series for one radial bin.
func (h haloProfile) evalBin(i int, ang float64) float64 {
	c := h.coef[i*haloCoefs : (i+1)*haloCoefs]
	v := c[0]
	for k := 1; k <= haloHarmonics; k++ {
		v += c[2*k-1]*math.Cos(float64(k)*ang) + c[2*k]*math.Sin(float64(k)*ang)
	}
	return v
}

// offLimbProfile measures the sky outside the limb: a median in each (radius, sector) cell, then a
// low-order azimuthal series fitted per radius.
//
// The median is what leaves prominences alone — they occupy a small fraction of any cell, so they
// barely move it, while the background fills every cell and defines it.
func offLimbProfile(p []float32, w, h int, l Limb) haloProfile {
	prof := haloProfile{
		lo: 1.0, hi: 1.6,
		bins: make([]float64, haloBins),
		coef: make([]float64, haloBins*haloCoefs),
	}
	buckets := make([][]float32, haloBins)
	cells := make([][]float32, haloBins*haloFitSectors)
	for y := 0; y < h; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < w; x++ {
			dx := float64(x) - l.CX
			frac := math.Hypot(dx, dy) / l.R
			if frac < prof.lo || frac > prof.hi {
				continue
			}
			b := clampInt(int(haloBinOf(frac, prof.lo, prof.hi)), 0, haloBins-1)
			v := p[y*w+x]
			buckets[b] = append(buckets[b], v)
			a := math.Atan2(dy, dx) / (2 * math.Pi) * haloFitSectors
			for a < 0 {
				a += haloFitSectors
			}
			s := clampInt(int(a), 0, haloFitSectors-1)
			cells[b*haloFitSectors+s] = append(cells[b*haloFitSectors+s], v)
		}
	}
	last := 0.0
	for i, vals := range buckets {
		if len(vals) < 32 {
			prof.bins[i] = last
			continue
		}
		prof.bins[i] = float64(imgops.Percentile(imgops.Subsample(vals, 50000), 50))
		last = prof.bins[i]
	}
	smoothProfile(prof.bins)

	samples := make([]float64, haloFitSectors)
	valid := make([]bool, haloFitSectors)
	for i := 0; i < haloBins; i++ {
		for s := 0; s < haloFitSectors; s++ {
			vals := cells[i*haloFitSectors+s]
			// A sector with no data is left OUT of the fit rather than filled in with the radial
			// median. Substituting is the tempting shortcut, because it keeps the ring uniform and the
			// projection exact — but the substituted value is not the background there, and feeding it
			// to a five-parameter fit corrupts every coefficient, so the error leaks all the way round
			// the disc instead of staying where the data was missing. Rings are routinely incomplete:
			// past about 1.2 R only the raster corners still reach.
			if valid[s] = len(vals) >= 24; !valid[s] {
				continue
			}
			samples[s] = float64(imgops.Percentile(imgops.Subsample(vals, 50000), 50))
		}
		fitAzimuth(samples, valid, prof.bins[i], prof.coef[i*haloCoefs:(i+1)*haloCoefs])
	}
	// The background varies slowly with radius, so smoothing each coefficient suppresses the
	// per-bin measurement noise that would otherwise be subtracted as though it were structure.
	series := make([]float64, haloBins)
	for c := 0; c < haloCoefs; c++ {
		for i := 0; i < haloBins; i++ {
			series[i] = prof.coef[i*haloCoefs+c]
		}
		smoothProfile(series)
		for i := 0; i < haloBins; i++ {
			prof.coef[i*haloCoefs+c] = series[i]
		}
	}
	return prof
}

// fitAzimuth fits the low-order azimuthal series to the sectors that carry data.
//
// Least squares over the valid sectors only, which means solving the normal equations rather than
// taking a Fourier projection — the valid sectors are not uniformly spaced once a ring is partly
// outside the raster, and the projection is only the least-squares answer when they are.
//
// It runs twice. The second pass is the guard that matters: a prominence bright and broad enough to
// survive its own sector's median would otherwise pull every coefficient a little, spreading a
// shadow of itself right around the disc. Pulling outliers onto the first model rather than dropping
// them keeps the system well-conditioned when few sectors remain.
func fitAzimuth(samples []float64, valid []bool, fallback float64, out []float64) {
	for c := range out {
		out[c] = 0
	}
	out[0] = fallback
	n := 0
	for _, v := range valid {
		if v {
			n++
		}
	}
	// Five coefficients need comfortably more than five constraints, and a ring reduced to a couple
	// of slivers cannot say anything about a gradient. Below that the radial median stands alone,
	// which is the honest answer rather than an extrapolated one.
	if n < 2*haloCoefs {
		return
	}
	work := append([]float64(nil), samples...)
	for pass := 0; pass < 2; pass++ {
		var ata [haloCoefs * haloCoefs]float64
		var aty [haloCoefs]float64
		for j := range work {
			if !valid[j] {
				continue
			}
			b := azimuthBasis(j, len(work))
			for r := 0; r < haloCoefs; r++ {
				aty[r] += b[r] * work[j]
				for c := 0; c < haloCoefs; c++ {
					ata[r*haloCoefs+c] += b[r] * b[c]
				}
			}
		}
		// A whisper of ridge: with an incomplete ring the high harmonics can be nearly degenerate,
		// and this makes the solve prefer the smaller coefficient rather than a wild one.
		for r := 0; r < haloCoefs; r++ {
			ata[r*haloCoefs+r] += 1e-6 * float64(n)
		}
		sol, ok := solveSmall(ata[:], aty[:], haloCoefs)
		if !ok {
			return
		}
		copy(out, sol)
		if pass == 1 {
			break
		}
		resid := make([]float64, len(work))
		abs := make([]float64, 0, len(work))
		for j := range work {
			if !valid[j] {
				continue
			}
			b := azimuthBasis(j, len(work))
			var m float64
			for r := 0; r < haloCoefs; r++ {
				m += b[r] * out[r]
			}
			resid[j] = work[j] - m
			abs = append(abs, math.Abs(resid[j]))
		}
		scale := median(abs)
		if scale <= 0 {
			break
		}
		for j := range work {
			if valid[j] && math.Abs(resid[j]) > 3*scale {
				work[j] -= resid[j] // pull the outlier onto the model
			}
		}
	}
}

// azimuthBasis is the design row for sector j of n: the centre angle evaluated in the series basis.
func azimuthBasis(j, n int) [haloCoefs]float64 {
	ang := 2 * math.Pi * (float64(j) + 0.5) / float64(n)
	var b [haloCoefs]float64
	b[0] = 1
	for k := 1; k <= haloHarmonics; k++ {
		b[2*k-1] = math.Cos(float64(k) * ang)
		b[2*k] = math.Sin(float64(k) * ang)
	}
	return b
}

// solveSmall solves a small dense symmetric system by Gaussian elimination with partial pivoting.
func solveSmall(a, y []float64, n int) ([]float64, bool) {
	m := append([]float64(nil), a...)
	v := append([]float64(nil), y...)
	for col := 0; col < n; col++ {
		p := col
		for r := col + 1; r < n; r++ {
			if math.Abs(m[r*n+col]) > math.Abs(m[p*n+col]) {
				p = r
			}
		}
		if math.Abs(m[p*n+col]) < 1e-12 {
			return nil, false
		}
		if p != col {
			for c := 0; c < n; c++ {
				m[p*n+c], m[col*n+c] = m[col*n+c], m[p*n+c]
			}
			v[p], v[col] = v[col], v[p]
		}
		for r := col + 1; r < n; r++ {
			f := m[r*n+col] / m[col*n+col]
			if f == 0 {
				continue
			}
			for c := col; c < n; c++ {
				m[r*n+c] -= f * m[col*n+c]
			}
			v[r] -= f * v[col]
		}
	}
	out := make([]float64, n)
	for r := n - 1; r >= 0; r-- {
		s := v[r]
		for c := r + 1; c < n; c++ {
			s -= m[r*n+c] * out[c]
		}
		out[r] = s / m[r*n+r]
	}
	return out, true
}

// offLimbLevel estimates the sky background from an annulus beyond any prominence.
func offLimbLevel(p []float32, w, h int, l Limb) float64 {
	var vals []float32
	for _, band := range [][2]float64{{1.30, 1.50}, {1.15, 1.30}, {1.02, 1.15}} {
		vals = vals[:0]
		for y := 0; y < h; y++ {
			dy := float64(y) - l.CY
			for x := 0; x < w; x++ {
				dx := float64(x) - l.CX
				if d := math.Hypot(dx, dy) / l.R; d >= band[0] && d <= band[1] {
					vals = append(vals, p[y*w+x])
				}
			}
		}
		if len(vals) > 500 {
			// The median, not the minimum: prominences are a small part of any annulus, so the
			// median reads sky even when they are present.
			return imgops.Percentile(imgops.Subsample(vals, 100000), 50)
		}
	}
	return 0
}

// onDiscSamples collects the pixels inside a radius fraction.
func onDiscSamples(p []float32, w, h int, l Limb, frac float64) []float32 {
	var vals []float32
	r2 := (frac * l.R) * (frac * l.R)
	for y := 0; y < h; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < w; x++ {
			dx := float64(x) - l.CX
			if dx*dx+dy*dy <= r2 {
				vals = append(vals, p[y*w+x])
			}
		}
	}
	return vals
}

// toneMapDisc renders the flattened disc into 0..1.
//
// The window is anchored on the DISC LEVEL, not on the frame's percentiles. That is the difference
// between a solar image and a white blob. After flattening, the surface is nearly uniform and every
// feature worth seeing — filaments, plage, sunspots — is a modest departure from that one level, so
// the useful contrast lives in a narrow band around it. A curve fitted to the frame's own histogram
// instead maps that whole band onto a sliver near white, which is exactly what a general-purpose
// stretch does to the Sun.
//
// Sky falls below the window and clamps to black, leaving the off-limb rendering to blendProminences.
func toneMapDisc(p []float32, w, h int, l Limb, stretch, contrast float64) []float32 {
	out := make([]float32, len(p))
	if contrast <= 0 {
		contrast = 1
	}
	black := offLimbLevel(p, w, h, l)
	ref := imgops.Percentile(imgops.Subsample(onDiscSamples(p, w, h, l, 0.6), 200000), 50)
	span := ref - black
	if span <= 1e-9 {
		span = math.Max(ref, 1e-6)
	}
	// The window: a fraction below the disc level for sunspots and filaments, a smaller fraction
	// above it for plage and flares, both tightened by contrast.
	below := 0.62 / contrast
	above := 0.30 / contrast
	lo := black + span*(1-below)
	hi := black + span*(1+above)
	if hi-lo <= 1e-12 {
		hi = lo + 1e-12
	}
	// stretch shifts the midtones: 0.5 is linear, higher lifts the faint end.
	gamma := 1 / (0.5 + clampF(stretch, 0, 1))
	for i, v := range p {
		u := (float64(v) - lo) / (hi - lo)
		out[i] = float32(clampF(shoulderCurve(toeCurve(u, gamma)), 0, 1))
	}
	return out
}

// shoulderThreshold is where the display curve stops being linear and rolls into its highlight
// shoulder.
const shoulderThreshold = 0.85

// shoulderCurve rolls the top of the curve off asymptotically instead of clipping it.
//
// This is the toe's mirror image and it exists for a sharper reason. The window above the disc level
// is deliberately narrow — plage and flares are only tens of percent brighter than the quiet Sun, so
// a wide window would leave the whole disc grey — and that guarantees the brightest plage runs past
// the top of it. Clipping there does not merely brighten those regions, it DELETES them: plage is
// the most structured thing on the disc, and a hard ceiling renders every part of it as the same
// flat white blob, which is the one place a viewer looks for detail.
//
// tanh is chosen because it meets the linear segment with matching value AND matching slope — its
// derivative at zero is exactly one — so nothing kinks at the join, and it approaches 1 without ever
// reaching it, so no amount of headroom above the window can clip.
func shoulderCurve(y float64) float64 {
	if y <= shoulderThreshold {
		return y
	}
	room := 1 - shoulderThreshold
	return shoulderThreshold + room*math.Tanh((y-shoulderThreshold)/room)
}

// toeThreshold is where the curve stops being a straight power law and rolls into its toe.
const toeThreshold = 0.10

// toeCurve is the display curve: a power law above the toe, an exponential roll-off below it.
//
// The roll-off exists because a hard black clip does not just crush shadows, it SPECKLES them. Right
// at the limb the brightness falls through the black point within a couple of pixels, so noise
// scatters individual pixels either side of it and half of them snap to zero — the result is a
// dotted arc tracing the limb, which reads as a processing artefact rather than as a dark edge.
// Rolling off asymptotically keeps that region continuous and dark instead of binary.
func toeCurve(u, gamma float64) float64 {
	if u >= toeThreshold {
		return math.Pow(u, gamma)
	}
	edge := math.Pow(toeThreshold, gamma)
	// Exponential approach to zero, matching value and staying monotone at the join.
	return edge * math.Exp((u-toeThreshold)/toeThreshold)
}
