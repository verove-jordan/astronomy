package pipeline

import (
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/mode"
)

// The synthetic frames below reproduce the regime the trim was written for, measured on job 629's
// M31 master: a sky at 0.245 whose per-pixel noise is a few parts in 100 000, so the "invisible"
// 0.2% edge excess is tens of sigma. Getting that ratio right is the whole point — a test with
// loud noise would pass with any threshold.
const (
	testSky   = 0.245
	testNoise = 2e-5 // per-pixel sigma
	testSkirt = 5e-4 // peak edge excess = 25 sigma per pixel, ~250x the line-to-line scatter
)

// skyFrame is a flat, noisy, featureless sky — the thing every other case is a perturbation of.
func skyFrame(t *testing.T, w, h int) *fits.Image {
	t.Helper()
	im := fits.NewImage(w, h, 3)
	rng := rand.New(rand.NewSource(7))
	for i := 0; i < w*h; i++ {
		v := float32(testSky + rng.NormFloat64()*testNoise)
		for c := range im.Pix {
			im.Pix[c][i] = v
		}
	}
	return im
}

// addDriftEdge paints the artefact a drifting session leaves on its own stack: a wedge of pixels no
// frame covered (dead, tapering away with x) and, beside it, a band that is both BRIGHTER and
// NOISIER than the interior, because fewer frames reached it. All three marks are faithful to the
// master this was written from — the dead wedge stopped at x≈50, the excess ran to x≈190, and the
// noise there was 8x the interior's.
func addDriftEdge(im *fits.Image, wedgePx, skirtPx int) {
	rng := rand.New(rand.NewSource(11))
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			i := y*im.W + x
			deadTo := int(float64(wedgePx) * (1 - float64(y)/float64(im.H)) * 1.4) // a wedge, not a strip
			if x < deadTo {
				for c := range im.Pix {
					im.Pix[c][i] = 0
				}
				continue
			}
			if x >= skirtPx {
				continue
			}
			peak := float64(skirtPx) / 4
			d := (float64(x) - peak) / (float64(skirtPx) / 5.7)
			shallow := math.Exp(-0.5 * d * d)
			lift := testSkirt * shallow
			extra := rng.NormFloat64() * testNoise * 7 * shallow // depth falls off, noise rises
			for c := range im.Pix {
				im.Pix[c][i] += float32(lift + extra)
			}
		}
	}
}

// addEdgeObject paints a nebula that runs off the left of the frame: bright at the edge, same noise
// as the rest of the stack. It is the case the level test alone gets wrong.
func addEdgeObject(im *fits.Image) {
	for y := 0; y < im.H; y++ {
		for x := 0; x < im.W; x++ {
			dx := float64(x) / 260
			dy := float64(y-im.H/2) / 400
			for c := range im.Pix {
				im.Pix[c][y*im.W+x] += float32(0.06 * math.Exp(-0.5*(dx*dx+dy*dy)))
			}
		}
	}
}

