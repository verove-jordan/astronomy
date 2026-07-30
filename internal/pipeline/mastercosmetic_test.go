package pipeline

import (
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestRepairIsolatedOutliers pins the cosmetic contract: a LONE hot pixel and a LONE cold pixel are
// median-repaired, while a compact 2-pixel "star" (bright core with a bright neighbour) survives
// untouched — the isolation gate is what separates defects from real undersampled detail.
func TestRepairIsolatedOutliers(t *testing.T) {
	const w, h = 64, 64
	im := fits.NewImage(w, h, 1)
	for i := range im.Pix[0] {
		im.Pix[0][i] = 0.10
	}
	sigma := 0.001

	hot := 20*w + 20
	cold := 40*w + 40
	starA := 30*w + 30
	starB := 30*w + 31
	im.Pix[0][hot] = 0.90
	im.Pix[0][cold] = 0.001
	im.Pix[0][starA] = 0.90
	im.Pix[0][starB] = 0.70

	n := repairIsolatedOutliers(im, sigma)
	if n != 2 {
		t.Fatalf("repaired %d pixels, want exactly the 2 planted defects", n)
	}
	if got := im.Pix[0][hot]; got != 0.10 {
		t.Fatalf("hot pixel not repaired to the neighbour median: %v", got)
	}
	if got := im.Pix[0][cold]; got != 0.10 {
		t.Fatalf("cold pixel not repaired: %v", got)
	}
	if im.Pix[0][starA] != 0.90 || im.Pix[0][starB] != 0.70 {
		t.Fatalf("compact star was modified: %v %v", im.Pix[0][starA], im.Pix[0][starB])
	}
}
