package nightscape

import (
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestComputeCleanSkyStack_SkyPercentile covers the second place the recipe assumed a landscape.
//
// Every frame normally contributes only its brightest pixels, which is how a drifting tree or
// rooftop is kept out of the sky. Point the camera at the zenith and that rule turns on the sky
// itself: only the pixels tracing the Milky Way clear the threshold, the darker sky around them
// falls below the coverage floor and is replaced by an extrapolated fill, and the panel comes out
// as a bright band ringed by a black shell. Selecting every pixel is what a frame with no
// foreground needs.
func TestComputeCleanSkyStack_SkyPercentile(t *testing.T) {
	// Three identical frames: a bright half and a dim half, both real sky at the ~1.5x contrast a
	// Milky Way band actually has, plus a dark obstruction in one corner.
	const w, h = 40, 40
	const bright, dim, obstruction = float32(0.9), float32(0.6), float32(0.05)
	dir := t.TempDir()
	var paths []string
	for i := 0; i < 3; i++ {
		im := fits.NewImage(w, h, 3)
		for y := 0; y < h; y++ {
			v := bright
			if y >= h/2 {
				v = dim
			}
			for x := 0; x < w; x++ {
				val := v
				if x < w/8 && y < h/8 {
					val = obstruction
				}
				for c := 0; c < 3; c++ {
					im.Pix[c][y*w+x] = val
				}
			}
		}
		p := filepath.Join(dir, fmt.Sprintf("r_light_%05d.fit", i))
		require.NoError(t, im.WriteFITS(p))
		paths = append(paths, p)
	}
	fg, err := fits.ReadImage(paths[0])
	require.NoError(t, err)

	// Sample well inside each half so the mask's feathering cannot reach the probe.
	topIdx := (h / 4) * w
	botIdx := (3 * h / 4) * w

	t.Run("selecting every pixel keeps the dim half at its true level", func(t *testing.T) {
		sky, _, _, err := computeCleanSkyStack(paths, fg, 0, false, 1.3, 0.5, nil)
		require.NoError(t, err)

		assert.InDelta(t, bright, sky.Pix[0][topIdx], 0.02, "bright half")
		assert.InDelta(t, dim, sky.Pix[0][botIdx], 0.02, "dim half must survive as sky, not be extrapolated")
	})

	t.Run("an obstruction is still refused even when every pixel is asked for", func(t *testing.T) {
		// The corner is far below the frame's own sky level, so the dark floor drops it and it is
		// filled from the surrounding sky rather than averaged in as a dark blob. This is what lets
		// a panel that is sky apart from a rock in one corner take the sky-only path at all.
		sky, coverage, _, err := computeCleanSkyStack(paths, fg, 0, false, 1.3, 0.5, nil)
		require.NoError(t, err)

		corner := (h/16)*w + w/16
		// The returned map counts frames that REACHED the pixel, not frames that contributed sky to
		// it — the obstruction is opaque, not absent, so all three frames reached it. Conflating the
		// two is what cropped a landscape panel to a sliver: over a horizon most of the frame
		// contributes no sky at all, yet every frame reaches it.
		assert.Equal(t, float64(len(paths)), coverage[corner], "every frame reached the corner")
		assert.Greater(t, sky.Pix[0][corner], obstruction*2, "the hole should be filled from sky, not left dark")
	})

	t.Run("the landscape rule discards the dim half", func(t *testing.T) {
		// What the percentile does, kept as documentation of why nothing passes it any more. It was
		// once thought correct over a real horizon and wrong only at the zenith; it is wrong in both
		// places, for the reason shown here — it drops a share of the pixels, and the dim half of a
		// real sky is in that share. compose now passes 0 on both paths and lets the dark floor do
		// the rejecting. See TestComputeCleanSkyStack_DarkFloorSeparatesGroundFromSky.
		sky, _, _, err := computeCleanSkyStack(paths, fg, 55, true, 1.3, 0.5, nil)
		require.NoError(t, err)

		assert.InDelta(t, bright, sky.Pix[0][topIdx], 0.02, "bright half")
		assert.Greater(t, math.Abs(float64(sky.Pix[0][botIdx]-dim)), 0.02,
			"the dim half is expected to be dropped here, not reproduced")
	})
}

// TestComputeCleanSkyStack_DarkFloorSeparatesGroundFromSky pins the decision that replaced the
// percentile, at the margins the real data actually has.
//
// The existing fixture above puts the obstruction at 0.05 against sky at 0.6–0.9, a gap of more than
// ten to one, which almost any test would pass. The panel this was fixed on is far tighter: measured
// on p02, ground p99 = 0.0023 and the faintest sky p1 = 0.0051, so barely a factor of two, with the
// dark floor (half the frame median) at 0.0030 in between. Those are the numbers worth protecting —
// if the floor ever drifts, it fails here first.
func TestComputeCleanSkyStack_DarkFloorSeparatesGroundFromSky(t *testing.T) {
	// Levels taken from the measurement, scaled by 100 so a float32 FITS round-trip cannot be what
	// decides the test. The frame is two thirds sky so its median lands in the sky, as p02's does.
	const w, h = 48, 48
	const skyFaint, skyBright, ground = float32(0.51), float32(0.90), float32(0.23)
	const groundFrom = 2 * h / 3

	dir := t.TempDir()
	var paths []string
	for i := 0; i < 3; i++ {
		im := fits.NewImage(w, h, 3)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				v := skyFaint
				switch {
				case y >= groundFrom:
					v = ground
				case y < h/3:
					v = skyBright // the band's half, so the median sits between the two sky levels
				}
				for c := 0; c < 3; c++ {
					im.Pix[c][y*w+x] = v
				}
			}
		}
		p := filepath.Join(dir, fmt.Sprintf("r_light_%05d.fit", i))
		require.NoError(t, im.WriteFITS(p))
		paths = append(paths, p)
	}
	fg, err := fits.ReadImage(paths[0])
	require.NoError(t, err)

	sky, _, _, err := computeCleanSkyStack(paths, fg, 0, true, 1.3, 0.5, nil)
	require.NoError(t, err)

	faintIdx := (h/2)*w + w/2  // in the faint sky band, well clear of both boundaries
	groundIdx := (h-3)*w + w/2 // deep in the ground

	t.Run("the faintest sky survives at its own level", func(t *testing.T) {
		assert.InDelta(t, skyFaint, sky.Pix[0][faintIdx], 0.02,
			"faint sky must be kept, not dropped below the coverage floor and extrapolated")
	})

	t.Run("the ground is refused and filled from the sky above it", func(t *testing.T) {
		assert.Greater(t, sky.Pix[0][groundIdx], ground*1.5,
			"the ground must not be averaged in at its own level")
	})
}
