package transient

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/trail"
)

// Line-validation tuning. A REAL satellite/plane trail lights its corridor in ONE frame — or, for a
// reused sky track (geostationary belt, satellite trains), lights each stretch of the corridor in a
// FEW frames; fixed-pattern walking noise (uncalibrated darks) lights the SAME stretches in a large
// fraction of frames. Validation therefore compares candidate and witnesses window-by-window along
// the corridor (see corridorWindow): the old whole-corridor mean rejected every candidate on a
// belt-reused track as "fixed pattern" (job #316: 261 rejections, trail stacked through).
const (
	lineFixedPatternFrac = 0.30 // median lit-window witness fraction ≥ this ⇒ fixed pattern
	lineWingK            = 0.5  // corridor significance uses half the per-pixel k, to catch the faint wings
	minCorridorPx        = 24   // fewer lit corridor pixels than this ⇒ not a real trail extent
	lineWindowT          = 64   // corridor window length along the line (t-steps)
	lineWindowMinPx      = 32   // a window needs this many non-zero pixels to be judged at all
	// lineWindowMinWitness is the minimum observing witnesses for a window's fraction to count.
	// MinFrames = 5 ⇒ the smallest valid run has 4 witnesses; 3 keeps those runs validatable while
	// still refusing to judge a window most witnesses cannot see (zero-fill borders).
	lineWindowMinWitness = 3
	// lineSharedRepairFrac: a recurring-corridor window lit in ≥ this fraction of the basis has a
	// contaminated cross-frame median there — repair it from local background instead.
	lineSharedRepairFrac = 0.45
	skySubsampleMax      = 200000
)

// MaskCrossFrameValidated masks satellite/aircraft trails from a set of REGISTERED frames (mutated in
// place) with three passes: (1) the blanket per-pixel positive-outlier replacement (cosmic rays, hot
// pixels, and a trail's bright core), (2) a LINE pass that paints the faint sub-threshold WINGS a
// per-pixel threshold misses — each candidate validated window-by-window against the other frames so
// walking noise is never repainted (fixed-pattern test) while marching/recurring real trails are —
// and (3) a recurring-corridor pass that finds belt-reused tracks on the coverage-aware MEAN residual
// (where they sum coherently, exactly as they would in the final stack) and repairs each frame's lit
// windows. Geostationary streaks (present in the cross-frame MEDIAN) are repaired from local
// background. Fewer than MinFrames frames or k ≤ 0 → no-op. The Report carries per-frame + aggregate
// counts.
// satCeil (optional, aligned with frames; nil = off) enables the saturated-core repair FIRST: each
// frame's at-ceiling pixels are replaced from the sub-ceiling median (satmask.go), so the plateau
// bias never reaches the median/MAD planes or the stack.
func MaskCrossFrameValidated(frames []*fits.Image, k float64, satCeil []float32) (*Report, error) {
	n := len(frames)
	if n < MinFrames || (k <= 0 && len(satCeil) == 0) {
		return &Report{}, nil
	}
	w, h, c := frames[0].W, frames[0].H, frames[0].C
	if err := checkDims(frames, w, h, c); err != nil {
		return nil, err
	}
	satPx := repairSaturatedAll(frames, satCeil, w, h, c)
	if k <= 0 { // saturation-only invocation: report the repairs, skip the trail machinery
		rep := &Report{width: w, height: h}
		for fi := range frames {
			rep.PerFrame = append(rep.PerFrame, FrameReport{Index: fi + 1, SatPx: intAt(satPx, fi)})
		}
		return rep, nil
	}
	med, sig := medianMADPlanes(frames, w, h, c)
	resid := make([][]float32, n)
	sky := make([]float64, n)
	acc := newMeanAccum(w, h)
	for i, f := range frames {
		resid[i] = residualPlane(f, med, w, h, c)
		sky[i] = residualSky(resid[i])
		acc.add(resid[i], f)
	}
	medSegs := detectMedianTrails(med, w, h)
	recs := buildRecurring(acc.plane(), medSegs, resid, sky, k, w, h)

	rep := &Report{width: w, height: h, geo: len(medSegs), recurring: len(recs)}
	for fi, f := range frames {
		fr := FrameReport{Index: fi + 1, SatPx: intAt(satPx, fi)}
		fr.MaskedPx += blanketMask(f, med, sig, k, w, h, c) // pass 1: per-pixel outliers (unchanged)
		maskFrameLines(f, med, resid, sky, fi, k, w, h, c, &fr, rep)
		maskFrameRecurring(f, resid[fi], sky[fi], med, recs, w, h, c, &fr)
		rep.PerFrame = append(rep.PerFrame, fr)
	}
	maskGeostationary(frames, med, medSegs, c, rep)
	return rep, nil
}

