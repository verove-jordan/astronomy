package pipeline

import (
	"math"
	"math/rand"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// synthLumRamp builds a stretched-looking image whose SKY level ramps ±15% of bg across the width
// (the grey-left/black-right case), with a bright broadband object that must keep its photometry.
func synthLumRamp(t *testing.T, channels int) *fits.Image {
	t.Helper()
	im := fits.NewImage(scW, scH, channels)
	rng := rand.New(rand.NewSource(9))
	for y := 0; y < scH; y++ {
		for x := 0; x < scW; x++ {
			i := y*scW + x
			level := scBg * (1 + 0.30*(0.5-float64(x)/scW)) // +15% left, −15% right
			v := level + 0.004*rng.NormFloat64()
			if x >= 300 && x < 360 && y >= 100 && y < 160 {
				v = 0.55 // bright object
			}
			for c := 0; c < channels; c++ {
				im.Pix[c][i] = float32(v)
			}
		}
	}
	return im
}

func skyLevel(im *fits.Image, x0, x1 int) float64 {
	var vals []float64
	for y := 180; y < 240; y++ {
		for x := x0; x < x1; x++ {
			vals = append(vals, float64(im.Pix[0][y*scW+x]))
		}
	}
	return median64(vals)
}

func TestFlattenSkyLuminance(t *testing.T) {
	for _, tc := range []struct {
		name     string
		channels int
	}{
		{"rgb", 3},
		{"mono", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			im := synthLumRamp(t, tc.channels)
			path := filepath.Join(t.TempDir(), "stretch.fits")
			require.NoError(t, im.WriteFITS(path))
			before, err := fits.ReadImage(path)
			require.NoError(t, err)

			note, err := flattenSkyLuminance(path, 64)
			require.NoError(t, err)
			assert.Contains(t, note, "sky luminance flattened")

			after, err := fits.ReadImage(path)
			require.NoError(t, err)

			// The ±15% ramp flattens to a uniform level…
			leftB, rightB := skyLevel(before, 10, 90), skyLevel(before, scW-90, scW-10)
			require.Greater(t, leftB/rightB, 1.25) // ramp really was there
			left, right := skyLevel(after, 10, 90), skyLevel(after, scW-90, scW-10)
			assert.InDelta(t, 1.0, left/right, 0.02)

			// …at the DARK end of the ramp (≈P10 of the fitted surface = the glow-free sky): any
			// higher target would sit mid-glow and wash the whole frame brighter than the level
			// the finish curves are designed for. The correction is SIGNED, so the few tiles the
			// surface puts below the target are lifted to it — one level, no darker second zone.
			darkEnd := skyLevel(before, scW-90, scW-10)
			assert.InDelta(t, darkEnd, skyLevel(after, scW/2-40, scW/2+40), 0.006)

			// The object shifts only with its local sky (locally uniform correction, no feather):
			// its CONTRAST over the sky in the SAME columns is preserved exactly. (Sky at another
			// x sat on a different ramp level by design — that difference is what the flatten
			// removes, so the reference must share the object's x.)
			objContrast := func(im *fits.Image) float64 {
				var obj, sky []float64
				for x := 315; x < 345; x++ {
					for y := 115; y < 145; y++ {
						obj = append(obj, float64(im.Pix[0][y*scW+x]))
					}
					for y := 190; y < 220; y++ {
						sky = append(sky, float64(im.Pix[0][y*scW+x]))
					}
				}
				return median64(obj) - median64(sky)
			}
			assert.InDelta(t, objContrast(before), objContrast(after), 0.003)

			if tc.channels == 3 {
				// Chroma untouched: identical subtraction per channel keeps R−G at 0 everywhere.
				worst := 0.0
				for i := range after.Pix[0] {
					worst = math.Max(worst, math.Abs(float64(after.Pix[0][i]-after.Pix[1][i])))
				}
				assert.Less(t, worst, 1e-6)
			}
		})
	}
}

