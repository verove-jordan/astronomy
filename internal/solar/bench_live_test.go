package solar

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// bench_live_test.go is the one harness solar stacking changes are judged on.
//
// It exists because the measurements that matter were scattered across half a dozen opt-in tests,
// each answering one question on its own terms, and comparing two runs meant comparing numbers taken
// on different clips with different builds. That is not a small inconvenience: it is how a softer
// stack came to be recorded as the sharper one. Everything is therefore measured here, in one pass,
// on one clip, with one build.
//
// The four measurements, and why each is here:
//
//   - the FRAME PSF distribution against the STACK's PSF. Noise does not make an edge narrower, so
//     this is the only like-for-like comparison between a grainy single frame and a clean stack, and
//     it is the number that says whether registration is costing resolution.
//
//   - fine detail against radius (discContrast). A registration residual in scale or rotation is zero
//     at the centre and worst at the limb; a soft lens is soft everywhere. This tells them apart.
//
//   - the RENDERED radial profile across the limb (ringAmplitude). The two above are measured on the
//     linear master and are blind to everything the finish does, including a false bright ring.
//
//   - wall time, because every improvement here has been a trade against it.
//
//     ASTRO_SOLAR_FRAMES=work/sun_<stamp> ASTRO_SOLAR_SOURCE=08042026144830390 \
//     ASTRO_SOLAR_N=40,150 ASTRO_SOLAR_OUT=/tmp/bench \
//     go test ./internal/solar -run Bench_Live -v -timeout 4h
//
// ASTRO_SOLAR_MASTERS measures already-stacked masters instead of, or as well as, stacking here —
// which is how an existing run is brought onto the current build's scale.
func TestBench_Live(t *testing.T) {
	dir := os.Getenv("ASTRO_SOLAR_FRAMES")
	masters := splitList(os.Getenv("ASTRO_SOLAR_MASTERS"))
	if dir == "" && len(masters) == 0 {
		t.Skip("set ASTRO_SOLAR_FRAMES=<dir of ingested frames> and/or ASTRO_SOLAR_MASTERS=<a.fits,...>")
	}
	outDir := os.Getenv("ASTRO_SOLAR_OUT")
	if outDir != "" {
		require.NoError(t, os.MkdirAll(outDir, 0o755))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Hour)
	defer cancel()

	var prof []float64
	var prevName string
	for _, m := range masters {
		m = fromRepoRoot(m)
		im, err := fits.ReadImage(m)
		require.NoError(t, err)
		mono := firstPlane(im)
		l, ok := FitLimb(mono)
		require.True(t, ok, "%s: the master must carry a fittable limb", m)
		name := filepath.Base(filepath.Dir(m)) + "/" + filepath.Base(m)
		report(t, benchTarget{name: "MASTER " + name, im: mono, limb: l}, outDir)

		// The angle between consecutive masters. Two runs over the same target must agree on it, and
		// when they do not the difference is the one registration term the limb cannot supply: a circle
		// fit is rotation-blind, so rotation is correlated off disc structure and is the only place a
		// silent whole-degree error can enter a stack.
		if next := annulusProfile(mono, l); prof != nil {
			if deg, ok := CorrelateRotation(prof, next); ok {
				t.Logf("  ROTATION %s -> %s: %+.3f°  =  %.1f px at the limb",
					prevName, name, deg, math.Abs(deg)*math.Pi/180*l.R)
			} else {
				t.Logf("  ROTATION %s -> %s: could not be correlated", prevName, name)
			}
			prof = next
		} else {
			prof = next
		}
		prevName = name
	}
	if dir == "" {
		return
	}

	frames, sigmas := loadBenchFrames(t, fromRepoRoot(dir), os.Getenv("ASTRO_SOLAR_SOURCE"))
	require.NotEmpty(t, frames)
	sort.Float64s(sigmas)
	t.Logf("%d frames, %d measurable: PSF sigma best %.2f, p10 %.2f, MEDIAN %.2f, p90 %.2f, worst %.2f px",
		len(frames), len(sigmas), sigmas[0], sigmas[len(sigmas)/10], median(sigmas),
		sigmas[len(sigmas)*9/10], sigmas[len(sigmas)-1])
	// How much the fitted geometry itself moves, which is the ceiling on what any rigid registration
	// can remove. The radius matters as much as the centre and is easier to overlook: registration
	// pins it to one robust constant per clip, so whatever it really does frame to frame is left in
	// the stack as a RADIAL smear of the limb — the one place the point spread function is measured.
	logGeometrySpread(t, frames)

	// The median-sharpness frame is the yardstick every stack below is measured against: it is what
	// the pipeline was handed, so anything the stack resolves less well than this was lost by us.
	byScore := balancedByScore(frames)
	mid := byScore[len(byScore)/2]
	midIm, err := fits.ReadImage(mid.Path)
	require.NoError(t, err)
	report(t, benchTarget{name: "FRAME " + filepath.Base(mid.Path), im: firstPlane(midIm), limb: mid.Limb}, outDir)

	// The SAME frame through the single resample, and nothing else. This is the floor under every
	// stack below — no combination of frames can resolve better than one frame costs to register —
	// and it is measured on the same frame as the line above so the two differ by exactly one warp.
	// Reading that cost off a one-frame stack instead compares two DIFFERENT frames, because the
	// stack anchors on the sharpest frame and the line above reports the median one.
	side := CanonicalSide(mid.Limb.R, defaultCropMargin, 1)
	canonical := Limb{CX: float64(side-1) / 2, CY: float64(side-1) / 2, R: mid.Limb.R}
	report(t, benchTarget{name: "WARPED " + filepath.Base(mid.Path),
		im: Warp(firstPlane(midIm), SolveTransform(mid.Limb, canonical), side, 1), limb: canonical}, outDir)

	for _, v := range benchVariants(os.Getenv("ASTRO_SOLAR_VARIANTS")) {
		for _, n := range benchCounts(os.Getenv("ASTRO_SOLAR_N")) {
			if n > len(byScore) {
				continue
			}
			sel := append([]Frame(nil), byScore[:n]...)
			// Back into capture order: the stack reads frames sequentially and a rotation model is
			// fitted against time, so handing it a sharpness-ordered list measures something else.
			sort.SliceStable(sel, func(i, j int) bool { return sel[i].Index < sel[j].Index })
			start := time.Now()
			st, err := Stack(ctx, sel, v.opts)
			if err != nil {
				t.Logf("=== %s n=%d FAILED: %v", v.name, n, err)
				continue
			}
			for _, note := range st.Notes {
				t.Logf("    note: %s", note)
			}
			report(t, benchTarget{name: fmt.Sprintf("STACK %s n=%d", v.name, n),
				im: st.Master, limb: st.Limb, dur: time.Since(start)}, outDir)
		}
	}
}