// intAt is a bounds-safe lookup into an optional per-frame count slice.
func intAt(vals []int, i int) int {
	if i < 0 || i >= len(vals) {
		return 0
	}
	return vals[i]
}

// maskFrameLines runs the validated line pass for frame fi: detect candidate segments on its residual
// and paint the lit runs of each candidate that passes the windowed fixed-pattern validation.
// DetectSegments' own per-plane ceiling (4) bounds the candidates; every one is judged individually —
// the old "skip the whole frame when the detector saturates" guard predated per-candidate validation
// and leaked real trails on frames that legitimately carry several (job #316: 4 frames skipped).
func maskFrameLines(f *fits.Image, med [][]float32, resid [][]float32, sky []float64, fi int,
	k float64, w, h, c int, fr *FrameReport, rep *Report) {
	segs := trail.DetectSegments(resid[fi], w, h, trail.DefaultParams(k))
	isZero := frameZeroFn(f)
	for _, s := range segs {
		runs, ok := validTrailBasis(s, resid[fi], sky[fi], resid, sky, fi, w, h, isZero)
		if !ok {
			rep.rejected++
			continue
		}
		fr.Segments++
		for _, sub := range runs {
			for ch := 0; ch < c; ch++ {
				fr.MaskedPx += trail.ApplySwathMedian(f.Pix[ch], med[ch], w, h, sub)
			}
		}
	}
}

// maskGeostationary repairs streaks that sit on the same sky pixels in a majority of frames (so they
// survive into the cross-frame median) from local background in every frame.
func maskGeostationary(frames []*fits.Image, med [][]float32, medSegs []trail.Segment, c int, rep *Report) {
	for si, s := range medSegs {
		for fi, f := range frames {
			for ch := 0; ch < c; ch++ {
				rep.PerFrame[fi].MaskedPx += trail.ApplySwathLocalBG(f.Pix[ch], f.W, f.H, s, seedFor(si, ch))
			}
		}
	}
}

// validTrailBasis validates one candidate segment against an explicit BASIS (a subset of the sequence
// in the streamed variant; the whole sequence in the in-memory one, where selfIdx skips the frame's
// own residual) and, when it passes, returns the extent-clipped sub-segments to paint (the frame's lit
// windows ±1 of margin). A candidate passes when (1) it is significant over ≥1 corridor window of its
// own frame (zero-fill pixels excluded), (2) those lit windows total ≥ minCorridorPx pixels, and
// (3) the median lit-window witness fraction stays under lineFixedPatternFrac — windows most
// witnesses cannot judge (too few observing) are skipped, and a candidate with NO judgeable window is
// rejected (walking-noise safety beats an unverifiable paint).
func validTrailBasis(s trail.Segment, own []float32, ownSky float64,
	basisResid [][]float32, basisSky []float64, selfIdx, w, h int, isZero func(int) bool) ([]trail.Segment, bool) {
	wins := corridorWindows(s, w, h)
	lit, litPx := litWindows(wins, own, ownSky, isZero)
	if len(lit) == 0 || litPx < minCorridorPx {
		return nil, false
	}
	frac, ok := fixedPatternFrac(wins, lit, basisResid, basisSky, selfIdx)
	if !ok || frac >= lineFixedPatternFrac {
		return nil, false
	}
	return paintRuns(s, wins, lit), true
}

