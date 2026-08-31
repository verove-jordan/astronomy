package skypano

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestZZSinglePanelRender separates the two ways the mosaic could be smearing stars: the resampling
// onto a coarser canvas, or the panels disagreeing about where a star is. One panel rendered alone
// has no second panel to disagree with, so if its stars come out round the geometry is fine and the
// blend is at fault.
func TestZZSinglePanelRender(t *testing.T) {
	requireHarness(t)
	var panels []Panel
	for _, name := range []string{"p01", "p03", "p04", "p05", "p06", "p07", "p08"} {
		b, err := os.ReadFile(scratch + "cam_" + name + ".json")
		if err != nil {
			continue
		}
		var cam Camera
		if json.Unmarshal(b, &cam) != nil {
			continue
		}
		runs, _ := filepath.Glob("../../output/" + name + "/*")
		sort.Strings(runs)
		if len(runs) == 0 {
			continue
		}
		im, _ := fits.ReadImage(filepath.Join(runs[len(runs)-1], "lin_sky.fits"))
		if im == nil {
			continue
		}
		panels = append(panels, Panel{Name: name, Cam: cam, Img: im})
	}
	if len(panels) < 2 {
		t.Skip("no cached solutions")
	}
	c, err := PlanCanvas(panels, Equirectangular, Galactic, 0.03)
	if err != nil {
		t.Fatal(err)
	}

	// One panel alone, on the very same canvas.
	for _, want := range []string{"p05"} {
		for _, p := range panels {
			if p.Name != want {
				continue
			}
			img, cov, err := Render([]Panel{p}, c, DefaultRenderOptions())
			if err != nil {
				t.Fatal(err)
			}
			psf, n := empiricalPSF(img, 20)
			if n > 0 {
				fwhm, el, pa := measurePSF(psf, 20)
				fmt.Printf("%s alone on the canvas: FWHM %.1f px, elongation %.2f, PA %.1f (%d stars)\n",
					p.Name, fwhm, el, pa, n)
				_ = writePSFPNG(psf, 20, scratch+"psf_single_"+p.Name+".png")
			}
			x0, y0 := boxCentre(cov, img.W, img.H)
			_ = writeCrop(img, x0, y0, 600, scratch+"crop_single_"+p.Name+".png")
			fmt.Printf("  crop at %d,%d -> crop_single_%s.png\n", x0, y0, p.Name)
		}
	}

	// And the whole set, for the same measurement at the same place.
	MatchPhotometry(panels, c, 40000, 8)
	img, cov, err := Render(panels, c, DefaultRenderOptions())
	if err != nil {
		t.Fatal(err)
	}
	psf, n := empiricalPSF(img, 20)
	if n > 0 {
		fwhm, el, pa := measurePSF(psf, 20)
		fmt.Printf("all %d panels blended:   FWHM %.1f px, elongation %.2f, PA %.1f (%d stars)\n",
			len(panels), fwhm, el, pa, n)
		_ = writePSFPNG(psf, 20, scratch+"psf_blended.png")
	}
	x0, y0 := boxCentre(cov, img.W, img.H)
	_ = writeCrop(img, x0, y0, 600, scratch+"crop_blended.png")
}

// boxCentre picks a well-covered spot to crop.
func boxCentre(cov []float32, w, h int) (int, int) {
	best, bx, by := float32(-1), w/2, h/2
	for y := 300; y < h-300; y += 50 {
		for x := 300; x < w-300; x += 50 {
			if v := cov[y*w+x]; v > best {
				best, bx, by = v, x, y
			}
		}
	}
	return bx - 300, by - 300
}
