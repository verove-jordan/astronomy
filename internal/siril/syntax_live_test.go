package siril_test

// Live contract tests against the host siril-cli: every Siril syntax fact the pipeline's generated
// scripts rely on is exercised for real — the adaptive rejection tokens and their parameter
// semantics, set32bits, `calibrate -cc=bpm` (acceptance AND that it actually repairs), find_hot's
// .lst coordinate convention (which WriteSirilBPM must mirror), and the .seq R-line homography
// format grade.ParseSeq reads. Skipped when no siril-cli is installed (e.g. Linux CI).

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/grade"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/stackalg"
	"golang.org/x/image/tiff"
)

const defaultSirilBin = "/Applications/Siril.app/Contents/MacOS/siril-cli"

func sirilRunner(t *testing.T) *siril.Runner {
	t.Helper()
	bin := os.Getenv("SIRIL_BIN")
	if bin == "" {
		bin = defaultSirilBin
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("no siril-cli at %s", bin)
	}
	return siril.New(bin, siril.Limits{})
}

// lcg is a deterministic pseudo-random source (live tests must be reproducible run to run).
type lcg struct{ s uint64 }

func (l *lcg) next() float64 {
	l.s = l.s*6364136223846793005 + 1442695040888963407
	return float64(l.s>>11) / float64(1<<53)
}

// writeMono writes a WxH mono float FITS built by fill(x,y) (fits.WriteFITS row order is TOP-DOWN).
func writeMono(t *testing.T, path string, w, h int, fill func(x, y int) float32) {
	t.Helper()
	im := fits.NewImage(w, h, 1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.Pix[0][y*w+x] = fill(x, y)
		}
	}
	require.NoError(t, im.WriteFITS(path))
}

func readPixel(t *testing.T, path string, x, y int) float32 {
	t.Helper()
	im, err := fits.ReadImage(path)
	require.NoError(t, err)
	return im.Pix[0][y*im.W+x]
}

// writeNoiseSeq writes n small noisy frames (with a few planted outliers) for stack syntax checks.
func writeNoiseSeq(t *testing.T, dir string, n int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	rng := &lcg{s: 999}
	for i := 1; i <= n; i++ {
		writeMono(t, filepath.Join(dir, fmt.Sprintf("d_%03d.fits", i)), 64, 64, func(x, y int) float32 {
			v := float32(0.01 + (rng.next()-0.5)*0.004)
			if i == 5 && x%17 == 3 {
				v += 0.5 // outliers in one frame so rejection has work to do
			}
			return v
		})
	}
}

// TestSirilLive_RejectionTokens locks the exact rejection clauses siril.Rejection emits: each must
// be accepted AND select the intended algorithm (checked via Siril's own summary log).
func TestSirilLive_RejectionTokens(t *testing.T) {
	r := sirilRunner(t)
	for _, tc := range []struct {
		name, clause, logNeedle string
	}{
		{"winsorized", siril.Rejection(30), "winsorized sigma clipping"},
		{"gesd", siril.Rejection(60), "GESDT clipping"},
		{"percentile", siril.Rejection(6), "percentile clipping"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), tc.name)
			writeNoiseSeq(t, dir, 12)
			script := fmt.Sprintf("requires 1.2.0\nsetext fits\nlink cal -out=.\nstack cal %s -nonorm -out=m\n", tc.clause)
			res, err := r.Run(context.Background(), dir, script, nil)
			require.NoError(t, err, "Siril rejected %q", tc.clause)
			assert.Contains(t, res.Log, tc.logNeedle, "the clause must select the intended algorithm")
			require.FileExists(t, filepath.Join(dir, "m.fits"))
		})
	}
}

// TestSirilLive_ConvertStacksTIFF locks the StackMasterScript contract that a 16-bit TIFF
// calibration pool (SharpCap lunar darks) stacks via `convert` exactly like FITS — the reason the
// master scripts use convert, not link (which only accepts FITS and fails with "generic error").
func TestSirilLive_ConvertStacksTIFF(t *testing.T) {
	r := sirilRunner(t)
	dir := t.TempDir()
	rng := &lcg{s: 31337}
	for i := 1; i <= 10; i++ {
		img := image.NewGray16(image.Rect(0, 0, 64, 64))
		for y := 0; y < 64; y++ {
			for x := 0; x < 64; x++ {
				img.SetGray16(x, y, color.Gray16{Y: uint16(600 + rng.next()*100)})
			}
		}
		f, err := os.Create(filepath.Join(dir, fmt.Sprintf("d_%03d.tif", i)))
		require.NoError(t, err)
		require.NoError(t, tiff.Encode(f, img, nil))
		require.NoError(t, f.Close())
	}
	_, err := r.Run(context.Background(), dir, siril.StackMasterScript("cal", filepath.Join(dir, "m"), 10, stackalg.DefaultMasters().Dark), nil)
	require.NoError(t, err, "a pure-TIFF master pool must stack")
	require.FileExists(t, filepath.Join(dir, "m.fits"))
}