// benchTarget is one image the bench reports on, whether it was stacked here or read off disk.
type benchTarget struct {
	name string
	im   *fits.Image // linear mono master or frame
	limb Limb
	dur  time.Duration
}

// report measures one target every way the bench knows and, when asked, writes the finished image
// plus 100% crops at the centre, mid-disc and limb.
func report(t *testing.T, tg benchTarget, outDir string) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "\n=== %s   (%dx%d, R=%.0f px, %.2f\"/px", tg.name, tg.im.W, tg.im.H, tg.limb.R,
		sunAngularDiameterArcsec/(2*tg.limb.R))
	if tg.dur > 0 {
		fmt.Fprintf(&b, ", %s", tg.dur.Round(time.Second))
	}
	fmt.Fprintf(&b, ")\n")

	psf := MeasurePSF(tg.im, tg.limb)
	if psf.OK {
		fmt.Fprintf(&b, "  limb PSF   sigma %.2f px  FWHM %.1f\"  overshoot %+.1f%%\n",
			psf.SigmaPx, psf.FWHMArcsec, 100*psf.Overshoot)
	} else {
		fmt.Fprintf(&b, "  limb PSF   NOT MEASURABLE\n")
	}

	// Absolute first, then as a fraction of this target's own centre. Both, because either alone
	// misleads: the fraction is what separates a registration residual (worst at the limb) from a soft
	// lens (soft everywhere), but comparing two variants BY that fraction says the wrong thing whenever
	// their centres differ — one variant read 84% against another's 107% while being the better of the
	// two at that radius in absolute terms.
	const contrastBins = 10
	c := discContrast(tg.im, tg.limb, contrastBins)
	fmt.Fprintf(&b, "  detail     centre %.5f", c[0])
	for _, i := range []int{contrastBins / 2, contrastBins - 1} {
		if c[0] > 0 {
			fmt.Fprintf(&b, " | %.2fR %.5f (%.0f%%)", (float64(i)+0.5)*0.95/contrastBins, c[i], 100*c[i]/c[0])
		}
	}
	// The prominences, separately, because they are the only thing a derotation error moves.
	fmt.Fprintf(&b, " | proms %.5f\n", promContrast(tg.im, tg.limb))

	fin, _, notes := ResolveFinish(tg.im, tg.limb, DefaultFinish())
	for _, n := range notes {
		fmt.Fprintf(&b, "  finish     %s\n", n)
	}
	img := Finish(tg.im, tg.limb, fin)
	prof := renderedRadial(img, tg.limb)
	amp, at := ringAmplitude(prof)
	fmt.Fprintf(&b, "  rendered   ring %+.4f at %.3fR   (0 = a limb that only ever falls)\n", amp, at)

	if os.Getenv("ASTRO_SOLAR_PROFILES") != "" {
		b.WriteString(formatProfile(c, "fine detail vs radius", func(i int) float64 {
			return (float64(i) + 0.5) * 0.95 / contrastBins
		}, 1))
		b.WriteString(formatProfile(prof, "rendered luminance across the limb", radiusOfBin, 4))
	}
	t.Log(b.String())

	if outDir == "" {
		return
	}
	base := sanitizeName(tg.name)
	require.NoError(t, WritePNG(img, filepath.Join(outDir, base+".png")))
	cx, cy := int(tg.limb.CX), int(tg.limb.CY)
	for _, crop := range []struct {
		tag  string
		x, y int
	}{
		{"centre", cx, cy},
		{"mid", cx + int(0.55*tg.limb.R), cy},
		{"limb", cx + int(0.95*tg.limb.R), cy},
	} {
		require.NoError(t, WritePNG(cropImage(img, crop.x, crop.y, 200),
			filepath.Join(outDir, base+"_"+crop.tag+".png")))
	}
}

