package platesolve_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/device/sim"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/platesolve"
	"github.com/verove-jordan/astronomy/internal/postprocess"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// Can Siril plate-solve a simulated frame?
//
// Measured answer, on this host with Siril 1.4.x: NO — and the reason is worth recording, because
// it bounds what can be verified without a telescope.
//
// Plate solving matches the brightest DETECTED stars against a reference catalogue of the REAL sky.
// The simulator draws its stars from the bundled `deepstars` catalogue, which stops near magnitude
// 9 and puts only about 10–20 stars in a one-degree field — fewer than Siril can pick and match.
// The synthetic faint population (sim/faint.go) makes frames look and measure realistically, but it
// cannot fix this: fabricated stars are in nobody's reference catalogue, so adding bright ones only
// trades "there are not enough stars picked in the image" for "the image could not be aligned with
// the reference stars". Both were observed. Solving needs a genuinely deeper catalogue — a Gaia or
// Tycho subset — not more synthetic light.
//
// Consequence, stated plainly: plate-solve centring and tracking measurement are unit-tested against
// a substituted solver, but their end-to-end path through real Siril is exercised only on sky.

func sirilRunner(t *testing.T) *siril.Runner {
	t.Helper()
	bin := os.Getenv("SIRIL_BIN")
	if bin == "" {
		bin = "/Applications/Siril.app/Contents/MacOS/siril-cli"
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skip("siril-cli not installed on this host")
	}
	return siril.New(bin, siril.Limits{})
}

// simFrame renders one exposure of a real sky position and writes it where Siril can read it.
func simFrame(t *testing.T, dir string, raDeg, decDeg float64, exposureUs, gain int64) string {
	t.Helper()
	ctx := context.Background()
	world := sim.NewWorld(sim.Config{})
	mount := sim.NewMount(world)
	require.NoError(t, mount.Connect(ctx))
	require.NoError(t, mount.Sync(ctx, raDeg, decDeg))

	cam := sim.NewCamera(world)
	require.NoError(t, cam.Connect(ctx))
	t.Cleanup(func() { _ = cam.Close() })

	require.NoError(t, cam.SetControl(device.ControlExposure, exposureUs, false))
	require.NoError(t, cam.SetControl(device.ControlGain, gain, false))
	require.NoError(t, cam.StartExposure(ctx, false))

	deadline := time.Now().Add(60 * time.Second)
	for {
		st, err := cam.ExposureState()
		require.NoError(t, err)
		if st == device.ExposureSuccess {
			break
		}
		require.NotEqual(t, device.ExposureFailed, st)
		require.True(t, time.Now().Before(deadline), "the simulated exposure never finished")
		time.Sleep(50 * time.Millisecond)
	}
	frame, err := cam.Download(ctx)
	require.NoError(t, err)
	require.NotNil(t, frame)

	path := filepath.Join(dir, "sim.fit")
	require.NoError(t, fits.Write16(path, frame.Width, frame.Height, frame.Pix, nil))
	return path
}

// What the simulator CAN do: produce frames full of measurable stars. That is what the focus meter,
// the live histogram and star matching actually need, and it is what the synthetic population is
// there to provide.
func TestSim_FrameIsFullOfMeasurableStars(t *testing.T) {
	path := simFrame(t, t.TempDir(), 350.85, 58.8, 10_000_000, 120)

	img, err := fits.ReadImage(path)
	require.NoError(t, err)

	stars := postprocess.DetectStarPeaks(img, postprocess.StarDetectOptions{
		MaxStars: 2000,
		Sigma:    3.5,
		// SatLevel defaults to 0.9, which assumes NORMALISED pixels. A 16-bit FITS reads back in raw
		// ADU, so that default would treat every single star as saturated and return nothing.
		SatLevel: 1 << 20,
		// The default half-max ceiling is tuned for tight, well-focused stars; raising it here means
		// the count reflects what is in the sky rather than the detector's idea of good focus.
		MaxHalfMax: 12,
	})
	assert.Greater(t, len(stars), 100,
		"the synthetic faint population must fill the field — this is what the focus meter measures")
}

// The known gap, kept as an executable record rather than a red failure. Remove the skip once a
// deeper reference catalogue lands, and this becomes the end-to-end centring test the plan wanted.
func TestSolve_SimulatedFrameIsNotYetSolvable(t *testing.T) {
	t.Skip("simulated frames are not plate-solvable: the bundled catalogue reaches only ~mag 9, " +
		"leaving too few real stars for Siril to match. Needs a Gaia/Tycho subset.")

	runner := sirilRunner(t)
	const ra, dec = 350.85, 58.8
	path := simFrame(t, t.TempDir(), ra, dec, 5_000_000, 50)

	solver := platesolve.New(runner, siril.SolveOptions{})
	res, err := solver.Solve(context.Background(), path, platesolve.Hint{
		RADeg: ra, DecDeg: dec, HasHint: true, FocalMM: 740, PixelUm: 3.8,
	})
	require.NoError(t, err)
	assert.InDelta(t, ra, res.RADeg, 0.2)
	assert.InDelta(t, dec, res.DecDeg, 0.2)
}
