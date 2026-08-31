package skypano

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestZZCrops writes 1:1 crops at each stage, so the trailing can be looked at rather than inferred.
func TestZZCrops(t *testing.T) {
	requireHarness(t)
	const panel = "p05"
	seq := "../../work/" + panel + "/run_20260811_182421/01_seq/"
	runs, _ := filepath.Glob("../../output/" + panel + "/*")
	sort.Strings(runs)

	cases := []struct{ label, path string }{
		{"crop_sub", seq + "light_00001.fits"},
		{"crop_registered", seq + "r_light_00001.fits"},
	}
	if len(runs) > 0 {
		cases = append(cases, struct{ label, path string }{"crop_stack", filepath.Join(runs[len(runs)-1], "lin_sky.fits")})
	}
	cases = append(cases, struct{ label, path string }{"crop_mosaic", scratch + "mosaic_galactic_strip_flat.fits"})

	for _, c := range cases {
		im, err := fits.ReadImage(c.path)
		if err != nil || im == nil {
			fmt.Printf("%s: %v\n", c.label, err)
			continue
		}
		// Same relative position in every image, so they show comparable sky.
		x0, y0 := im.W/2-300, im.H/2-300
		if err := writeCrop(im, x0, y0, 600, scratch+c.label+".png"); err != nil {
			t.Fatal(err)
		}
		psf, n := empiricalPSF(im, 20)
		if n == 0 {
			fmt.Printf("%s: %dx%d, no measurable stars -> %s.png\n", c.label, im.W, im.H, c.label)
			continue
		}
		fwhm, el, pa := measurePSF(psf, 20)
		fmt.Printf("%s: %dx%d, PSF FWHM %.1f px, elongation %.2f, PA %.0f deg (%d stars) -> %s.png\n",
			c.label, im.W, im.H, fwhm, el, pa, n, c.label)
	}
}

func writeCrop(im *fits.Image, x0, y0, n int, path string) error {
	var v []float64
	for y := y0; y < y0+n; y++ {
		for x := x0; x < x0+n; x++ {
			v = append(v, float64(im.Pix[1][y*im.W+x]))
		}
	}
	sort.Float64s(v)
	lo, hi := v[int(0.20*float64(len(v)))], v[int(0.9995*float64(len(v)))]
	if hi <= lo {
		hi = lo + 1e-6
	}
	const beta = 25.0
	den := math.Asinh(beta)

	out := image.NewRGBA(image.Rect(0, 0, n, n))
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			var rgb [3]uint8
			for k := 0; k < 3 && k < im.C; k++ {
				t := (float64(im.Pix[k][(y0+y)*im.W+x0+x]) - lo) / (hi - lo)
				s := math.Asinh(math.Max(t, 0)*beta) / den
				rgb[k] = uint8(255 * math.Min(math.Max(SRGBEncode(s), 0), 1))
			}
			out.Set(x, y, color.RGBA{rgb[0], rgb[1], rgb[2], 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, out)
}
