package pipeline

// Harness: measure the stacking-edge trim on a REAL channel master and print the profile it read.
// Off by default — it needs a run's output on disk.
//
//	ASTRO_EDGE_MASTER=output/<run>/master_RGB.fits go test ./internal/pipeline -run EdgeTrimOnRealMaster -v

import (
	"fmt"
	"math"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

func TestEdgeTrimOnRealMaster(t *testing.T) {
	path := os.Getenv("ASTRO_EDGE_MASTER")
	if path == "" {
		t.Skip("set ASTRO_EDGE_MASTER to a stacked master")
	}
	im, err := fits.ReadImage(path)
	require.NoError(t, err)

	sky := interiorSky(im)
	st := lineProfiles(im, edgeRect{0, 0, im.W, im.H}, true, float32(edgeDeadLevel*sky))
	trend, scale, ok := interiorTrend(st.Bg)
	require.True(t, ok, "the column profile must be fittable")
	ref := interiorNoise(st.Noise)

	fmt.Printf("%s %dx%dx%d sky=%.6f line-scale=%.3g interior-noise=%.3g\n", path, im.W, im.H, im.C, sky, scale, ref)
	for _, x := range []int{0, 10, 20, 30, 50, 80, 120, 160, 200, 300, 600, im.W / 2, im.W - 1} {
		if x >= len(st.Bg) {
			continue
		}
		fmt.Printf("  x=%-5d bg=%.6f  %+8.1f sigma  dead=%.3f  noise=%.2fx\n",
			x, st.Bg[x], (st.Bg[x]-trend(x))/scale, st.Dead[x], st.Noise[x]/ref)
	}
	r, capped := measureEdgeTrim(im)
	fmt.Printf("  => trim left %d right %d top %d bottom %d (capped=%v) — keeps %.2f%% of the frame\n",
		r.X0, im.W-r.X1, r.Y0, im.H-r.Y1, capped, 100*float64(r.area())/float64(im.W*im.H))

	require.False(t, math.IsNaN(scale))
}
