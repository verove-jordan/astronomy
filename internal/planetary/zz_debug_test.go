package planetary

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// Debug harness for iterating on real lunar captures without a full pipeline run.
//
//	ASTRO_DBG_DIR=<dir of FITS> [ASTRO_DBG_MAX=40] [ASTRO_DBG_BEST=50] \
//	  go test ./internal/planetary/ -run TestZZDebugStack -v -timeout 3000s
//
// It reports the two numbers that say whether the stack is any good:
//   - detail ratio: the stacked master's noise-corrected detail over the best single frame's. The
//     pipeline's own acceptance gate wants ≥1.05; every real lunar run measured 0.02–0.17, which is
//     the signature of frames being summed at the wrong offsets.
//   - limb width: the 10–90 % rise distance across the sunlit limb, in pixels, for the master and
//     for the sharpest single frame. This is the "halo" directly — a stack of misaligned frames
//     smears the limb by exactly the spread of the misalignment, so a master whose limb is much
//     wider than a single frame's contains more than one Moon.
func TestZZDebugStack(t *testing.T) {
	dir := os.Getenv("ASTRO_DBG_DIR")
	if dir == "" {
		t.Skip("set ASTRO_DBG_DIR to a folder of FITS frames")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.fit*"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no frames in %s", dir)
	}
	sort.Strings(paths)
	if max := envInt("ASTRO_DBG_MAX", 0); max > 0 && len(paths) > max {
		paths = paths[:max]
	}
	best := envInt("ASTRO_DBG_BEST", 50)
	t.Logf("frames: %d from %s (keeping best %d%%)", len(paths), dir, best)

	scores := make([]float64, len(paths))
	for i, p := range paths {
		scores[i] = frameSharpness(p)
	}
	rejected := rejectLeastSharp(scores, best)
	rej := map[int]bool{}
	for _, idx := range rejected {
		rej[idx] = true
	}
	var keptPaths []string
	var keptScores []float64
	for i, p := range paths {
		if !rej[i+1] {
			keptPaths = append(keptPaths, p)
			keptScores = append(keptScores, scores[i])
		}
	}
	t.Logf("kept %d frames by sharpness", len(keptPaths))

	drift, w, h, err := trackDrift(context.Background(), keptPaths, nil)
	if err != nil {
		t.Fatalf("trackDrift: %v", err)
	}
	span := driftSpan(drift)
	t.Logf("frame %dx%d  drift span %.0f px (%.0f%% of the frame)", w, h, span, 100*span/float64(min(w, h)))

	out := os.Getenv("ASTRO_DBG_OUT")
	if out == "" {
		out = t.TempDir()
	} else {
		_ = os.MkdirAll(out, 0o755)
	}
	seeds := seedsFromTrajectory(drift, keptScores)
	if os.Getenv("ASTRO_DBG_NOSEED") != "" {
		seeds = nil // A/B: reproduce the pre-fix behaviour (brightness-centroid seed only)
		t.Log("SEEDS DISABLED (centroid-seeded, as before the fix)")
	}
	res, err := warpToSharpest(context.Background(), keptPaths, keptScores, out, "d", true, 1, 0, seeds, nil)
	if err != nil {
		t.Fatalf("warpToSharpest: %v", err)
	}
	t.Logf("aligned %d/%d frames (%d dropped by the correlation gate); %s", len(res.paths), len(keptPaths), res.dropped, res.note)

	master := filepath.Join(out, "master")
	if err := stackAligned(context.Background(), res.paths, res.cellSharp, DefaultOptions(), master, func(int, int) {}); err != nil {
		t.Fatalf("stack: %v", err)
	}

	bestFrame := 0.0
	for _, s := range keptScores {
		if s > bestFrame {
			bestFrame = s
		}
	}
	md := masterDetailNative(master+".fits", 1)
	t.Logf("DETAIL  master %.4g  best frame %.4g  ratio %.3f  (gate wants >= 1.05)", md, bestFrame, md/bestFrame)

	t.Logf("master written: %s.fits", master)
	// Previews for eyeballing: the whole master, and a 1:1 crop across the sunlit limb of both the
	// master and the sharpest input frame — where a second, offset copy of the Moon shows up.
	sharpest := keptPaths[argmax(keptScores)]
	_ = sharpest
	writePreview(t, master+".fits", filepath.Join(out, "master_full.png"), 0, 0, 0, 0, 4)
	if cx, cy, r, ok := discOf(master + ".fits"); ok {
		x0, y0 := int(cx-r-120), int(cy-160)
		writePreview(t, master+".fits", filepath.Join(out, "master_limb.png"), x0, y0, 700, 320, 1)
		writePreview(t, sharpest, filepath.Join(out, "frame_limb.png"), x0, y0, 700, 320, 1)
		t.Logf("limb crops at (%d,%d) 700x320", x0, y0)
	}
	mw, mok := limbWidth(master + ".fits")
	fw, fok := limbWidth(keptPaths[argmax(keptScores)])
	t.Logf("LIMB    master %.1f px (ok=%v)   sharpest frame %.1f px (ok=%v)   widening %.2fx",
		mw, mok, fw, fok, mw/math.Max(fw, 1e-9))
	if mok && fok && mw > 2.0*fw {
		t.Errorf("limb is %.2fx wider in the master than in one frame — the stack contains misaligned copies", mw/fw)
	}
}

