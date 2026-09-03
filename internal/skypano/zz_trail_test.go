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
	"github.com/verove-jordan/astronomy/internal/starfield"
)

// TestZZTrailDiag measures WHERE the star trailing is introduced. One 10-second exposure, that same
// exposure after registration, and the stack: whichever step the elongation appears at is the step
// that causes it, and the fix for a motion blur inside one exposure has nothing in common with the
// fix for frames that did not land on top of each other.
//
// It measures an EMPIRICAL PSF — the average of cutouts around the brightest stars — rather than
// per-star second moments. Moments over a box that is mostly background measure the box: the same
// frame read FWHM 9.4 px at box radius 9 and 29.3 px at radius 24, which is a measurement of the
// radius and not of the star. Averaging hundreds of stars gives one high-signal profile instead,
// and the half-maximum contour of that profile is a number that means what it says.
func TestZZTrailDiag(t *testing.T) {
	requireHarness(t)
	const panel = "p05"
	seq := "../../work/" + panel + "/run_20260811_182421/01_seq/"
	runs, _ := filepath.Glob("../../output/" + panel + "/*")
	sort.Strings(runs)

	cases := []struct{ label, path string }{
		{"one_raw_sub", seq + "light_00001.fits"},
		{"registered_sub", seq + "r_light_00001.fits"},
		{"another_raw_sub", seq + "light_00005.fits"},
	}
	if len(runs) > 0 {
		cases = append(cases, struct{ label, path string }{"stack", filepath.Join(runs[len(runs)-1], "lin_sky.fits")})
	}
	for _, c := range cases {
		im, err := fits.ReadImage(c.path)
		if err != nil || im == nil {
			fmt.Printf("%s: %v\n", c.label, err)
			continue
		}
		psf, n := empiricalPSF(im, 20)
		if n == 0 {
			fmt.Printf("%s: no usable stars\n", c.label)
			continue
		}
		fwhm, elong, pa := measurePSF(psf, 20)
		fmt.Printf("%-16s from %3d stars: FWHM %.1f px, elongation %.2f, PA %.1f deg  (%dx%d image)\n",
			c.label, n, fwhm, elong, pa, im.W, im.H)
		_ = writePSFPNG(psf, 20, scratch+"psf_"+c.label+".png")
	}
}

// empiricalPSF averages normalised cutouts around the brightest unsaturated stars.
func empiricalPSF(im *fits.Image, half int) ([]float64, int) {
	det := starfield.Detect(im.Pix[1], im.W, im.H,
		starfield.Options{Sigma: 10, BoxRadius: 6, MinSeparation: float64(3 * half), Max: 4000})
	sort.Slice(det, func(a, b int) bool { return det[a].Flux > det[b].Flux })

	size := 2*half + 1
	acc := make([]float64, size*size)
	used := 0
	for _, s := range det {
		if used >= 400 {
			break
		}
		cx, cy := int(math.Round(s.X)), int(math.Round(s.Y))
		if cx < half || cy < half || cx+half >= im.W || cy+half >= im.H {
			continue
		}
		// Local background from the cutout's own border, then normalise by the peak so every star
		// contributes its SHAPE and the brightest few do not become the answer on their own.
		var border []float64
		for dy := -half; dy <= half; dy++ {
			for dx := -half; dx <= half; dx++ {
				if abs(dx) == half || abs(dy) == half {
					border = append(border, float64(im.Pix[1][(cy+dy)*im.W+cx+dx]))
				}
			}
		}
		sort.Float64s(border)
		bg := border[len(border)/2]
		peak := float64(im.Pix[1][cy*im.W+cx]) - bg
		if peak <= 0 || peak > 0.7 { // saturated stars have a flat top and no shape
			continue
		}
		for dy := -half; dy <= half; dy++ {
			for dx := -half; dx <= half; dx++ {
				v := (float64(im.Pix[1][(cy+dy)*im.W+cx+dx]) - bg) / peak
				acc[(dy+half)*size+dx+half] += v
			}
		}
		used++
	}
	if used == 0 {
		return nil, 0
	}
	for i := range acc {
		acc[i] /= float64(used)
	}
	return acc, used
}

// measurePSF reports the half-maximum contour: its equivalent circular width, and the elongation and
// angle of the second moments taken over that contour ALONE. Restricting to the core is what keeps
// this from measuring the background the way per-star moments did.
func measurePSF(psf []float64, half int) (fwhm, elong, paDeg float64) {
	size := 2*half + 1
	var peak float64
	for _, v := range psf {
		peak = math.Max(peak, v)
	}
	if peak <= 0 {
		return 0, 0, 0
	}
	var area, sxx, syy, sxy, sw float64
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			v := psf[y*size+x]
			if v < peak/2 {
				continue
			}
			area++
			w := v - peak/2
			dx, dy := float64(x-half), float64(y-half)
			sxx += w * dx * dx
			syy += w * dy * dy
			sxy += w * dx * dy
			sw += w
		}
	}
	fwhm = 2 * math.Sqrt(area/math.Pi)
	if sw <= 0 {
		return fwhm, 0, 0
	}
	sxx, syy, sxy = sxx/sw, syy/sw, sxy/sw
	tr, det := sxx+syy, sxx*syy-sxy*sxy
	disc := math.Sqrt(math.Max(tr*tr/4-det, 0))
	maj, min := tr/2+disc, tr/2-disc
	if min <= 0 {
		return fwhm, 0, 0
	}
	return fwhm, math.Sqrt(maj / min), math.Mod(0.5*math.Atan2(2*sxy, sxx-syy)*180/math.Pi+180, 180)
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// writePSFPNG renders the PSF big enough to look at, on a square-root scale.
func writePSFPNG(psf []float64, half int, path string) error {
	size := 2*half + 1
	const zoom = 8
	var peak float64
	for _, v := range psf {
		peak = math.Max(peak, v)
	}
	out := image.NewRGBA(image.Rect(0, 0, size*zoom, size*zoom))
	for y := 0; y < size*zoom; y++ {
		for x := 0; x < size*zoom; x++ {
			v := psf[(y/zoom)*size+x/zoom] / peak
			g := uint8(255 * math.Sqrt(math.Min(math.Max(v, 0), 1)))
			out.Set(x, y, color.RGBA{g, g, g, 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, out)
}
