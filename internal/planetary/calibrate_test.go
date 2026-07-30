package planetary

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/inspect"
)

const calW, calH = 6, 4

// calImage builds a mono test image whose pixel value is fill(x, y).
func calImage(fill func(x, y int) float32) *fits.Image {
	im := fits.NewImage(calW, calH, 1)
	for y := 0; y < calH; y++ {
		for x := 0; x < calW; x++ {
			im.Pix[0][y*calW+x] = fill(x, y)
		}
	}
	return im
}

func writeImage(t *testing.T, path string, im *fits.Image) string {
	t.Helper()
	require.NoError(t, im.WriteFITS(path))
	return path
}

// lightSet hand-shapes a classified Light set the way inspect groups this camera's frames
// (gain 0 / offset 0 / bin 1, exposure in ms, 5 °C temp bucket).
func lightSet(filter string, expMs int64, tempC, count int) inspect.Set {
	return inspect.Set{
		Key: inspect.SetKey{
			Type: inspect.Light, Filter: filter, ExposureMs: expMs,
			Gain: 0, Offset: 0, Bin: 1, TempBucket: tempC,
		},
		Count: count,
	}
}

// calibFixture is one runnable calibrateChannel scenario: scratch layout + masters + two frames.
type calibFixture struct {
	runRoot, chDir string
	frames         []string
	masters        []calib.Master
}

// newCalibFixture writes a 0.1 dark and a mean-2 flat (left half 1.0, right half 3.0) plus two constant
// 0.6 lights as regular files inside the channel scratch — the in-place overwrite case.
func newCalibFixture(t *testing.T) *calibFixture {
	t.Helper()
	runRoot := t.TempDir()
	chDir := filepath.Join(runRoot, "ch_L")
	require.NoError(t, os.MkdirAll(chDir, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(runRoot, "calmasters"), 0o755))
	darkPath := writeImage(t, filepath.Join(runRoot, "calmasters", "master_dark.fits"),
		calImage(func(x, y int) float32 { return 0.1 }))
	flatPath := writeImage(t, filepath.Join(runRoot, "calmasters", "master_flat_L.fits"),
		calImage(func(x, y int) float32 {
			if x < calW/2 {
				return 1.0
			}
			return 3.0
		}))
	f := &calibFixture{runRoot: runRoot, chDir: chDir}
	for i := 0; i < 2; i++ {
		f.frames = append(f.frames, writeImage(t,
			filepath.Join(chDir, fmt.Sprintf("vid_%05d.fits", i+1)),
			calImage(func(x, y int) float32 { return 0.6 })))
	}
	f.masters = []calib.Master{
		{Type: calib.MasterDark, ExposureMs: 10, Gain: 0, Offset: 0, Bin: 1,
			TempMilliC: -17000, HasTemp: true, FrameCount: 64, Path: darkPath},
		{Type: calib.MasterFlat, Filter: "L", ExposureMs: 10, Gain: 0, Offset: 0, Bin: 1,
			FrameCount: 64, Path: flatPath},
	}
	return f
}

func (f *calibFixture) inv() *inspect.Inventory {
	return &inspect.Inventory{Sets: []inspect.Set{lightSet("L", 10, -20, 2)}}
}

// assertCalibrated checks the exact (L−D)/flatNorm math of the fixture: flat mean is 2, so the
// normalized flat is 0.5 | 1.5 and (0.6−0.1) becomes 1.0 on the left, 1/3 on the right.
func assertCalibrated(t *testing.T, path string) {
	t.Helper()
	im, err := fits.ReadImage(path)
	require.NoError(t, err)
	for y := 0; y < calH; y++ {
		for x := 0; x < calW; x++ {
			want := float32(1.0)
			if x >= calW/2 {
				want = 0.5 / 1.5
			}
			assert.InDelta(t, want, im.Pix[0][y*calW+x], 1e-5, "pixel (%d,%d)", x, y)
		}
	}
}