// limbWidth measures the 10–90 % intensity rise across the sunlit limb, in ABSOLUTE pixels.
//
// The radial samples are stepped in pixels, not in fractions of the fitted radius: expressing the
// width as a share of r makes the answer scale with whatever radius the fit happened to return, and
// a master and a single frame do not fit identical radii. Measured against a ONE-frame stack, where
// the true widening is 1.00 by construction, the fractional version reported 1.15.
func limbWidth(path string) (float64, bool) {
	im, err := fits.ReadImage(path)
	if err != nil {
		return 0, false
	}
	cx, cy, r, ok := trackerDisc(im)
	if !ok {
		return 0, false
	}
	lum := lumaPlane(im)
	const step = 0.5 // px along the radius
	const reach = 60 // px either side of the fitted limb
	n := int(reach / step)
	var widths []float64
	for a := 0.0; a < 2*math.Pi; a += math.Pi / 180 {
		ca, sa := math.Cos(a), math.Sin(a)
		prof := make([]float64, 0, 2*n+1)
		for k := -n; k <= n; k++ {
			rr := r + float64(k)*step
			prof = append(prof, float64(bilinear(lum, cx+ca*rr, cy+sa*rr)))
		}
		inner, outer := prof[0], prof[len(prof)-1]
		if inner <= 0 || inner-outer < 0.05*math.Abs(inner) {
			continue // not a lit limb at this angle (terminator, or off-frame)
		}
		hi, lo := outer+0.9*(inner-outer), outer+0.1*(inner-outer)
		iHi, iLo := -1, -1
		for i, v := range prof {
			if iHi < 0 && v <= hi {
				iHi = i
			}
			if iLo < 0 && v <= lo {
				iLo = i
			}
		}
		if iHi < 0 || iLo < 0 || iLo <= iHi {
			continue
		}
		widths = append(widths, float64(iLo-iHi)*step)
	}
	if len(widths) < 20 {
		return 0, false
	}
	sort.Float64s(widths)
	return widths[len(widths)/2], true
}

func envInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		return def
	}
	return n
}

func discOf(path string) (cx, cy, r float64, ok bool) {
	im, err := fits.ReadImage(path)
	if err != nil {
		return 0, 0, 0, false
	}
	return trackerDisc(im)
}