func TestFlattenSkyLuminance_FaintStructureSurvives(t *testing.T) {
	// A blob keeps its CONTRAST over the adjacent sky: like subsky, the flatten subtracts the same
	// smooth glow from the blob and its surroundings (its tiles are rejected from the fit).
	im := synthLumRamp(t, 1)
	for y := 250; y < 300; y++ {
		for x := 100; x < 150; x++ {
			im.Pix[0][y*scW+x] += 0.12
		}
	}
	path := filepath.Join(t.TempDir(), "faint.fits")
	require.NoError(t, im.WriteFITS(path))
	before, err := fits.ReadImage(path)
	require.NoError(t, err)
	_, err = flattenSkyLuminance(path, 64)
	require.NoError(t, err)
	after, err := fits.ReadImage(path)
	require.NoError(t, err)
	contrast := func(im *fits.Image) float64 {
		var blob, sky []float64
		for x := 110; x < 140; x++ {
			for y := 260; y < 290; y++ {
				blob = append(blob, float64(im.Pix[0][y*scW+x]))
			}
			for y := 320; y < 350; y++ { // sky in the SAME columns — the ramp varies with x only
				sky = append(sky, float64(im.Pix[0][y*scW+x]))
			}
		}
		return median64(blob) - median64(sky)
	}
	assert.InDelta(t, contrast(before), contrast(after), 0.003)
}

// TestFlattenSkyLuminance_NoMoatInsideGalaxy: a large faint extended object must NOT get a dark
// moat carved through its envelope. The object sits at the ramp's center (local glow ≈ 0), so a
// correct flatten changes it by ≈ nothing; the old bug measured the faint envelope as elevated
// "sky" and subtracted it (delta ~ +0.01).
func TestFlattenSkyLuminance_NoMoatInsideGalaxy(t *testing.T) {
	im := synthLumRamp(t, 1)
	cx, cy := scW/2, scH/2
	for y := 0; y < scH; y++ {
		for x := 0; x < scW; x++ {
			d2 := float64((x-cx)*(x-cx) + (y-cy)*(y-cy))
			im.Pix[0][y*scW+x] += float32(0.03 * math.Exp(-d2/(2*30*30))) // broad faint disc, peak ≈7σ
		}
	}
	path := filepath.Join(t.TempDir(), "galaxy.fits")
	require.NoError(t, im.WriteFITS(path))
	before, err := fits.ReadImage(path)
	require.NoError(t, err)
	// Grid 32 keeps the test frame's tile grid production-proportioned (the adaptive exclusion
	// needs surviving sky tiles around the object, as a real 4k frame always has).
	_, err = flattenSkyLuminance(path, 32)
	require.NoError(t, err)
	after, err := fits.ReadImage(path)
	require.NoError(t, err)

	// The flatten shifts the object exactly like the sky around it; the envelope must not be dug
	// any DEEPER than its own columns' sky — that excess is the moat. The per-column sky reference
	// (same x, sky rows above the object) isolates object-local digging from the legitimate ramp
	// removal, which varies with x across the box.
	ref := make([]float64, scW)
	for x := cx - 100; x < cx+100; x++ {
		var col []float64
		for y := 20; y < 80; y++ {
			i := y*scW + x
			col = append(col, float64(before.Pix[0][i]-after.Pix[0][i]))
		}
		ref[x] = median64(col)
	}
	worst := 0.0
	for y := cy - 100; y < cy+100; y++ {
		for x := cx - 100; x < cx+100; x++ {
			i := y*scW + x
			worst = math.Max(worst, float64(before.Pix[0][i]-after.Pix[0][i])-ref[x])
		}
	}
	assert.Less(t, worst, 0.005, "flatten dug into the galaxy envelope (moat)")
}

func TestFlattenSkyLuminance_NoOps(t *testing.T) {
	t.Run("zero grid", func(t *testing.T) {
		note, err := flattenSkyLuminance("/nonexistent.fits", 0)
		require.NoError(t, err)
		assert.Empty(t, note)
	})
	t.Run("unreadable path errors softly", func(t *testing.T) {
		_, err := flattenSkyLuminance(filepath.Join(t.TempDir(), "missing.fits"), 64)
		assert.Error(t, err) // caller turns this into a note; the TIFF still gets saved
	})
}