func TestCalibrateChannel_DarkFlatMath(t *testing.T) {
	f := newCalibFixture(t)
	out, notes, err := calibrateChannel(context.Background(), f.frames, "L", f.inv(), f.masters, nil, false, f.chDir, f.runRoot, nil)
	require.NoError(t, err)
	assert.Equal(t, f.frames, out, "regular files under scratch are rewritten in place")
	require.True(t, anyNoteContains(notes, "calibrated L"), "notes: %v", notes)
	for _, p := range out {
		assertCalibrated(t, p)
	}
}

func TestCalibrateChannel_ADUScaleLightNormalized(t *testing.T) {
	f := newCalibFixture(t)
	// Rewrite the lights in ADU scale (0..65535, an in-place 16-bit capture) — the result must be
	// identical to the [0,1] case because both sides are normalized onto one scale before the math.
	for _, p := range f.frames {
		writeImage(t, p, calImage(func(x, y int) float32 { return 0.6 * 65535 }))
	}
	out, _, err := calibrateChannel(context.Background(), f.frames, "L", f.inv(), f.masters, nil, false, f.chDir, f.runRoot, nil)
	require.NoError(t, err)
	for _, p := range out {
		assertCalibrated(t, p)
	}
}

func TestCalibrateChannel_DimsMismatchDropsMasters(t *testing.T) {
	f := newCalibFixture(t)
	// Shrink both masters: they must be dropped (never applied, never crash) and the frames untouched.
	small := fits.NewImage(2, 2, 1)
	for i := range f.masters {
		writeImage(t, f.masters[i].Path, small)
	}
	out, notes, err := calibrateChannel(context.Background(), f.frames, "L", f.inv(), f.masters, nil, false, f.chDir, f.runRoot, nil)
	require.NoError(t, err)
	assert.Equal(t, f.frames, out)
	assert.True(t, anyNoteContains(notes, "does not match"), "notes: %v", notes)
	assert.True(t, anyNoteContains(notes, "uncalibrated"), "notes: %v", notes)
	im, err := fits.ReadImage(f.frames[0])
	require.NoError(t, err)
	assert.InDelta(t, 0.6, im.Pix[0][0], 1e-6, "frame must be untouched")
}

func TestCalibrateChannel_InPlaceSourcesGetCopies(t *testing.T) {
	f := newCalibFixture(t)
	// Move the frames OUTSIDE the scratch root — the in-place-FITS case. Sources must stay byte-stable
	// and the calibrated result must land as copies in the channel dir.
	captureDir := t.TempDir()
	var srcs []string
	for _, p := range f.frames {
		dst := filepath.Join(captureDir, filepath.Base(p))
		require.NoError(t, os.Rename(p, dst))
		srcs = append(srcs, dst)
	}
	out, _, err := calibrateChannel(context.Background(), srcs, "L", f.inv(), f.masters, nil, false, f.chDir, f.runRoot, nil)
	require.NoError(t, err)
	require.Len(t, out, len(srcs))
	for i, p := range out {
		assert.NotEqual(t, srcs[i], p, "must not rewrite an input capture")
		assert.True(t, filepath.Dir(p) == f.chDir, "copy must land in the channel dir: %s", p)
		assertCalibrated(t, p)
		orig, err := fits.ReadImage(srcs[i])
		require.NoError(t, err)
		assert.InDelta(t, 0.6, orig.Pix[0][0], 1e-6, "source frame mutated")
	}
}

func TestCalibrateChannel_SymlinkedFrameNotOverwritten(t *testing.T) {
	f := newCalibFixture(t)
	// Siril `convert` symlinks FITS sources instead of copying: a symlink under scratch must be treated
	// like an in-place source (copy written, target untouched) or the original capture gets corrupted.
	captureDir := t.TempDir()
	target := filepath.Join(captureDir, "orig.fits")
	require.NoError(t, os.Rename(f.frames[0], target))
	link := filepath.Join(f.chDir, "vid_00001.fits")
	require.NoError(t, os.Symlink(target, link))
	out, _, err := calibrateChannel(context.Background(), []string{link, f.frames[1]}, "L", f.inv(), f.masters, nil, false, f.chDir, f.runRoot, nil)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(f.chDir, "cal_00001.fits"), out[0], "symlink → calibrated copy")
	assert.Equal(t, f.frames[1], out[1], "regular scratch file → in place")
	orig, err := fits.ReadImage(target)
	require.NoError(t, err)
	assert.InDelta(t, 0.6, orig.Pix[0][0], 1e-6, "symlink target mutated")
}

