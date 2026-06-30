package nightscape

import (
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestComputeCleanSkyStack_RejectsOutlier verifies the sigma-clipped stack rejects a hot pixel present
// in a single frame — the grain fix. 12 uniform frames, one with a bright spike at one pixel: the result
// there must be the clean value (~0.30), NOT the plain mean (~0.354) that included the spike.
func TestComputeCleanSkyStack_RejectsOutlier(t *testing.T) {
	dir := t.TempDir()
	const n, w, h = 12, 8, 8
	const hot = 10 // pixel index carrying the outlier
	var paths []string
	for f := 0; f < n; f++ {
		im := fits.NewImage(w, h, 3)
		for c := 0; c < 3; c++ {
			for i := range im.Pix[c] {
				im.Pix[c][i] = 0.3
			}
		}
		if f == 5 { // one frame: a neutral white hot pixel (not green-dominant, so the σ-clip is what rejects it)
			im.Pix[0][hot], im.Pix[1][hot], im.Pix[2][hot] = 0.95, 0.95, 0.95
		}
		p := filepath.Join(dir, fmt.Sprintf("r_light_%05d.fits", f))
		if err := im.WriteFITS(p); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	sky, err := computeCleanSkyStack(paths, fits.NewImage(w, h, 3), 55, false, 1.3, 0.3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := sky.Pix[1][hot]; math.Abs(float64(got)-0.3) > 0.02 {
		t.Fatalf("outlier not rejected: pixel = %.4f, want ~0.30 (a plain mean would be ~0.354)", got)
	}
	if got := sky.Pix[1][20]; math.Abs(float64(got)-0.3) > 1e-3 {
		t.Fatalf("clean pixel = %.4f, want 0.30", got)
	}
}