// TestSirilLive_Set32Bits locks the set32bits command and the -32 output contract ReadPlaneBand needs.
func TestSirilLive_Set32Bits(t *testing.T) {
	r := sirilRunner(t)
	dir := t.TempDir()
	writeNoiseSeq(t, dir, 10)
	_, err := r.Run(context.Background(), dir,
		"requires 1.2.0\nsetext fits\nset32bits\nlink cal -out=.\nstack cal rej winsorized 3 3 -nonorm -out=m\n", nil)
	require.NoError(t, err)
	f, err := fits.Open(filepath.Join(dir, "m.fits"))
	require.NoError(t, err)
	bp, _ := f.Header.Int("BITPIX")
	assert.Equal(t, int64(-32), bp)
}

// TestSirilLive_FindHotConventionAndBPMRepair locks two coupled contracts:
//  1. find_hot's .lst coordinate convention — "P x y H|C", 0-based, y flipped on our TOP-DOWN
//     files — which calib.WriteSirilBPM mirrors;
//  2. `calibrate -cc=bpm <file>` is accepted and REPAIRS the listed pixel: a flickering hot pixel
//     leaves a +0.05 residual after dark subtraction alone, and ~background with the map applied.
func TestSirilLive_FindHotConventionAndBPMRepair(t *testing.T) {
	r := sirilRunner(t)
	ctx := context.Background()
	dir := t.TempDir()
	const w, h = 64, 64
	hotX, hotY := 10, 20   // top-down coordinates
	coldX, coldY := 40, 50 // dead pixel

	rng := &lcg{s: 12345}
	noise := func() float32 { return float32(0.01 + (rng.next()-0.5)*0.004) }
	writeMono(t, filepath.Join(dir, "master_dark.fits"), w, h, func(x, y int) float32 {
		switch {
		case x == hotX && y == hotY:
			return 0.9
		case x == coldX && y == coldY:
			return 0.0
		default:
			return noise()
		}
	})
	_, err := r.Run(ctx, dir, "requires 1.2.0\nsetext fits\nload master_dark\nfind_hot defects 3 3\n", nil)
	require.NoError(t, err)
	lst, err := os.ReadFile(filepath.Join(dir, "defects.lst"))
	require.NoError(t, err)
	assert.Contains(t, string(lst), fmt.Sprintf("P %d %d H", hotX, h-1-hotY), "hot pixel, y flipped (TOP-DOWN source)")
	assert.Contains(t, string(lst), fmt.Sprintf("P %d %d C", coldX, h-1-coldY), "cold pixel, y flipped")

	// Lights whose hot pixel flickers ABOVE the master (0.95 vs 0.9): dark subtraction alone
	// leaves +0.05; the BPM repair replaces it with the ~0 neighborhood.
	calDir := filepath.Join(dir, "cal")
	require.NoError(t, os.MkdirAll(calDir, 0o755))
	rng2 := &lcg{s: 4242}
	for i := 1; i <= 3; i++ {
		writeMono(t, filepath.Join(calDir, fmt.Sprintf("l_%03d.fits", i)), w, h, func(x, y int) float32 {
			if x == hotX && y == hotY {
				return 0.95
			}
			return float32(0.012 + (rng2.next()-0.5)*0.004)
		})
	}
	masterAbs := filepath.Join(dir, "master_dark")
	_, err = r.Run(ctx, calDir, fmt.Sprintf(
		"requires 1.2.0\nsetext fits\nset32bits\nlink light -out=.\ncalibrate light -dark=%s -prefix=ctl_\n", masterAbs), nil)
	require.NoError(t, err)
	ctl := readPixel(t, filepath.Join(calDir, "ctl_light_00001.fits"), hotX, hotY)
	require.InDelta(t, 0.05, ctl, 0.02, "without the map the flicker residual survives dark subtraction")

	res, err := r.Run(ctx, calDir, fmt.Sprintf(
		"requires 1.2.0\nsetext fits\nset32bits\nlink light -out=.\ncalibrate light -dark=%s -cc=bpm %s -prefix=pp_\n",
		masterAbs, filepath.Join(dir, "defects.lst")), nil)
	require.NoError(t, err, "-cc=bpm <file> must be accepted")
	assert.Contains(t, res.Log, "Bad Pixel Map")
	got := readPixel(t, filepath.Join(calDir, "pp_light_00001.fits"), hotX, hotY)
	assert.Less(t, math.Abs(float64(got)), 0.02, "the listed pixel must be repaired to ~background, got %v", got)
}