func TestCalibrateChannel_DarkOptimizeRefused(t *testing.T) {
	f := newCalibFixture(t)
	// Only a WRONG-exposure dark + a bias exist: MatchForLight selects it with DarkOptimize (Siril -opt
	// semantics the in-process math cannot honor) — the dark must be refused and the bias subtracted.
	biasPath := writeImage(t, filepath.Join(f.runRoot, "calmasters", "master_bias.fits"),
		calImage(func(x, y int) float32 { return 0.1 })) // same value as the fixture dark → same expectation
	f.masters[0].ExposureMs = 300000 // the dark no longer matches the 10ms lights
	f.masters = append(f.masters, calib.Master{
		Type: calib.MasterBias, Gain: 0, Offset: 0, Bin: 1, FrameCount: 64, Path: biasPath,
	})
	out, notes, err := calibrateChannel(context.Background(), f.frames, "L", f.inv(), f.masters, nil, false, f.chDir, f.runRoot, nil)
	require.NoError(t, err)
	assert.True(t, anyNoteContains(notes, "dark-optimization"), "notes: %v", notes)
	assert.True(t, anyNoteContains(notes, "bias"), "the bias must take over the subtraction: %v", notes)
	for _, p := range out {
		assertCalibrated(t, p)
	}
}

func TestCalibrateChannel_DominantSetWhenChannelSpansSets(t *testing.T) {
	f := newCalibFixture(t)
	// The channel spans two capture sets; the larger one (10 ms) must drive the match — its dark exists.
	inv := &inspect.Inventory{Sets: []inspect.Set{
		lightSet("L", 10, -20, 5),
		lightSet("L", 20, -20, 2),
	}}
	out, notes, err := calibrateChannel(context.Background(), f.frames, "L", inv, f.masters, nil, false, f.chDir, f.runRoot, nil)
	require.NoError(t, err)
	assert.True(t, anyNoteContains(notes, "spans 2 capture sets"), "notes: %v", notes)
	assert.True(t, anyNoteContains(notes, "calibrated L"), "notes: %v", notes)
	for _, p := range out {
		assertCalibrated(t, p)
	}
}

func TestCalibrateChannel_NoMatch(t *testing.T) {
	f := newCalibFixture(t)
	// A different camera's masters (gain mismatch) → no dark/bias; the flat still applies? No: bestFlat
	// falls back cross-camera, so only the dark side is missing. Force a full miss with gain+filterless.
	for i := range f.masters {
		f.masters[i].Gain = 300
	}
	f.masters = f.masters[:1] // dark only, wrong gain → nothing matches
	out, notes, err := calibrateChannel(context.Background(), f.frames, "L", f.inv(), f.masters, nil, false, f.chDir, f.runRoot, nil)
	require.NoError(t, err)
	assert.Equal(t, f.frames, out)
	assert.True(t, anyNoteContains(notes, "uncalibrated"), "notes: %v", notes)
}

func TestUnderDirAndSafeOverwrite(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "ch_L")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	regular := writeImage(t, filepath.Join(sub, "a.fits"), fits.NewImage(2, 2, 1))
	outside := writeImage(t, filepath.Join(t.TempDir(), "b.fits"), fits.NewImage(2, 2, 1))
	link := filepath.Join(sub, "l.fits")
	require.NoError(t, os.Symlink(outside, link))

	assert.True(t, underDir(regular, root))
	assert.False(t, underDir(outside, root))
	assert.False(t, underDir(root, root), "a dir is not under itself")

	assert.True(t, safeOverwrite(regular, root))
	assert.False(t, safeOverwrite(outside, root), "outside the scratch root")
	assert.False(t, safeOverwrite(link, root), "symlinks are never written through")
}

func anyNoteContains(notes []string, sub string) bool {
	for _, n := range notes {
		if strings.Contains(n, sub) {
			return true
		}
	}
	return false
}