// MaskCrossFrameStreamed is MaskCrossFrameValidated under a memory budget: only basisMax
// evenly-spaced frames are held resident (their residuals are the median/MAD source, the
// fixed-pattern validation basis AND the recurring-corridor mean — the same discriminators over
// fewer witnesses; keep the basis at a few dozen frames so the elevated-fraction estimate has
// resolution near the 30% threshold), and every frame is then read, masked and written back ONE AT
// A TIME. Peak memory is ~(basis + 6) planes instead of ~2·n — the difference between a 129-sub
// 16 MP merge masking in ~3 GiB and OOM-killing a containerized engine at ~30 GiB. Semantics per
// frame are identical to the in-memory pass (same blanket, same windowed line validation, same
// recurring + geostationary repairs; the basis covering every frame reproduces it bit for bit).
// satCeil mirrors MaskCrossFrameValidated's: the clean medians come from the BASIS subset (like
// every other cross-frame statistic here), each frame is then repaired as it streams through —
// BEFORE its residual is taken, exactly like the in-memory pass repairs before the median planes,
// so a full basis reproduces it bit for bit.
func MaskCrossFrameStreamed(paths []string, k float64, basisMax int, satCeil []float32) (*Report, error) {
	n := len(paths)
	if n < MinFrames || (k <= 0 && len(satCeil) == 0) {
		return &Report{}, nil
	}
	if basisMax < MinFrames {
		basisMax = MinFrames
	}
	basisIdx := evenIndices(n, basisMax)
	med, sig, bresid, bsky, meanResid, cleanMed, w, h, c, err := readStreamBasis(paths, basisIdx, satCeil)
	if err != nil {
		return nil, err
	}
	var medSegs []trail.Segment
	var recs []recurringCorridor
	if k > 0 {
		medSegs = detectMedianTrails(med, w, h)
		recs = buildRecurring(meanResid, medSegs, bresid, bsky, k, w, h)
	}
	selfIdx := make(map[int]int, len(basisIdx)) // sequence index → basis position (skip-self)
	for bi, fi := range basisIdx {
		selfIdx[fi] = bi
	}

	rep := &Report{width: w, height: h, geo: len(medSegs), recurring: len(recs)}
	for fi, p := range paths {
		f, err := fits.ReadImage(p)
		if err != nil {
			return rep, err
		}
		if f.W != w || f.H != h || f.C != c {
			return rep, fmt.Errorf("frame %d is %dx%dx%d, want %dx%dx%d", fi, f.W, f.H, f.C, w, h, c)
		}
		fr := FrameReport{Index: fi + 1, SatPx: repairSaturated(f, ceilAt(satCeil, fi), cleanMed)}
		if k > 0 {
			// The frame's residual is taken BEFORE the blanket pass mutates its pixels — exactly like
			// the in-memory pass, whose residual planes are precomputed (the line detector needs the
			// streak's bright core, which the blanket pass flattens).
			own := residualPlane(f, med, w, h, c)
			ownSky := residualSky(own)
			fr.MaskedPx += blanketMask(f, med, sig, k, w, h, c)
			streamFrameLines(f, own, ownSky, med, bresid, bsky, selfOr(selfIdx, fi), k, w, h, c, &fr, rep)
			maskFrameRecurring(f, own, ownSky, med, recs, w, h, c, &fr)
			for si, s := range medSegs {
				for ch := 0; ch < c; ch++ {
					fr.MaskedPx += trail.ApplySwathLocalBG(f.Pix[ch], w, h, s, seedFor(si, ch))
				}
			}
		}
		rep.PerFrame = append(rep.PerFrame, fr)
		if fr.MaskedPx > 0 || fr.SatPx > 0 {
			if err := f.OverwriteData(p); err != nil {
				return rep, fmt.Errorf("write %s: %w", filepath.Base(p), err)
			}
		}
	}
	return rep, nil
}