func TestMeasureEdgeTrim(t *testing.T) {
	const w, h = 2400, 900 // the drift skirt below is 200 px: on a real 6064 px master that is 3%

	tests := []struct {
		name    string
		build   func(t *testing.T) *fits.Image
		wantCut bool
		capped  bool
	}{
		{
			// The failure this exists for.
			name: "drift wedge and its skirt are cut",
			build: func(t *testing.T) *fits.Image {
				im := skyFrame(t, w, h)
				addDriftEdge(im, 40, 200)
				return im
			},
			wantCut: true,
		},
		{
			name:    "a clean frame is left alone",
			build:   func(t *testing.T) *fits.Image { return skyFrame(t, w, h) },
			wantCut: false,
		},
		{
			// The guard that makes the trim safe to leave on: a real light-pollution dome is a huge
			// signal (10% of the sky here, 5000x the line scatter) and must survive untouched. It does
			// because the border test measures departure from a fitted TREND, not from a level.
			name: "a real light-pollution gradient survives",
			build: func(t *testing.T) *fits.Image {
				im := skyFrame(t, w, h)
				for y := 0; y < h; y++ {
					for x := 0; x < w; x++ {
						u := float64(x) / float64(w)
						v := float64(y) / float64(h)
						g := 0.10*testSky*(u*u*0.6+u*0.4) + 0.04*testSky*v
						for c := range im.Pix {
							im.Pix[c][y*w+x] += float32(g)
						}
					}
				}
				return im
			},
			wantCut: false,
		},
		{
			// An object in the MIDDLE is unreachable by construction — the walk starts at the frame
			// edge and stops after a run of clean lines — but it can still wreck the trim indirectly,
			// by capturing the trend fit and sending its extrapolation off at the corners. It does not,
			// because the fit is clipped and spans the whole profile.
			name: "a bright object in the middle is not a border",
			build: func(t *testing.T) *fits.Image {
				im := skyFrame(t, w, h)
				for y := 0; y < h; y++ {
					for x := 0; x < w; x++ {
						dx, dy := float64(x-w/2)/110, float64(y-h/2)/70
						for c := range im.Pix {
							im.Pix[c][y*w+x] += float32(0.30 * math.Exp(-0.5*(dx*dx+dy*dy)))
						}
					}
				}
				return im
			},
			wantCut: false,
		},
		{
			// The case the level test alone gets wrong, and the reason the noise clause exists: a
			// nebula running off the frame lifts the edge background by 25% of the sky — thousands of
			// sigma — and must be kept, because it is as deep as everything else in the stack.
			name: "an object running off the frame edge is kept",
			build: func(t *testing.T) *fits.Image {
				im := skyFrame(t, w, h)
				addEdgeObject(im)
				return im
			},
			wantCut: false,
		},
		{
			// And the same object does not hide a real ragged edge underneath it.
			name: "a ragged edge under an object is still cut",
			build: func(t *testing.T) *fits.Image {
				im := skyFrame(t, w, h)
				addEdgeObject(im)
				addDriftEdge(im, 40, 200)
				return im
			},
			wantCut: true,
		},
		{
			// A frame that is border all the way in must not be eaten: the run keeps the field, says
			// so, and the sky model takes its chances.
			name: "an absurd measurement is capped, not obeyed",
			build: func(t *testing.T) *fits.Image {
				im := skyFrame(t, w, h)
				addDriftEdge(im, 900, w) // "skirt" across the entire width
				return im
			},
			wantCut: true,
			capped:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			im := tt.build(t)
			got, capped := measureEdgeTrim(im)

			assert.Equal(t, tt.capped, capped, "cap flag")
			assert.GreaterOrEqual(t, got.X0, 0)
			assert.LessOrEqual(t, got.X1, im.W)
			// Whatever it decides, it may never take more than the cap off a side.
			assert.LessOrEqual(t, got.X0, int(edgeMaxTrimFrac*float64(im.W)), "left trim within the cap")
			assert.LessOrEqual(t, im.W-got.X1, int(edgeMaxTrimFrac*float64(im.W)), "right trim within the cap")
			assert.LessOrEqual(t, got.Y0, int(edgeMaxTrimFrac*float64(im.H)), "top trim within the cap")

			if !tt.wantCut {
				assert.Equal(t, edgeRect{0, 0, im.W, im.H}, got, "nothing here is a stacking edge")
				return
			}
			assert.Less(t, got.area(), im.W*im.H, "something should have been cut")
		})
	}
}

// The trim must land past the skirt, not merely past the dead pixels — the dead wedge ends at 56 px
// here and the excess runs to 200, and it was the excess that wrecked the sky fit.
func TestMeasureEdgeTrim_LandsPastTheSkirtNotJustTheDeadPixels(t *testing.T) {
	im := skyFrame(t, 2400, 900)
	addDriftEdge(im, 40, 200)

	got, capped := measureEdgeTrim(im)
	require.False(t, capped)

	assert.Greater(t, got.X0, 100, "trimming only the dead wedge leaves the +25 sigma band in")
	assert.Less(t, got.X0, 300, "and it must not keep going into clean sky")
	assert.Equal(t, 2400, got.X1, "the far edge is clean")
	assert.Equal(t, 0, got.Y0)
	assert.Equal(t, 900, got.Y1)
}

// Idempotence is the honest statement of "the edge is gone": re-measuring what was kept must find
// nothing left to cut.
func TestMeasureEdgeTrim_IsIdempotent(t *testing.T) {
	im := skyFrame(t, 2400, 900)
	addDriftEdge(im, 40, 200)

	first, _ := measureEdgeTrim(im)
	cropped := subImage(im, first)

	again, capped := measureEdgeTrim(cropped)
	assert.False(t, capped)
	assert.Equal(t, edgeRect{0, 0, cropped.W, cropped.H}, again, "the kept field is already clean")
}