// benchVariant is one registration configuration to compare.
type benchVariant struct {
	name string
	opts StackOptions
}

// benchVariants is the comparison set, filtered by name when ASTRO_SOLAR_VARIANTS asks for a subset.
func benchVariants(sel string) []benchVariant {
	all := []benchVariant{
		{"rigid", StackOptions{}},
		{"ap", StackOptions{APAlign: true}},
		{"norefine", StackOptions{NoRefine: true}},
		{"ap-norefine", StackOptions{APAlign: true, NoRefine: true}},
		{"perframe-scale", StackOptions{ScalePerFrame: true}},
		{"norot", StackOptions{NoDerotate: true}},
		{"perframe-rot", StackOptions{RotationPerFrame: true}},
	}
	want := splitList(sel)
	if len(want) == 0 {
		return all[:2]
	}
	var out []benchVariant
	for _, w := range want {
		for _, v := range all {
			if v.name == w {
				out = append(out, v)
			}
		}
	}
	return out
}

// benchCounts is how many frames each variant is stacked from.
func benchCounts(sel string) []int {
	out := []int{}
	for _, s := range splitList(sel) {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			out = append(out, n)
		}
	}
	if len(out) == 0 {
		return []int{40, 150}
	}
	return out
}

// loadBenchFrames reads every ingested frame, optionally keeping one clip, and measures each one.
func loadBenchFrames(t *testing.T, dir, source string) ([]Frame, []float64) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(dir, "*.fits"))
	require.NoError(t, err)
	sort.Strings(paths)
	var frames []Frame
	var sigmas []float64
	for _, p := range paths {
		base := filepath.Base(p)
		if source != "" && !strings.Contains(base, source) {
			continue
		}
		im, err := fits.ReadImage(p)
		require.NoError(t, err)
		mono := firstPlane(im)
		l, ok := FitLimb(mono)
		if !ok {
			continue
		}
		// Source has to be recovered from the filename: ingest names each frame
		// "<sanitised source>_NNNNN.fits", and the balanced selection groups on it. Leaving it empty
		// collapses every clip into one group, which is how a two-clip comparison quietly turns into a
		// one-clip one that never exercises the registration between them.
		src := base
		if i := strings.LastIndex(base, "_"); i > 0 {
			src = base[:i]
		}
		frames = append(frames, Frame{Path: p, Source: src, Limb: l, Index: len(frames), Score: FrameSharpness(mono, l)})
		if psf := MeasurePSF(mono, l); psf.OK {
			sigmas = append(sigmas, psf.SigmaPx)
		}
	}
	return frames, sigmas
}