// writeStarFieldSeq writes nFrames registrable star-field frames (one synthetic sky drifting by
// dx,dy px per frame) plus the link/register/seqapplyreg preamble their tests share.
func writeStarFieldSeq(t *testing.T, dir string, nFrames int, dx, dy float64) {
	t.Helper()
	const rw, rh, nStars = 256, 256, 40
	srng := &lcg{s: 777}
	type star struct{ x, y, a float64 }
	stars := make([]star, nStars)
	for i := range stars {
		stars[i] = star{20 + srng.next()*(rw-40), 20 + srng.next()*(rh-40), 0.3 + srng.next()*0.5}
	}
	psf := func(d2 float64) float64 { return 1.0 / (1.0 + d2/(2*1.8*1.8)) }
	for i := 0; i < nFrames; i++ {
		ox, oy := dx*float64(i), dy*float64(i)
		frng := &lcg{s: uint64(1000 + i)}
		writeMono(t, filepath.Join(dir, fmt.Sprintf("s_%03d.fits", i+1)), rw, rh, func(x, y int) float32 {
			v := 0.005 + (frng.next()-0.5)*0.002
			for _, s := range stars {
				ddx, ddy := float64(x)-(s.x+ox), float64(y)-(s.y+oy)
				if d2 := ddx*ddx + ddy*ddy; d2 < 64 {
					v += s.a * psf(d2)
				}
			}
			return float32(v)
		})
	}
}

const registerPreamble = "requires 1.2.0\nsetext fits\nset32bits\nlink light -out=.\nregister light -2pass\nseqapplyreg light\n"

// TestSirilLive_SeqHomographyShifts locks the .seq R-line format grade.ParseSeq reads: register a
// synthetic star field with a planted (2,1) px/frame drift and require the parsed consecutive
// shift deltas to reproduce it (signs are Siril's own convention; magnitudes are what matter).
func TestSirilLive_SeqHomographyShifts(t *testing.T) {
	r := sirilRunner(t)
	dir := t.TempDir()
	const nFrames = 8
	writeStarFieldSeq(t, dir, nFrames, 2.0, 1.0)
	_, err := r.Run(context.Background(), dir, registerPreamble, nil)
	require.NoError(t, err)

	seq, err := grade.ParseSeq(filepath.Join(dir, "light_.seq"))
	require.NoError(t, err)
	require.Len(t, seq.Metrics, nFrames)
	for i := 1; i < nFrames; i++ {
		stepX := seq.Metrics[i].ShiftX - seq.Metrics[i-1].ShiftX
		stepY := seq.Metrics[i].ShiftY - seq.Metrics[i-1].ShiftY
		assert.InDelta(t, 2.0, math.Abs(stepX), 0.15, "frame %d x-step", i)
		assert.InDelta(t, 1.0, math.Abs(stepY), 0.15, "frame %d y-step", i)
	}
}

// writeRotatedStarFieldSeq writes nStraight dithered frames of a synthetic sky followed by
// nRotated frames of the SAME sky rotated by angleDeg about the frame centre — a two-night
// merge in miniature.
func writeRotatedStarFieldSeq(t *testing.T, dir string, nStraight, nRotated int, angleDeg float64) {
	t.Helper()
	const rw, rh, nStars = 256, 256, 40
	srng := &lcg{s: 777}
	type star struct{ x, y, a float64 }
	stars := make([]star, nStars)
	for i := range stars {
		stars[i] = star{20 + srng.next()*(rw-40), 20 + srng.next()*(rh-40), 0.3 + srng.next()*0.5}
	}
	psf := func(d2 float64) float64 { return 1.0 / (1.0 + d2/(2*1.8*1.8)) }
	render := func(path string, seed uint64, rotDeg, ox, oy float64) {
		rad := rotDeg * math.Pi / 180
		cos, sin := math.Cos(rad), math.Sin(rad)
		frng := &lcg{s: seed}
		writeMono(t, path, rw, rh, func(x, y int) float32 {
			v := 0.005 + (frng.next()-0.5)*0.002
			for _, s := range stars {
				rx := (s.x-rw/2)*cos - (s.y-rh/2)*sin + rw/2 + ox
				ry := (s.x-rw/2)*sin + (s.y-rh/2)*cos + rh/2 + oy
				ddx, ddy := float64(x)-rx, float64(y)-ry
				if d2 := ddx*ddx + ddy*ddy; d2 < 64 {
					v += s.a * psf(d2)
				}
			}
			return float32(v)
		})
	}
	for i := 0; i < nStraight; i++ {
		render(filepath.Join(dir, fmt.Sprintf("s_%03d.fits", i+1)), uint64(1000+i), 0, float64(i%3), float64((i%2)*-1))
	}
	for i := 0; i < nRotated; i++ {
		render(filepath.Join(dir, fmt.Sprintf("s_%03d.fits", nStraight+i+1)), uint64(2000+i), angleDeg, float64(i%2), float64(i%3))
	}
}

