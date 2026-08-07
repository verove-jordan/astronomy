package solar

import (
	"fmt"
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// bracket.go composites an exposure-bracketed session into one high-dynamic-range master.
//
// The Sun in Hα is the subject where one exposure cannot hold the scene. A prominence is a couple of
// percent of the disc, so a capture exposed for the surface records nothing off the limb and one
// exposed for the prominences burns the surface. Bracketing is therefore normal practice, and a real
// solar folder routinely holds several clips of the same Sun at settings a few stops apart.
//
// Triage already puts those clips in one group — they share an image scale, which is what decides
// whether frames may be combined — and photometric normalisation already brings frames onto one
// level. What normalisation cannot do is USE the bracket: it maps everything onto the group median,
// which is precisely the range the bracket was shot to escape, and the windowing downstream then
// splits the session on time and frame count so each stack is drawn from a single clip anyway. Left
// alone, such a run renders one exposure and quietly reduces the others to time-lapse frames.
//
// This file is the missing step. Each exposure tier is stacked on its own — sigma clipping only
// means something against siblings of the same exposure, and every tier needs its own noise
// estimate — and the tier masters are combined here, in linear light, before the finish.

const (
	// bracketGapStops is the exposure gap that separates one tier from the next.
	//
	// It sits well above what a session drifts by and well below what a bracket is shot at. A phone
	// re-meters between frames and thin haze moves the disc level by a few percent — hundredths of a
	// stop — while a bracket is deliberate and lands one to three stops apart. A whole stop is a gap
	// nothing accidental crosses.
	bracketGapStops = 1.0
	// bracketKnots is how many levels the tonal and noise curves are sampled at. The curves are
	// smooth by construction, so the knots exist to follow the shape of a camera transfer function,
	// not to fit structure.
	bracketKnots = 24
	// bracketCeiling is the value at which a tier has run out of sensor. Ingest normalises every
	// source onto 0..1 in linear light — HLG, PQ and SDR all map their signal maximum to 1 — so the
	// ceiling is a property of the pipeline rather than of any one camera.
	bracketCeiling = 1.0
	// bracketKnee is where a tier starts losing its claim to a pixel. Below it the tier is trusted
	// fully; between knee and ceiling its weight falls to zero, so a tier that is about to clip hands
	// over smoothly instead of at a threshold that would draw its own contour into the composite.
	bracketKnee = 0.85
	// bracketBlurSigma is the smoothing applied before the tonal mapping is fitted, and the scale
	// whose residual is measured as noise.
	bracketBlurSigma = 1.2
	// bracketMinPairs is the smallest number of shared pixels a tonal fit is attempted on.
	bracketMinPairs = 5000
)

// ExposureTiers splits a group's members into exposure tiers by measured on-disc brightness.
//
// The rule is the GAP between neighbouring exposures rather than the distance from an anchor — the
// same shape as the scale grouping in triage.go, and for the same reason: anchoring cuts a
// continuous run at an arbitrary point, while a gap cuts where the user actually changed something.
//
// Tiering is per FILE. Exposure is a camera setting: it holds for a whole clip and changes between
// them, so a file is the unit that carries it, and a per-frame split would only be re-measuring the
// ISP's own metering jitter.
//
// Tiers come back brightest first, because the brightest is the reference everything else is
// measured against: it collected the most photons, so it has the best claim on the scene wherever it
// has not run out of range.
func ExposureTiers(members []Member, gapStops float64) [][]Member {
	if gapStops <= 0 {
		gapStops = bracketGapStops
	}
	var live, unmeasured []Member
	for _, m := range keepLive(members) {
		if m.OnDiscMedian > 0 {
			live = append(live, m)
			continue
		}
		unmeasured = append(unmeasured, m)
	}
	if len(live) == 0 {
		if len(unmeasured) == 0 {
			return nil
		}
		return [][]Member{unmeasured}
	}
	sort.SliceStable(live, func(i, j int) bool { return live[i].OnDiscMedian < live[j].OnDiscMedian })

	ratio := math.Pow(2, gapStops)
	var tiers [][]Member
	start := 0
	for i := 1; i <= len(live); i++ {
		if i < len(live) && live[i].OnDiscMedian <= live[i-1].OnDiscMedian*ratio {
			continue
		}
		tiers = append(tiers, live[start:i])
		start = i
	}
	// Brightest first.
	for i, j := 0, len(tiers)-1; i < j; i, j = i+1, j-1 {
		tiers[i], tiers[j] = tiers[j], tiers[i]
	}
	// A member that survived the gates without a measurable disc level cannot be tiered — it should
	// not exist, since the gate rejects a file with no fittable limb — so it joins the reference
	// tier. The point is that it is never silently dropped: a member that reaches here has been
	// judged stackable, and tiering is a way of combining frames, not another gate.
	if len(unmeasured) > 0 {
		tiers[0] = append(tiers[0], unmeasured...)
	}
	return tiers
}

// keepLive returns the members still in play after the gates.
func keepLive(members []Member) []Member {
	out := make([]Member, 0, len(members))
	for _, m := range members {
		if !m.Rejected {
			out = append(out, m)
		}
	}
	return out
}

// TierLevel is a tier's exposure, as the median of its members' measured disc levels.
func TierLevel(tier []Member) float64 {
	v := make([]float64, 0, len(tier))
	for _, m := range tier {
		v = append(v, m.OnDiscMedian)
	}
	return median(v)
}

// Exposure is one stacked exposure tier, ready to be composited.
type Exposure struct {
	Master *fits.Image
	Limb   Limb
	Label  string // how the tier is named in the run's account of itself
}

// TierReport records what the composite made of one tier.
type TierReport struct {
	Label string `json:"label"`
	// Stops is how far below the reference tier this one was exposed, as measured from the fitted
	// tonal mapping rather than from any metadata.
	Stops float64 `json:"stops"`
	// Share is the mean fraction of a merged pixel this tier contributed, over the pixels it covers.
	// It is the number that says whether the bracket was worth shooting.
	Share float64 `json:"share"`
	// RotationDeg is the field rotation solved between this tier and the reference. Bracketed clips
	// are shot one after another, so on an alt-az mount — or a phone held to an eyepiece — the field
	// has turned between them.
	RotationDeg float64 `json:"rotation_deg"`
}

// MergeResult is the composite and the account of how it was built.
type MergeResult struct {
	Master *fits.Image
	Limb   Limb
	Tiers  []TierReport
	Notes  []string
}

// MergeExposures composites exposure tiers, brightest first, into one linear master.
//
// The brightest tier is the reference: it defines the output's geometry and photometric scale, so
// everything downstream — the finish's disc-anchored tone curve, the prominence composite's "a
// prominence is a couple of percent of the disc" — keeps working on exactly the units it was
// designed for. A single tier is returned unchanged, which is what keeps an ordinary one-exposure
// session byte-identical to a run that never knew about brackets.
func MergeExposures(tiers []Exposure) (*MergeResult, error) {
	if len(tiers) == 0 {
		return nil, fmt.Errorf("bracket: no exposure tier to merge")
	}
	ref := tiers[0]
	if ref.Master == nil || ref.Limb.R <= 0 {
		return nil, fmt.Errorf("bracket: the reference tier has no fitted disc")
	}
	res := &MergeResult{Limb: ref.Limb}
	if len(tiers) == 1 {
		res.Master = ref.Master
		return res, nil
	}

	w, h := ref.Master.W, ref.Master.H
	refPix := ref.Master.Pix[0]
	refNoise := measureNoiseByLevel(refPix, w, h)

	num := make([]float64, w*h)
	den := make([]float64, w*h)
	share := make([][]float64, len(tiers))
	for i := range share {
		share[i] = make([]float64, w*h)
	}
	floor := noiseFloor(refNoise)

	for i, v := range refPix {
		val := float64(v)
		wt := headroom(val) / sq(math.Max(refNoise.eval(val), floor))
		num[i] = wt * val
		den[i] = wt
		share[0][i] = wt
	}
	res.Tiers = append(res.Tiers, TierReport{Label: ref.Label})

	for t := 1; t < len(tiers); t++ {
		rep := TierReport{Label: tiers[t].Label}
		aligned, cov, deg, err := alignToReference(ref.Master, ref.Limb, tiers[t])
		if err != nil {
			res.Notes = append(res.Notes, fmt.Sprintf("%s: %v — left out of the composite", tiers[t].Label, err))
			res.Tiers = append(res.Tiers, rep)
			continue
		}
		rep.RotationDeg = deg

		tierNoise := measureNoiseByLevel(aligned.Pix[0], w, h)
		tone, ok := measureTonalMap(aligned.Pix[0], refPix, cov, w, h, ref.Limb)
		if !ok {
			res.Notes = append(res.Notes, fmt.Sprintf(
				"%s: the two exposures share too little measurable signal to be put on one scale — left out of the composite",
				tiers[t].Label))
			res.Tiers = append(res.Tiers, rep)
			continue
		}
		rep.Stops = tone.stops()

		for i, v := range aligned.Pix[0] {
			if !cov[i] {
				continue
			}
			val := float64(v)
			// The tier's noise is carried onto the reference's scale by the LOCAL SLOPE of the tonal
			// mapping, and that single term is what makes the composite do the right thing everywhere
			// without a mask. On the disc the slope is the exposure ratio, so a darker tier is simply
			// the noisier estimate and contributes in proportion. Off the limb it is far steeper — a
			// darker tier reads sky and prominence alike as a handful of quantisation steps — so its
			// noise explodes in reference units and the prominences come, correctly, from the exposure
			// that actually recorded them.
			//
			// The slope is floored at the exposure ratio, never below it. A darker exposure cannot be
			// a more precise estimator per unit than its own ratio allows, so a fitted slope under
			// that is a property of the fit and not of the camera — and taking it at face value is the
			// one way this weighting can fail catastrophically rather than gracefully, since it hands
			// unbounded confidence to the tier that has the least to say.
			sigma := math.Max(tone.slope(val), tone.gain()) * tierNoise.eval(val)
			wt := headroom(val) / sq(math.Max(sigma, floor))
			num[i] += wt * tone.eval(val)
			den[i] += wt
			share[t][i] = wt
		}
		res.Tiers = append(res.Tiers, rep)
	}

	master := fits.NewImage(w, h, 1)
	for i := range master.Pix[0] {
		if den[i] > 0 {
			master.Pix[0][i] = float32(num[i] / den[i])
			continue
		}
		// Every tier was out of range here. Keeping the reference is the honest fallback: it is the
		// pixel the run would have produced without a bracket at all, so the composite can never be
		// worse than the exposure it started from.
		master.Pix[0][i] = refPix[i]
	}
	res.Master = master
	for t := range res.Tiers {
		res.Tiers[t].Share = meanShare(share[t], den)
	}
	return res, nil
}

// meanShare averages one tier's contribution over the pixels that were composited at all.
func meanShare(w, den []float64) float64 {
	var sum float64
	var n int
	for i, d := range den {
		if d <= 0 {
			continue
		}
		sum += w[i] / d
		n++
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// alignToReference maps a tier's master onto the reference raster.
//
// Tier masters are NOT already aligned, even though each is centred on its own disc: every stack
// picks its own sharpest frame as the rotation reference, so two tiers agree on where the disc is
// and disagree on which way up it sits. Bracketed clips are shot one after another and the field
// turns between them, so the rotation has to be solved here — and then verified, because a circle
// carries no rotation information and the estimate comes from disc structure that a dark tier may
// barely resolve. A rotation that does not improve the match against the reference is discarded
// rather than applied: a wrong rotation smears the composite in a way that reads as poor seeing.
func alignToReference(ref *fits.Image, refLimb Limb, tier Exposure) (*fits.Image, []bool, float64, error) {
	if tier.Master == nil || tier.Limb.R <= 0 {
		return nil, nil, 0, fmt.Errorf("no fitted disc")
	}
	t := SolveTransform(tier.Limb, refLimb)
	deg := 0.0
	if d, ok := EstimateRotation(ref, tier.Master, refLimb, tier.Limb); ok {
		deg = d
	}
	plain, plainCov := warpCovered(tier.Master, t, ref.W, 1, nil)
	if deg == 0 {
		return plain, plainCov, 0, nil
	}
	rot := t
	rot.RotDeg = deg
	turned, turnedCov := warpCovered(tier.Master, rot, ref.W, 1, nil)
	if discMatch(ref, turned, refLimb) <= discMatch(ref, plain, refLimb) {
		return plain, plainCov, 0, nil
	}
	return turned, turnedCov, deg, nil
}

// discMatch is the normalised correlation of two co-registered discs, over the annulus the rotation
// was solved on. It is the check, not the solve: a similarity score that a false rotation cannot
// improve.
func discMatch(a, b *fits.Image, l Limb) float64 {
	pa, pb := annulusProfile(a, l), annulusProfile(b, l)
	if pa == nil || pb == nil {
		return 0
	}
	var num, da, db float64
	for i := range pa {
		num += pa[i] * pb[i]
		da += pa[i] * pa[i]
		db += pb[i] * pb[i]
	}
	if da <= 0 || db <= 0 {
		return 0
	}
	return num / math.Sqrt(da*db)
}

// tonalMap is the monotone mapping taking one exposure's values onto another's.
type tonalMap struct{ pts []lutPoint }

// measureTonalMap fits the mapping from a tier's values onto the reference's, from the pixels they
// share.
//
// It is a PAIRED-pixel regression, not a histogram match, because two co-registered masters of the
// same Sun give something two independent frames never do: for every pixel, both exposures' reading
// of the same piece of chromosphere. Histogram matching throws that correspondence away and is only
// equivalent when both frames see identical content — which stops being true the moment one of them
// runs out of range, i.e. exactly the case a bracket exists for.
//
// Both sides are blurred before the fit. Binning on a noisy variable and averaging the other
// attenuates the slope that comes back — textbook regression dilution — and here that would land as
// a composite whose faint end is systematically flattened towards the sky. Blurring costs nothing
// that matters, because what is being fitted is a tonal relation and not a spatial one.
//
// The knots are placed at QUANTILES of the tier's own values rather than at even intervals. A solar
// raster is mostly sky with a bright disc in the middle, so even intervals would spend twenty knots
// on an empty background and one on the entire disc — and the disc is where a stop of exposure error
// is visible.
//
// It is fitted ON THE DISC, and only there. That bound is not a refinement, it is what makes the
// composite work at all. A solar raster is mostly sky, so a fit over the whole frame is dominated by
// pixels where BOTH exposures are reading their own noise; binning those on the tier's value and
// taking the median of the reference returns the reference's noise mean in every low bin, whatever
// the scene was. The mapping then comes back not merely flat at the faint end but sloped the wrong
// way — measured here, a tier eight times darker produced a fitted gain of 0.2 — and a slope that
// small reads, to the weighting downstream, as an exposure of infinite precision. The darker tier
// then takes over exactly the region it knows least about: on synthetic prominences it erased them.
//
// Below the disc the curve runs to the ORIGIN instead. That is the one boundary condition needing no
// measurement — no light is no light — it is the same argument normalisation makes about its own
// under-range (norm.go), and it leaves the slope out there at the exposure ratio, so the faint end
// is correctly judged as the noisier estimate that it is.
func measureTonalMap(tier, ref []float32, cov []bool, w, h int, l Limb) (tonalMap, bool) {
	ts := imgops.GaussianBlur(tier, w, h, bracketBlurSigma)
	rs := imgops.GaussianBlur(ref, w, h, bracketBlurSigma)

	type pair struct{ t, r float64 }
	var pairs []pair
	// A reference pixel without headroom carries no photometry: it is the ceiling, not a measurement,
	// and fitting through it would bend the whole mapping towards it. The knee is the cautious bound
	// and it is tried first; on a capture that legitimately exposes its disc into the top of the
	// range, it would throw away the disc itself, so the ceiling stands in rather than giving up.
	onDisc := onDiscMask(Limb{CX: l.CX, CY: l.CY, R: l.R})
	for _, cut := range []float64{bracketKnee, bracketCeiling} {
		pairs = pairs[:0]
		for i := range ts {
			if cov != nil && !cov[i] {
				continue
			}
			if !onDisc(i%w, i/w) || float64(rs[i]) >= cut {
				continue
			}
			pairs = append(pairs, pair{float64(ts[i]), float64(rs[i])})
		}
		if len(pairs) >= bracketMinPairs {
			break
		}
	}
	if len(pairs) < bracketMinPairs {
		return tonalMap{}, false
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].t < pairs[j].t })

	src := make([]float64, 0, bracketKnots)
	dst := make([]float64, 0, bracketKnots)
	per := len(pairs) / bracketKnots
	if per < 1 {
		per = 1
	}
	buf := make([]float64, 0, per)
	for start := 0; start < len(pairs); start += per {
		end := start + per
		if end > len(pairs) || len(pairs)-end < per/2 {
			end = len(pairs) // fold a short last bin into its neighbour rather than fitting a knot to it
		}
		buf = buf[:0]
		for _, p := range pairs[start:end] {
			buf = append(buf, p.r)
		}
		src = append(src, pairs[(start+end-1)/2].t)
		dst = append(dst, median(buf))
		if end == len(pairs) {
			break
		}
	}
	pts := monotoneKnots(append([]float64{0}, src...), append([]float64{0}, dst...))
	if len(pts) < 2 {
		return tonalMap{}, false
	}
	return tonalMap{pts: pts}, true
}

// monotoneKnots pairs the binned tier values with the reference values and keeps the strictly
// increasing run, dropping ties on either side — a flat stretch cannot be inverted, and forcing one
// puts a step in the mapping that shows up as banding on the smooth solar surface.
//
// It is buildLUT's rule without buildLUT's identity shortcut. Normalisation may skip a frame whose
// mapping turns out to be the identity, because rewriting it would only cost I/O; here the identity
// is a legitimate answer — two tiers really can sit at the same exposure — and dropping it would
// silently leave that tier out of the composite.
func monotoneKnots(src, dst []float64) []lutPoint {
	if len(src) != len(dst) {
		return nil
	}
	pts := make([]lutPoint, 0, len(src))
	for i := range src {
		if len(pts) > 0 && (src[i] <= pts[len(pts)-1].from || dst[i] <= pts[len(pts)-1].to) {
			continue
		}
		pts = append(pts, lutPoint{from: src[i], to: dst[i]})
	}
	return pts
}

// eval maps a tier value onto the reference scale.
func (m tonalMap) eval(x float64) float64 {
	p := m.pts
	switch {
	case x <= p[0].from:
		return p[0].to + (x-p[0].from)*segSlope(p[0], p[1])
	case x >= p[len(p)-1].from:
		last := p[len(p)-1]
		return last.to + (x-last.from)*segSlope(p[len(p)-2], last)
	default:
		i := m.segment(x)
		a, b := p[i], p[i+1]
		return a.to + (x-a.from)/(b.from-a.from)*(b.to-a.to)
	}
}

// slope is the local gain of the mapping — how many reference units one tier unit is worth here.
func (m tonalMap) slope(x float64) float64 {
	p := m.pts
	switch {
	case x <= p[0].from:
		return segSlope(p[0], p[1])
	case x >= p[len(p)-1].from:
		return segSlope(p[len(p)-2], p[len(p)-1])
	default:
		i := m.segment(x)
		return segSlope(p[i], p[i+1])
	}
}

// segment finds the knot interval holding x.
func (m tonalMap) segment(x float64) int {
	i := sort.Search(len(m.pts), func(k int) bool { return m.pts[k].from > x }) - 1
	return clampInt(i, 0, len(m.pts)-2)
}

// gain is how many reference units one tier unit is worth overall — the exposure ratio.
//
// It is the median over the UPPER knots, where the fit sits on the disc and both exposures carry
// real signal, rather than the ratio at any single knot: one knot is a measurement, and the figure
// is used to bound the weighting, which is not a place to be wrong once.
func (m tonalMap) gain() float64 {
	var r []float64
	for _, p := range m.pts[len(m.pts)/2:] {
		if p.from > 0 && p.to > 0 {
			r = append(r, p.to/p.from)
		}
	}
	if len(r) == 0 {
		return 1
	}
	return median(r)
}

// stops is how far below the reference this tier was exposed.
func (m tonalMap) stops() float64 {
	if g := m.gain(); g > 0 {
		return math.Log2(g)
	}
	return 0
}

// levelCurve is a quantity sampled against signal level, linear between knots and flat outside them.
type levelCurve struct{ at, val []float64 }

// eval reads the curve at a level.
func (c levelCurve) eval(x float64) float64 {
	if len(c.at) == 0 {
		return 0
	}
	if x <= c.at[0] {
		return c.val[0]
	}
	if x >= c.at[len(c.at)-1] {
		return c.val[len(c.val)-1]
	}
	i := clampInt(sort.SearchFloat64s(c.at, x)-1, 0, len(c.at)-2)
	span := c.at[i+1] - c.at[i]
	if span <= 0 {
		return c.val[i]
	}
	return c.val[i] + (x-c.at[i])/span*(c.val[i+1]-c.val[i])
}

// measureNoiseByLevel estimates a plane's noise as a function of signal level.
//
// The estimator is the MAD of the high-frequency residual — the plane minus a mild blur — taken
// within each level bin. MAD rather than a standard deviation because the residual is not only
// noise: filament edges, granulation and the limb all live at high frequency too. They are a
// minority of the pixels in any level bin while noise fills every one of them, so the median
// absolute deviation reads the noise and steps over the structure — the same argument that makes the
// radial profile a median rather than a mean.
//
// Level by level, rather than one figure for the frame, because a solar raster spans sky and disc
// and their noise differs by more than the thing being decided. One global sigma would judge the
// disc by the sky's quantisation and the sky by the disc's photon noise, and get both wrong.
func measureNoiseByLevel(p []float32, w, h int) levelCurve {
	blur := imgops.GaussianBlur(p, w, h, bracketBlurSigma)
	type sample struct{ level, resid float64 }
	s := make([]sample, len(p))
	for i := range p {
		s[i] = sample{float64(blur[i]), math.Abs(float64(p[i]) - float64(blur[i]))}
	}
	sort.Slice(s, func(i, j int) bool { return s[i].level < s[j].level })

	per := len(s) / bracketKnots
	if per < 16 {
		per = len(s)
	}
	out := levelCurve{}
	buf := make([]float64, 0, per)
	for start := 0; start < len(s); start += per {
		end := start + per
		if end > len(s) || len(s)-end < per/2 {
			end = len(s)
		}
		buf = buf[:0]
		for _, v := range s[start:end] {
			buf = append(buf, v.resid)
		}
		// 1.4826 converts a median absolute deviation into the Gaussian sigma the weights want.
		out.at = append(out.at, s[(start+end-1)/2].level)
		out.val = append(out.val, 1.4826*median(buf))
		if end == len(s) {
			break
		}
	}
	return out
}

// noiseFloor bounds the weights away from infinity where a curve measures no noise at all — a run of
// identical values, which a hard-clipped or quantised region really can produce. Without it one such
// pixel would be given unbounded confidence and would take the composite on its own.
func noiseFloor(c levelCurve) float64 {
	var pos []float64
	for _, v := range c.val {
		if v > 0 {
			pos = append(pos, v)
		}
	}
	if len(pos) == 0 {
		return 1e-6
	}
	return 0.05 * median(pos)
}

// headroom is a tier's claim on a pixel as its own value approaches the ceiling.
func headroom(v float64) float64 {
	if v <= bracketKnee {
		return 1
	}
	if v >= bracketCeiling {
		return 0
	}
	return 1 - smoothstep((v-bracketKnee)/(bracketCeiling-bracketKnee))
}

func sq(v float64) float64 { return v * v }