// logGeometrySpread reports how far the fitted centre and radius wander across the clip, each after
// removing a straight-line trend in time so that genuine drift is not counted as scatter.
func logGeometrySpread(t *testing.T, frames []Frame) {
	t.Helper()
	spread := func(name string, get func(Frame) float64) {
		v := make([]float64, len(frames))
		for i, f := range frames {
			v[i] = get(f)
		}
		var resid []float64
		a, b, ok := benchTrend(v)
		for i, x := range v {
			if ok {
				x -= a + b*float64(i)
			} else {
				x -= median(v)
			}
			resid = append(resid, math.Abs(x))
		}
		t.Logf("  fitted %-7s spread %.2f px about its trend (worst %.2f)", name,
			1.4826*median(resid), maxOf(resid))
	}
	spread("cx", func(f Frame) float64 { return f.Limb.CX })
	spread("cy", func(f Frame) float64 { return f.Limb.CY })
	spread("radius", func(f Frame) float64 { return f.Limb.R })
}

// benchTrend is a least-squares line through a series against its index.
func benchTrend(v []float64) (a, b float64, ok bool) {
	n := float64(len(v))
	if n < 3 {
		return 0, 0, false
	}
	var sx, sy, sxx, sxy float64
	for i, y := range v {
		x := float64(i)
		sx, sy, sxx, sxy = sx+x, sy+y, sxx+x*x, sxy+x*y
	}
	den := n*sxx - sx*sx
	if den == 0 {
		return 0, 0, false
	}
	b = (n*sxy - sx*sy) / den
	return (sy - b*sx) / n, b, true
}

// balancedByScore ranks frames by sharpness WITHIN each clip and then interleaves, so "the best n"
// is drawn evenly from every clip — which is what ingest does, and what a combined stack therefore
// has to cope with. Ranking globally instead quietly selects one clip and never exercises the
// registration between them at all.
func balancedByScore(frames []Frame) []Frame {
	bySrc := map[string][]Frame{}
	var srcs []string
	for _, f := range frames {
		if _, seen := bySrc[f.Source]; !seen {
			srcs = append(srcs, f.Source)
		}
		bySrc[f.Source] = append(bySrc[f.Source], f)
	}
	sort.Strings(srcs)
	for _, k := range srcs {
		v := bySrc[k]
		sort.SliceStable(v, func(i, j int) bool { return v[i].Score > v[j].Score })
	}
	var out []Frame
	for round := 0; ; round++ {
		added := false
		for _, k := range srcs {
			if round < len(bySrc[k]) {
				out = append(out, bySrc[k][round])
				added = true
			}
		}
		if !added {
			return out
		}
	}
}

// fromRepoRoot resolves a relative path against the repository root rather than the package
// directory a test happens to run in, so the paths in the invocation above are the ones a shell in
// the repo would use.
func fromRepoRoot(p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join("..", "..", p)
}

// splitList parses a comma-separated environment value.
func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