// TestSirilLive_SetRefAfter2PassFramingCurrent locks the anchored cross-night registration
// contract (Register2PassScript + ApplyRegistrationScript): a fresh Siril process finds the
// 2-pass-registered sequence by name, `setref` re-pins its reference, and `seqapplyreg
// -framing=current` keeps that frame's FULL canvas (no min-style intersection crop) — while the
// .seq homographies, re-referenced with grade.RelativeH, read the planted inter-"night" rotation.
// This is the Siril behavior the anchor-canvas merge (the task #312 fix) is built on.
func TestSirilLive_SetRefAfter2PassFramingCurrent(t *testing.T) {
	r := sirilRunner(t)
	dir := t.TempDir()
	const nStraight, nRotated, angle = 6, 4, 30.0
	writeRotatedStarFieldSeq(t, dir, nStraight, nRotated, angle)

	_, err := r.Run(context.Background(), dir, siril.Register2PassScript("light", "homography"), nil)
	require.NoError(t, err)
	seq, err := grade.ParseSeq(filepath.Join(dir, "light_.seq"))
	require.NoError(t, err)
	require.Len(t, seq.Metrics, nStraight+nRotated)
	require.True(t, seq.Metrics[2].HasH, "2-pass register writes full homographies")
	require.True(t, seq.Metrics[nStraight+1].HasH, "a 30° rotated frame still star-matches")

	// Our interpretation of Siril's H: re-referencing a rotated frame onto an upright one must
	// read the planted rotation, with a large footprint overlap (NOT absurd).
	rel, ok := grade.RelativeH(seq.Metrics[2].H, seq.Metrics[nStraight+1].H)
	require.True(t, ok)
	assert.InDelta(t, angle, math.Abs(grade.RotationDeg(rel)), 2.0)
	assert.Greater(t, grade.FootprintOverlap(rel, 256, 256), 0.5)

	_, err = r.Run(context.Background(), dir, siril.ApplyRegistrationScript("light", 3, "current"), nil)
	require.NoError(t, err, "setref after a 2-pass register must be accepted by a fresh process")
	seq2, err := grade.ParseSeq(filepath.Join(dir, "light_.seq"))
	require.NoError(t, err)
	assert.Equal(t, 2, seq2.Reference, "setref persisted into the sequence (0-based)")

	rot := filepath.Join(dir, fmt.Sprintf("r_light_%05d.fits", nStraight+2))
	require.FileExists(t, rot, "a rotated frame registers onto the pinned canvas")
	f, err := fits.Open(rot)
	require.NoError(t, err)
	w, _ := f.Header.Int("NAXIS1")
	h, _ := f.Header.Int("NAXIS2")
	assert.Equal(t, int64(256), w, "framing=current keeps the reference canvas — no intersection crop")
	assert.Equal(t, int64(256), h)
}

// predictUnionCanvas bounds every kept frame's transformed corners on the (0-based) refIdx frame's
// plane — the Go-side prediction of Siril's `seqapplyreg -framing=max` output geometry. The live
// test below pins the rounding convention; unionCanvasOf (internal/pipeline) must match it.
func predictUnionCanvas(seq *grade.Sequence, refIdx0, w, h int) (uw, uh, offX, offY int, ok bool) {
	refH := seq.Metrics[refIdx0].H
	minX, minY, maxX, maxY := 0.0, 0.0, float64(w), float64(h)
	for _, m := range seq.Metrics {
		if !m.HasH {
			continue
		}
		rel, rok := grade.RelativeH(refH, m.H)
		if !rok {
			return 0, 0, 0, 0, false
		}
		for _, c := range [4][2]float64{{0, 0}, {float64(w), 0}, {float64(w), float64(h)}, {0, float64(h)}} {
			den := rel[6]*c[0] + rel[7]*c[1] + rel[8]
			if den == 0 {
				return 0, 0, 0, 0, false
			}
			x := (rel[0]*c[0] + rel[1]*c[1] + rel[2]) / den
			y := (rel[3]*c[0] + rel[4]*c[1] + rel[5]) / den
			minX, maxX = math.Min(minX, x), math.Max(maxX, x)
			minY, maxY = math.Min(minY, y), math.Max(maxY, y)
		}
	}
	return int(math.Ceil(maxX)) - int(math.Floor(minX)), int(math.Ceil(maxY)) - int(math.Floor(minY)),
		-int(math.Floor(minX)), -int(math.Floor(minY)), true
}

