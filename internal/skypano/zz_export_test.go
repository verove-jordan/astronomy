package skypano

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// writePNG encodes a graded (display-referred linear) canvas, downscaled for viewing.
func writePNG(im *fits.Image, keep []bool, path string) error {
	s := 1
	for im.W/s > 2600 || im.H/s > 2600 {
		s++
	}
	ow, oh := im.W/s, im.H/s
	out := image.NewRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < oh; y++ {
		for x := 0; x < ow; x++ {
			i := (y*s)*im.W + x*s
			if keep != nil && !keep[i] {
				out.Set(x, y, color.RGBA{0, 0, 0, 255})
				continue
			}
			var v [3]uint8
			for k := 0; k < 3; k++ {
				c := im.Pix[k%im.C][i]
				v[k] = uint8(math.Round(255 * math.Min(math.Max(SRGBEncode(float64(c)), 0), 1)))
			}
			out.Set(x, y, color.RGBA{v[0], v[1], v[2], 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, out)
}

// TestZZRegrade re-grades an already-rendered mosaic in seconds, without re-solving or re-rendering.
func TestZZRegrade(t *testing.T) {
	requireHarness(t)
	for _, name := range []string{"galactic_strip", "stereographic"} {
		im, err := fits.ReadImage(scratch + "mosaic_" + name + "_raw.fits")
		if err != nil {
			t.Skip(err)
		}
		cv, err := fits.ReadImage(scratch + "mosaic_" + name + "_cov.fits")
		if err != nil {
			t.Skip(err)
		}
		var c Canvas
		b, err := os.ReadFile(scratch + "mosaic_" + name + "_canvas.json")
		if err != nil {
			t.Skip(err)
		}
		if err := json.Unmarshal(b, &c); err != nil {
			t.Fatal(err)
		}
		bg, err := Flatten(im, cv.Pix[0], c, DefaultFlattenOptions())
		if err != nil {
			t.Fatal(err)
		}
		o := DefaultGradeOptions()
		keep := Grade(im, cv.Pix[0], c, o)
		_ = bg
		n := 0
		for _, k := range keep {
			if k {
				n++
			}
		}
		if err := writePNG(im, keep, scratch+"mosaic_"+name+".png"); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("%s: %dx%d, %.1f%% kept -> mosaic_%s.png\n",
			name, im.W, im.H, 100*float64(n)/float64(len(keep)), name)
	}
}
