package solar

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestMeasure_Live scores whatever FITS files it is pointed at, so a finished master can be compared
// against the frames that went into it without re-running anything.
//
//	ASTRO_SOLAR_MEASURE="a.fits,b/*.fits" go test ./internal/solar -run Measure_Live -v
func TestMeasure_Live(t *testing.T) {
	spec := os.Getenv("ASTRO_SOLAR_MEASURE")
	if spec == "" {
		t.Skip("set ASTRO_SOLAR_MEASURE=<comma-separated files or globs>")
	}
	for _, pat := range strings.Split(spec, ",") {
		paths, err := filepath.Glob(strings.TrimSpace(pat))
		require.NoError(t, err)
		sort.Strings(paths)
		if len(paths) == 0 {
			t.Logf("%s: no match", pat)
			continue
		}
		var best, sum float64
		var bestPath string
		var n int
		for _, p := range paths {
			im, err := fits.ReadImage(p)
			require.NoError(t, err)
			mono := firstPlane(im)
			l, ok := FitLimb(mono)
			if !ok {
				t.Logf("  %s: no limb", filepath.Base(p))
				continue
			}
			d := FrameSharpness(mono, l)
			if len(paths) == 1 {
				t.Logf("%-46s detail %.5f  r=%.1f  %dx%d", filepath.Base(p), d, l.R, mono.W, mono.H)
			}
			if d > best {
				best, bestPath = d, p
			}
			sum += d
			n++
		}
		if n > 1 {
			t.Logf("%-30s n=%-4d best %.5f (%s)  mean %.5f",
				pat, n, best, filepath.Base(bestPath), sum/float64(n))
		}
	}
}