// argmaxPixel returns the brightest pixel's coordinates of a mono FITS.
func argmaxPixel(t *testing.T, path string) (int, int) {
	t.Helper()
	im, err := fits.ReadImage(path)
	require.NoError(t, err)
	best, bx, by := float32(-1), 0, 0
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			if v := im.Pix[0][y*im.W+x]; v > best {
				best, bx, by = v, x, y
			}
		}
	}
	return bx, by
}

// TestSirilLive_FramingMaxPerFrameCanvases pins the DISCOVERED (and for mosaic: unusable)
// Siril 1.4.4 behavior of `seqapplyreg -framing=max`: every output frame gets its OWN bounding
// canvas — mixed dimensions inside one registered sequence, no shared union canvas. The mosaic
// union is therefore built by Go-side padding + framing=current (see the padded test below); if a
// future Siril makes max produce a shared canvas, this test fails and the pivot can be revisited.
func TestSirilLive_FramingMaxPerFrameCanvases(t *testing.T) {
	r := sirilRunner(t)
	dir := t.TempDir()
	writeRotatedStarFieldSeq(t, dir, 6, 4, 30.0)
	_, err := r.Run(context.Background(), dir, siril.Register2PassScript("light", "homography"), nil)
	require.NoError(t, err)
	_, err = r.Run(context.Background(), dir, "requires 1.2.0\nsetext fits\nset32bits\nseqapplyreg light -framing=max\n", nil)
	require.NoError(t, err)

	dims := func(idx int) (int64, int64) {
		f, err := fits.Open(filepath.Join(dir, fmt.Sprintf("r_light_%05d.fits", idx)))
		require.NoError(t, err)
		w, _ := f.Header.Int("NAXIS1")
		h, _ := f.Header.Int("NAXIS2")
		return w, h
	}
	w3, h3 := dims(3) // an upright frame: its own footprint is (about) the sensor rectangle
	w8, h8 := dims(8) // a 30°-rotated frame: its own bounding box is much larger
	assert.InDelta(t, 256, float64(w3), 3)
	assert.InDelta(t, 256, float64(h3), 3)
	assert.Greater(t, w8, int64(300), "rotated frame gets its own (larger) canvas")
	assert.Greater(t, h8, int64(300))
	assert.NotEqual(t, w3, w8, "framing=max yields MIXED canvas dims — not a shared union")
}

// TestSirilLive_FilterInclKeepsNumbering pins the selection contract of
// ApplyRegistrationSelectedScript: excluded frames are skipped WITHOUT renumbering — the output
// files keep their original sequence indices (gaps where frames were excluded), so merged-order
// index maps stay valid across a filtered apply.
func TestSirilLive_FilterInclKeepsNumbering(t *testing.T) {
	r := sirilRunner(t)
	dir := t.TempDir()
	writeRotatedStarFieldSeq(t, dir, 6, 4, 30.0)
	_, err := r.Run(context.Background(), dir, siril.Register2PassScript("light", "homography"), nil)
	require.NoError(t, err)

	_, err = r.Run(context.Background(), dir,
		siril.ApplyRegistrationSelectedScript("light", 3, "current", []int{2}), nil)
	require.NoError(t, err)

	assert.NoFileExists(t, filepath.Join(dir, "r_light_00002.fits"), "excluded frame is skipped")
	require.FileExists(t, filepath.Join(dir, "r_light_00003.fits"), "original numbering is kept")
	require.FileExists(t, filepath.Join(dir, "r_light_00010.fits"))
	regs, err := filepath.Glob(filepath.Join(dir, "r_light_*.fits"))
	require.NoError(t, err)
	assert.Len(t, regs, 9)
}

// padFrameTo writes src's pixels onto a uw×uh zero canvas at (offX, offY) — the test-local twin of
// the pipeline's mosaic padding.
func padFrameTo(t *testing.T, src, dst string, uw, uh, offX, offY int) {
	t.Helper()
	im, err := fits.ReadImage(src)
	require.NoError(t, err)
	out := fits.NewImage(uw, uh, 1)
	for y := 0; y < im.H; y++ {
		copy(out.Pix[0][(y+offY)*uw+offX:(y+offY)*uw+offX+im.W], im.Pix[0][y*im.W:(y+1)*im.W])
	}
	require.NoError(t, out.WriteFITS(dst))
}

