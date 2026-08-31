package pipeline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/nightscape"
	"github.com/verove-jordan/astronomy/internal/starfield"
)

// TestZZGroundMask reports what the ground mask does to a real panel's detections.
//
// It exists to check the fix on the panel that motivated it — the low pointing whose field is half
// land and sea, with a town's chain of street lamps through it, where the solver was handed 1535
// objects and failed. The question is whether masking the ground leaves a usable star list.
//
//	ASTRO_PANEL_DIR=<abs .../panels/p02> go test ./internal/pipeline/ -run TestZZGroundMask -v
func TestZZGroundMask(t *testing.T) {
	dir := os.Getenv("ASTRO_PANEL_DIR")
	if dir == "" {
		t.Skip("set ASTRO_PANEL_DIR to a panel output directory")
	}
	im, err := fits.ReadImage(filepath.Join(dir, "stacked_sky.fits"))
	if err != nil {
		t.Skip(err)
	}
	for _, sig := range []float64{8, 6, 5, 4, 3} {
		d := starfield.Detect(im.Pix[1], im.W, im.H,
			starfield.Options{Sigma: sig, BoxRadius: 6, MinSeparation: 10, Max: panoMaxStars})
		k, dr := skyOnlyDetections(d, dir, im.W, im.H)
		fmt.Printf("  sigma %.0f -> %5d detections, %5d in sky (%d dropped)\n", sig, len(d), len(k), dr)
	}
	det := starfield.Detect(im.Pix[1], im.W, im.H,
		starfield.Options{Sigma: 8, BoxRadius: 6, MinSeparation: 10, Max: panoMaxStars})

	kept, dropped := skyOnlyDetections(det, dir, im.W, im.H)

	// How much of the frame the mask calls sky at all — the number that decides whether this panel
	// could ever have been solved from its whole field.
	skyFrac := -1.0
	if m, err := fits.ReadImage(filepath.Join(dir, "sky_alpha.fits")); err == nil {
		if b, rerr := os.ReadFile(filepath.Join(dir, "grade.orient")); rerr == nil && len(b) > 0 {
			m = nightscape.Orient(m, strings.TrimSpace(string(b)))
		}
		n := 0
		for _, v := range m.Pix[0] {
			if v >= 0.9 {
				n++
			}
		}
		skyFrac = 100 * float64(n) / float64(len(m.Pix[0]))
	}
	fmt.Printf("%s: %dx%d, mask calls %.1f%% of it sky\n", filepath.Base(dir), im.W, im.H, skyFrac)
	fmt.Printf("  detections %d -> %d kept, %d dropped as ground\n", len(det), len(kept), dropped)
	if dropped == 0 && len(kept) == len(det) {
		fmt.Println("  (the mask was refused — too few would have been left, or it did not match)")
	}
}
