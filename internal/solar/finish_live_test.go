package solar

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestFinish_Live re-renders a persisted window master from a real run, which is the same path the
// Refine panel and the supervised auto-tuner take. It is the fast way to judge the finish: seconds,
// against the minutes a full re-ingest and re-stack would cost. Opt-in:
//
//	ASTRO_SOLAR_MASTER=output/.../master_w02.fits ASTRO_SOLAR_OUT=/tmp/out.png \
//	  go test ./internal/solar -run Finish_Live -v
func TestFinish_Live(t *testing.T) {
	master := os.Getenv("ASTRO_SOLAR_MASTER")
	dst := os.Getenv("ASTRO_SOLAR_OUT")
	if master == "" || dst == "" {
		t.Skip("set ASTRO_SOLAR_MASTER=<master.fits> and ASTRO_SOLAR_OUT=<out.png>")
	}
	if !filepath.IsAbs(master) {
		master = filepath.Join("..", "..", master)
	}
	im, err := fits.ReadImage(master)
	require.NoError(t, err)
	mono := firstPlane(im)

	l, ok := FitLimb(mono)
	require.True(t, ok, "the master must carry a fittable limb")
	t.Logf("master %dx%d, disc r=%.1f px at (%.1f, %.1f), arc %.0f°",
		mono.W, mono.H, l.R, l.CX, l.CY, l.ArcDeg)

	o := DefaultFinish()
	if p := os.Getenv("ASTRO_SOLAR_PALETTE"); p != "" {
		o.Palette = p
	}
	out := Finish(mono, l, o)
	require.NoError(t, WritePNG(out, dst))

	// A finished solar image must have a dark sky and a bright disc; those two being the wrong way
	// round, or equal, is the signature of a stretch anchored on the wrong statistic.
	disc := annulusMedian(out.Pix[0], out.W, out.H, l, 0.2, 0.5)
	sky := annulusMedian(out.Pix[0], out.W, out.H, l, 1.25, 1.35)
	t.Logf("rendered disc %.3f vs sky %.3f → %s", disc, sky, dst)
	require.Greater(t, disc, 3*sky, "the disc must stand well clear of the sky")
}