// TestSirilLive_PaddedReregisterUnionCanvas pins the MOSAIC mechanism end-to-end: pad every frame
// to the Go-computed union bbox, re-register the padded sequence, and apply with framing=current —
// the output canvas is exactly the union, the anchor content sits at the exact pad offset, and a
// rotated frame keeps ALL its pixels (nothing cropped).
func TestSirilLive_PaddedReregisterUnionCanvas(t *testing.T) {
	r := sirilRunner(t)
	dir := t.TempDir()
	const nStraight, nRotated, angle = 6, 4, 30.0
	writeRotatedStarFieldSeq(t, dir, nStraight, nRotated, angle)

	_, err := r.Run(context.Background(), dir, siril.Register2PassScript("light", "homography"), nil)
	require.NoError(t, err)
	seq, err := grade.ParseSeq(filepath.Join(dir, "light_.seq"))
	require.NoError(t, err)
	const refIdx0 = 2
	uw, uh, offX, offY, ok := predictUnionCanvas(seq, refIdx0, 256, 256)
	require.True(t, ok)
	require.Greater(t, uw, 300, "the 30° night must grow the union")

	padded := filepath.Join(dir, "padded")
	require.NoError(t, os.MkdirAll(padded, 0o755))
	for i := 1; i <= nStraight+nRotated; i++ {
		padFrameTo(t, filepath.Join(dir, fmt.Sprintf("light_%05d.fits", i)),
			filepath.Join(padded, fmt.Sprintf("pad_%03d.fits", i)), uw, uh, offX, offY)
	}

	_, err = r.Run(context.Background(), padded, siril.Register2PassScript("light", "homography"), nil)
	require.NoError(t, err)
	_, err = r.Run(context.Background(), padded, siril.ApplyRegistrationScript("light", refIdx0+1, "current"), nil)
	require.NoError(t, err)

	anchorReg := filepath.Join(padded, fmt.Sprintf("r_light_%05d.fits", refIdx0+1))
	require.FileExists(t, anchorReg)
	f, err := fits.Open(anchorReg)
	require.NoError(t, err)
	w, _ := f.Header.Int("NAXIS1")
	h, _ := f.Header.Int("NAXIS2")
	assert.Equal(t, int64(uw), w, "union canvas = the padded anchor canvas, exactly")
	assert.Equal(t, int64(uh), h)

	origX, origY := argmaxPixel(t, filepath.Join(dir, "light_00003.fits"))
	regX, regY := argmaxPixel(t, anchorReg)
	assert.InDelta(t, float64(origX+offX), float64(regX), 2, "anchor content at the exact pad offset (X)")
	assert.InDelta(t, float64(origY+offY), float64(regY), 2, "anchor content at the exact pad offset (Y)")

	rot, err := fits.ReadImage(filepath.Join(padded, fmt.Sprintf("r_light_%05d.fits", nStraight+2)))
	require.NoError(t, err)
	nonzero := 0
	for _, v := range rot.Pix[0] {
		if v != 0 {
			nonzero++
		}
	}
	assert.Greater(t, nonzero, 256*256*9/10, "a rotated frame keeps (essentially) all its pixels")
}

// TestSirilLive_StackSelectedSeqNameAndFloor locks two contracts StackSelectedScript relies on:
// (1) the registered sequence really is addressable as "<prefix>_" and stacking it produces no
// "Reading sequence failed" name-lookup recovery noise; (2) `stack -filter-incl` with a single
// included frame fails with the exact two-image floor phrase grade's stackMinimum guards against —
// and sirilErrorHint surfaces that phrase (not the recovery noise) as the error.
func TestSirilLive_StackSelectedSeqNameAndFloor(t *testing.T) {
	r := sirilRunner(t)
	dir := t.TempDir()
	const nFrames = 4
	writeStarFieldSeq(t, dir, nFrames, 0, 0)
	_, err := r.Run(context.Background(), dir, registerPreamble, nil)
	require.NoError(t, err)

	// (2) three of four frames unselected → one survivor → the documented floor error is the hint
	// (live Siril emits no "Error in line" locator for this runtime failure, only the cause line
	// and a progress status — the hint must be the cause, not the noise).
	_, err = r.Run(context.Background(), dir,
		siril.StackSelectedScript("r_light", nFrames, []int{1, 2, 3}, filepath.Join(dir, "master_fail"), liveLights(stackalg.WeightNone)), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "do not allow at least two images")
	assert.NotContains(t, err.Error(), "Reading sequence failed")
	assert.NotContains(t, err.Error(), "Script execution failed")

	// (1) a valid selection stacks cleanly, addressed by its real "r_light_" name.
	res, err := r.Run(context.Background(), dir,
		siril.StackSelectedScript("r_light", nFrames, []int{1}, filepath.Join(dir, "master_ok"), liveLights(stackalg.WeightNone)), nil)
	require.NoError(t, err)
	assert.NotContains(t, res.Log, "Reading sequence failed")
	assert.FileExists(t, filepath.Join(dir, "master_ok.fits"))

	// (3) `-weight=noise` — the automatic switch for photometrically amplified groups
	// (photomStackWeight) — is accepted by the same stack grammar (house rule: every new .ssf
	// shape is smoke-tested live before the pipeline may emit it).
	_, err = r.Run(context.Background(), dir,
		siril.StackSelectedScript("r_light", nFrames, nil, filepath.Join(dir, "master_noise"), liveLights(stackalg.WeightNoise)), nil)
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(dir, "master_noise.fits"))
}