// subImage is the in-memory equivalent of cropFITS, for tests that re-measure what a crop kept.
func subImage(im *fits.Image, r edgeRect) *fits.Image {
	out := fits.NewImage(r.w(), r.h(), len(im.Pix))
	for c := range im.Pix {
		for y := 0; y < r.h(); y++ {
			copy(out.Pix[c][y*r.w():(y+1)*r.w()], im.Pix[c][(r.Y0+y)*im.W+r.X0:(r.Y0+y)*im.W+r.X1])
		}
	}
	return out
}

func TestApplyEdgeCrop(t *testing.T) {
	writeMaster := func(t *testing.T, dir, base string, im *fits.Image) {
		t.Helper()
		require.NoError(t, im.WriteFITS(filepath.Join(dir, base+".fits")))
	}

	t.Run("crops the combine inputs and leaves the masters alone", func(t *testing.T) {
		dir := t.TempDir()
		im := skyFrame(t, 2400, 900)
		addDriftEdge(im, 40, 200)
		writeMaster(t, dir, "master_RGB", im)

		res := &Result{}
		opts := Options{Preset: &mode.Preset{EdgeCrop: true}}
		got := applyEdgeCrop(opts, res, map[string]string{"RGB": "master_RGB"}, dir)

		require.Equal(t, map[string]string{"RGB": "trim_RGB"}, got)
		require.NotNil(t, res.EdgeCrop)
		assert.True(t, res.EdgeCrop.Applied)
		assert.Less(t, res.EdgeCrop.W, 2400)

		cropped, err := fits.ReadImage(filepath.Join(dir, "trim_RGB.fits"))
		require.NoError(t, err)
		assert.Equal(t, res.EdgeCrop.W, cropped.W)

		// A refine/supervise re-entry re-reads the master, so it must still be the frame the run
		// stacked — full size, dead wedge and all.
		orig, err := fits.ReadImage(filepath.Join(dir, "master_RGB.fits"))
		require.NoError(t, err)
		assert.Equal(t, 2400, orig.W)
	})

	t.Run("channels are cut to their common field", func(t *testing.T) {
		dir := t.TempDir()
		wide := skyFrame(t, 2400, 900)
		addDriftEdge(wide, 40, 200)
		narrow := skyFrame(t, 2400, 900)
		addDriftEdge(narrow, 20, 90)
		writeMaster(t, dir, "master_R", wide)
		writeMaster(t, dir, "master_G", narrow)

		res := &Result{}
		opts := Options{Preset: &mode.Preset{EdgeCrop: true}}
		got := applyEdgeCrop(opts, res, map[string]string{"R": "master_R", "G": "master_G"}, dir)
		require.Len(t, got, 2)

		r, err := fits.ReadImage(filepath.Join(dir, "trim_R.fits"))
		require.NoError(t, err)
		g, err := fits.ReadImage(filepath.Join(dir, "trim_G.fits"))
		require.NoError(t, err)
		assert.Equal(t, r.W, g.W, "one geometry, or the colour combine cannot run")
		assert.Equal(t, r.H, g.H)

		// The intersection is the WIDER channel's cut: the common clean field is the smaller one.
		alone, _ := measureEdgeTrim(wide)
		assert.Equal(t, alone.w(), r.W)
	})

	t.Run("a clean stack is passed through untouched", func(t *testing.T) {
		dir := t.TempDir()
		writeMaster(t, dir, "master_RGB", skyFrame(t, 2400, 900))

		res := &Result{}
		opts := Options{Preset: &mode.Preset{EdgeCrop: true}}
		in := map[string]string{"RGB": "master_RGB"}
		assert.Equal(t, in, applyEdgeCrop(opts, res, in, dir))
		assert.Nil(t, res.EdgeCrop)
		_, err := os.Stat(filepath.Join(dir, "trim_RGB.fits"))
		assert.True(t, os.IsNotExist(err), "no needless copy of a 24 Mpx master")
	})

	t.Run("disabled by the preset", func(t *testing.T) {
		dir := t.TempDir()
		im := skyFrame(t, 2400, 900)
		addDriftEdge(im, 40, 200)
		writeMaster(t, dir, "master_RGB", im)

		in := map[string]string{"RGB": "master_RGB"}
		assert.Equal(t, in, applyEdgeCrop(Options{Preset: &mode.Preset{}}, &Result{}, in, dir))
	})
}
