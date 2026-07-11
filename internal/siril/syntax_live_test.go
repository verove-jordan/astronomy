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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/grade"
	"github.com/verove-jordan/astronomy/internal/siril"
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
	_, err := r.Run(context.Background(), dir, siril.StackMasterScript("cal", filepath.Join(dir, "m"), 10), nil)
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

// TestSirilLive_SeqHomographyShifts locks the .seq R-line format grade.ParseSeq reads: register a
// synthetic star field with a planted (2,1) px/frame drift and require the parsed consecutive
// shift deltas to reproduce it (signs are Siril's own convention; magnitudes are what matter).
func TestSirilLive_SeqHomographyShifts(t *testing.T) {
	r := sirilRunner(t)
	dir := t.TempDir()
	const rw, rh, nStars, nFrames = 256, 256, 40, 8
	srng := &lcg{s: 777}
	type star struct{ x, y, a float64 }
	stars := make([]star, nStars)
	for i := range stars {
		stars[i] = star{20 + srng.next()*(rw-40), 20 + srng.next()*(rh-40), 0.3 + srng.next()*0.5}
	}
	psf := func(d2 float64) float64 { return 1.0 / (1.0 + d2/(2*1.8*1.8)) }
	for i := 0; i < nFrames; i++ {
		dx, dy := 2.0*float64(i), 1.0*float64(i)
		frng := &lcg{s: uint64(1000 + i)}
		writeMono(t, filepath.Join(dir, fmt.Sprintf("s_%03d.fits", i+1)), rw, rh, func(x, y int) float32 {
			v := 0.005 + (frng.next()-0.5)*0.002
			for _, s := range stars {
				ddx, ddy := float64(x)-(s.x+dx), float64(y)-(s.y+dy)
				if d2 := ddx*ddx + ddy*ddy; d2 < 64 {
					v += s.a * psf(d2)
				}
			}
			return float32(v)
		})
	}
	_, err := r.Run(context.Background(), dir,
		"requires 1.2.0\nsetext fits\nset32bits\nlink light -out=.\nregister light -2pass\nseqapplyreg light\n", nil)
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