// TestSirilLive_SeqSubskyPrefixAndLevel pins the `seqsubsky` contract the multi-night seam flatten
// (Preset.FlattenBg) builds on: output naming under -prefix=, per-frame degree-1 gradient removal,
// and what happens to the mean sky level — the stack's -norm=addscale tolerates either semantics,
// but the pipeline must not ASSUME one.
func TestSirilLive_SeqSubskyPrefixAndLevel(t *testing.T) {
	r := sirilRunner(t)
	dir := t.TempDir()
	const n = 3
	for i := 1; i <= n; i++ {
		tilt := float32(i) * 0.02 // each frame gets its own gradient slope
		writeMono(t, filepath.Join(dir, fmt.Sprintf("s_%03d.fits", i)), 128, 128, func(x, y int) float32 {
			return 0.10 + tilt*float32(x)/127 // pedestal 0.10 + left→right gradient
		})
	}
	script := "requires 1.2.0\nsetext fits\nset32bits\n" +
		"link light -out=.\n" +
		"seqsubsky light 1 -prefix=flat_\n"
	_, err := r.Run(context.Background(), dir, script, nil)
	require.NoError(t, err, "seqsubsky must accept `<seq> <degree> -prefix=`")

	for i := 1; i <= n; i++ {
		p := filepath.Join(dir, fmt.Sprintf("flat_light_%05d.fits", i))
		require.FileExists(t, p, "-prefix=flat_ names the outputs flat_light_NNNNN")
		left, right := readPixel(t, p, 4, 64), readPixel(t, p, 123, 64)
		grad := float64(i) * 0.02 * (123.0 - 4.0) / 127
		assert.Less(t, math.Abs(float64(right-left)), grad*0.15,
			"frame %d: the planted left→right gradient is flattened (was %.4f)", i, grad)
		// Level semantics (pinned empirically on 1.4): the frame's MEAN level is PRESERVED — the
		// background model is subtracted around it, not to zero. Downstream (photom already ran;
		// grading backgrounds; -norm=addscale) can rely on skies keeping their pedestal.
		mean := 0.10 + float64(i)*0.02/2
		assert.InDelta(t, mean, float64(left), 0.012, "frame %d: sky stays at the frame's mean level", i)
	}
}

// liveLights is the historical light-stack recipe with a weighting — what these live tests exercised
// before stacking became configurable.
func liveLights(w stackalg.Weight) stackalg.Options {
	o := stackalg.DefaultLights()
	o.Weight = w
	return o
}