// readStreamBasis loads the basis frames, repairs their saturated cores (so the plateau bias never
// reaches the statistics — mirroring the in-memory order), derives the median/MAD planes, the basis
// residuals + sky noises, the coverage-aware mean residual and the sub-ceiling clean medians, then
// releases the basis pixel data (only the residual planes stay resident).
func readStreamBasis(paths []string, basisIdx []int, satCeil []float32) (med, sig, bresid [][]float32, bsky []float64,
	meanResid []float32, cleanMed []map[int]float32, w, h, c int, err error) {
	basis := make([]*fits.Image, len(basisIdx))
	for bi, fi := range basisIdx {
		im, rerr := fits.ReadImage(paths[fi])
		if rerr != nil {
			return nil, nil, nil, nil, nil, nil, 0, 0, 0, fmt.Errorf("read %s: %w", filepath.Base(paths[fi]), rerr)
		}
		basis[bi] = im
	}
	w, h, c = basis[0].W, basis[0].H, basis[0].C
	if err = checkDims(basis, w, h, c); err != nil {
		return nil, nil, nil, nil, nil, nil, 0, 0, 0, err
	}
	if len(satCeil) > 0 {
		basisCeil := make([]float32, len(basisIdx))
		for bi, fi := range basisIdx {
			basisCeil[bi] = ceilAt(satCeil, fi)
		}
		cleanMed = cleanMedianPlanes(basis, basisCeil, w, h, c)
		for bi, f := range basis {
			repairSaturated(f, basisCeil[bi], cleanMed) // counts reported by the per-frame stream pass
		}
	}
	med, sig = medianMADPlanes(basis, w, h, c)
	bresid = make([][]float32, len(basis))
	bsky = make([]float64, len(basis))
	acc := newMeanAccum(w, h)
	for i, f := range basis {
		bresid[i] = residualPlane(f, med, w, h, c)
		bsky[i] = residualSky(bresid[i])
		acc.add(bresid[i], f)
		basis[i] = nil // release the frame's pixels; only its residual is needed from here on
	}
	return med, sig, bresid, bsky, acc.plane(), cleanMed, w, h, c, nil
}

// streamFrameLines is maskFrameLines for a streamed frame: candidates are detected on its
// pre-blanket residual (own) and validated against the resident basis (selfIdx ≥ 0 skips the
// frame's own basis residual when it happens to be part of it).
func streamFrameLines(f *fits.Image, own []float32, ownSky float64, med, bresid [][]float32, bsky []float64,
	selfIdx int, k float64, w, h, c int, fr *FrameReport, rep *Report) {
	segs := trail.DetectSegments(own, w, h, trail.DefaultParams(k))
	isZero := frameZeroFn(f)
	for _, s := range segs {
		runs, ok := validTrailBasis(s, own, ownSky, bresid, bsky, selfIdx, w, h, isZero)
		if !ok {
			rep.rejected++
			continue
		}
		fr.Segments++
		for _, sub := range runs {
			for ch := 0; ch < c; ch++ {
				fr.MaskedPx += trail.ApplySwathMedian(f.Pix[ch], med[ch], w, h, sub)
			}
		}
	}
}

// selfOr returns the basis position of sequence frame fi, or -1 when fi is not in the basis.
func selfOr(selfIdx map[int]int, fi int) int {
	if bi, ok := selfIdx[fi]; ok {
		return bi
	}
	return -1
}

// residualSky estimates a residual plane's robust sky noise (1.4826·MAD) on a capped subsample — the
// trail is a tiny fraction of the plane, so the subsample MAD tracks the sky, not the streak.
func residualSky(plane []float32) float64 {
	step := 1
	if len(plane) > skySubsampleMax {
		step = len(plane) / skySubsampleMax
	}
	sub := make([]float64, 0, len(plane)/step+1)
	for i := 0; i < len(plane); i += step {
		sub = append(sub, float64(plane[i]))
	}
	m := median(sub)
	for i := range sub {
		sub[i] = math.Abs(sub[i] - m)
	}
	sort.Float64s(sub)
	return 1.4826 * sub[len(sub)/2]
}