// writePreview saves an autostretched grey PNG of (a crop of) a FITS image, decimated by step.
func writePreview(t *testing.T, src, dst string, x0, y0, cw, ch, step int) {
	t.Helper()
	im, err := fits.ReadImage(src)
	if err != nil {
		t.Logf("preview %s: %v", dst, err)
		return
	}
	if cw <= 0 || ch <= 0 {
		x0, y0, cw, ch = 0, 0, im.W, im.H
	}
	if x0 < 0 {
		x0 = 0
	}
	if y0 < 0 {
		y0 = 0
	}
	lum := lumaPlane(im)
	var vals []float64
	for y := y0; y < y0+ch && y < im.H; y += step {
		for x := x0; x < x0+cw && x < im.W; x += step {
			vals = append(vals, float64(lum.Pix[0][y*im.W+x]))
		}
	}
	if len(vals) == 0 {
		return
	}
	sv := append([]float64(nil), vals...)
	sort.Float64s(sv)
	lo, hi := sv[len(sv)/200], sv[len(sv)-1-len(sv)/200]
	if hi <= lo {
		hi = lo + 1e-6
	}
	w, h := (min(cw, im.W-x0))/step, (min(ch, im.H-y0))/step
	img := image.NewGray(image.Rect(0, 0, w, h))
	for j := 0; j < h; j++ {
		for i := 0; i < w; i++ {
			v := float64(lum.Pix[0][(y0+j*step)*im.W+(x0+i*step)])
			tt := (v - lo) / (hi - lo)
			if tt < 0 {
				tt = 0
			}
			if tt > 1 {
				tt = 1
			}
			img.SetGray(i, j, color.Gray{Y: uint8(tt * 255)})
		}
	}
	f, err := os.Create(dst)
	if err != nil {
		return
	}
	defer f.Close()
	_ = png.Encode(f, img)
}

// normalizedCopy writes a copy of src through the same normalize() the stack applies to its master,
// so limb widths are compared in the same units.
func normalizedCopy(t *testing.T, src, dir string) string {
	t.Helper()
	im, err := fits.ReadImage(src)
	if err != nil {
		return src
	}
	normalize(im, stackNormPct)
	dst := filepath.Join(dir, "ref_normalized.fits")
	if err := im.WriteFITS(dst); err != nil {
		return src
	}
	return dst
}

// TestZZDebugMosaic runs the FULL segmentation + canvas path on a folder of frames, so panel
// placement can be iterated on locally instead of through a 20-minute containerized job.
//
//	ASTRO_DBG_DIR=<dir of FITS> ASTRO_DBG_OUT=<dir> [ASTRO_DBG_BEST=50] \
//	  go test ./internal/planetary/ -run TestZZDebugMosaic -v -timeout 3000s
func TestZZDebugMosaic(t *testing.T) {
	dir := os.Getenv("ASTRO_DBG_DIR")
	out := os.Getenv("ASTRO_DBG_OUT")
	if dir == "" || out == "" {
		t.Skip("set ASTRO_DBG_DIR and ASTRO_DBG_OUT")
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.fit*"))
	if len(paths) == 0 {
		t.Fatalf("no frames in %s", dir)
	}
	sort.Strings(paths)
	_ = os.MkdirAll(out, 0o755)

	opts := DefaultOptions()
	opts.BestPercent = envInt("ASTRO_DBG_BEST", 50)
	opts.DrizzleScale = 1
	prog := newRunProgress(nil)
	master, _, kept, notes, err := stackChannel(context.Background(), nil, paths, "", out, "dbg", opts, prog, 90, nil)
	if err != nil {
		t.Fatalf("stackChannel: %v", err)
	}
	for _, n := range notes {
		if !strings.Contains(n, "alignment points") {
			t.Log("note:", n)
		}
	}
	t.Logf("stacked %d frames -> %s", kept, master)
	writePreview(t, master+".fits", filepath.Join(out, "canvas.png"), 0, 0, 0, 0, 4)
	t.Logf("canvas preview: %s", filepath.Join(out, "canvas.png"))
}