// sirilSummary extracts one line of Siril's end-of-stack summary block ("Pixel combination",
// "Input normalization", "Pixel rejection", …), where the value follows a run of dots. Asserting on
// this instead of the raw log keeps a failure readable — and it is the only place Siril states what
// it ACTUALLY did, as opposed to what the command asked for.
func sirilSummary(log, field string) string {
	re := regexp.MustCompile(`(?m)^log: ` + regexp.QuoteMeta(field) + ` \.+ (.+)$`)
	m := re.FindStringSubmatch(log)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// TestSirilLive_StackClauseGrammar is the trap-catcher for the user-selectable stacking panel: every
// clause StackClause can emit is run against the real siril-cli, and Siril's own summary must PROVE
// the option took effect. "No error" is not enough — Siril accepts several wrong-but-plausible
// spellings and then silently ignores them (`-norm=additive` is the classic, logging "none"), so
// each case pins the algorithm/normalization Siril reports back.
func TestSirilLive_StackClauseGrammar(t *testing.T) {
	r := sirilRunner(t)
	lights := func(f func(*stackalg.Options)) stackalg.Options {
		o := stackalg.DefaultLights()
		f(&o)
		return o
	}
	cases := []struct {
		name              string
		opts              stackalg.Options
		combination       string // Siril's "Pixel combination" summary value
		rejection         string // Siril's "Pixel rejection" summary value ("" = not applicable)
		normalization     string // Siril's "Input normalization" summary value
		wantWeighting     string
		wantRejectionMaps string
	}{
		{name: "none", opts: lights(func(o *stackalg.Options) { o.Reject = stackalg.RejectNone }),
			combination: "average", rejection: "none", normalization: "additive + scaling"},
		{name: "percentile", opts: lights(func(o *stackalg.Options) { o.Reject = stackalg.RejectPercentile }),
			combination: "average", rejection: "percentile clipping", normalization: "additive + scaling"},
		{name: "sigma", opts: lights(func(o *stackalg.Options) { o.Reject = stackalg.RejectSigma }),
			combination: "average", rejection: "sigma clipping", normalization: "additive + scaling"},
		{name: "median_sigma", opts: lights(func(o *stackalg.Options) { o.Reject = stackalg.RejectMedianSigma }),
			combination: "average", rejection: "median sigma clipping", normalization: "additive + scaling"},
		{name: "winsorized", opts: lights(func(o *stackalg.Options) { o.Reject = stackalg.RejectWinsorized }),
			combination: "average", rejection: "winsorized sigma clipping", normalization: "additive + scaling"},
		{name: "linear_fit", opts: lights(func(o *stackalg.Options) { o.Reject = stackalg.RejectLinearFit }),
			combination: "average", rejection: "linear fit clipping", normalization: "additive + scaling"},
		{name: "gesd", opts: lights(func(o *stackalg.Options) { o.Reject = stackalg.RejectGESD }),
			combination: "average", rejection: "GESDT clipping", normalization: "additive + scaling"},
		{name: "mad", opts: lights(func(o *stackalg.Options) { o.Reject = stackalg.RejectMAD }),
			combination: "average", rejection: "MAD clipping", normalization: "additive + scaling"},
		{name: "median combine", opts: lights(func(o *stackalg.Options) { o.Combine = stackalg.CombineMedian }),
			combination: "median", normalization: "additive + scaling"},
		{name: "sum combine", opts: lights(func(o *stackalg.Options) { o.Combine = stackalg.CombineSum })},
		{name: "max combine", opts: lights(func(o *stackalg.Options) { o.Combine = stackalg.CombineMax })},
		{name: "min combine", opts: lights(func(o *stackalg.Options) { o.Combine = stackalg.CombineMin })},
		{name: "norm add", opts: lights(func(o *stackalg.Options) { o.Norm = stackalg.NormAdd }),
			combination: "average", rejection: "winsorized sigma clipping", normalization: "additive"},
		{name: "norm mul", opts: lights(func(o *stackalg.Options) { o.Norm = stackalg.NormMul }),
			combination: "average", rejection: "winsorized sigma clipping", normalization: "multiplicative"},
		{name: "norm mulscale", opts: lights(func(o *stackalg.Options) { o.Norm = stackalg.NormMulScale }),
			combination: "average", rejection: "winsorized sigma clipping", normalization: "multiplicative + scaling"},
		{name: "nonorm", opts: lights(func(o *stackalg.Options) { o.Norm = stackalg.NormNone }),
			combination: "average", rejection: "winsorized sigma clipping", normalization: "none"},
		// "(fast)" is Siril confirming it swapped IKSS for the cheap estimators — the flag really lands.
		{name: "fastnorm", opts: lights(func(o *stackalg.Options) { o.FastNorm = true }),
			combination: "average", rejection: "winsorized sigma clipping", normalization: "additive + scaling (fast)"},
		{name: "weight noise", opts: lights(func(o *stackalg.Options) { o.Weight = stackalg.WeightNoise }),
			combination: "average", rejection: "winsorized sigma clipping", normalization: "additive + scaling",
			wantWeighting: "noise"},
		{name: "rejection maps", opts: lights(func(o *stackalg.Options) { o.RejMaps = true }),
			combination: "average", rejection: "winsorized sigma clipping", normalization: "additive + scaling",
			wantRejectionMaps: "yes"},
		{name: "feather", opts: lights(func(o *stackalg.Options) { o.Feather = 4 }),
			combination: "average", rejection: "winsorized sigma clipping", normalization: "additive + scaling"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "s")
			writeNoiseSeq(t, dir, 12)
			clause := siril.StackClause(tc.opts, 12)
			script := "requires 1.2.0\nsetext fits\nset32bits\nlink cal -out=.\n" +
				fmt.Sprintf("stack cal %s -out=m\n", clause)
			res, err := r.Run(context.Background(), dir, script, nil)
			require.NoError(t, err, "Siril rejected the clause %q", clause)
			require.FileExists(t, filepath.Join(dir, "m.fits"), "the stack produced no master")

			if tc.combination != "" {
				assert.Equal(t, tc.combination, sirilSummary(res.Log, "Pixel combination"), "clause %q", clause)
			}
			if tc.rejection != "" {
				assert.Equal(t, tc.rejection, sirilSummary(res.Log, "Pixel rejection"), "clause %q", clause)
			}
			if tc.normalization != "" {
				assert.Equal(t, tc.normalization, sirilSummary(res.Log, "Input normalization"),
					"the normalization was accepted and then silently ignored — clause %q", clause)
			}
			if tc.wantWeighting != "" {
				assert.Contains(t, sirilSummary(res.Log, "Image weighting"), tc.wantWeighting, "clause %q", clause)
			}
			if tc.wantRejectionMaps != "" {
				assert.Equal(t, tc.wantRejectionMaps, sirilSummary(res.Log, "Creating rejection maps"), "clause %q", clause)
			}
		})
	}
}
