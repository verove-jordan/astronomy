package skypano

import (
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestZZCorners crops the four corners of a rendered mosaic into one sheet, because doubled stars
// show up where panels meet near their own frame edges and nowhere else.
func TestZZCorners(t *testing.T) {
	requireHarness(t)
	name := os.Getenv("ASTRO_PANO_NAME")
	if name == "" {
		name = "stereographic"
	}
	im, err := fits.ReadImage(scratch + "mosaic_" + name + "_flat.fits")
	if err != nil {
		t.Skip(err)
	}
	cv, cerr := fits.ReadImage(scratch + "mosaic_" + name + "_cov.fits")
	if cerr != nil {
		t.Skip(cerr)
	}
	const n, gap = 640, 10
	// Seam regions: exactly two panels overlapping. That is where a disagreement between them can
	// draw a star twice, and it is the only place worth looking.
	spots := seamSpots(cv, im.W, im.H, n, 4)
	if len(spots) > 0 {
		fmt.Println("seam tiles:", spots)
	}
	if len(spots) == 0 {
		t.Skip("no covered tiles found")
	}
	cols := 2
	rows := (len(spots) + cols - 1) / cols
	sheet := image.NewRGBA(image.Rect(0, 0, n*cols+gap, n*rows+gap))
	for i, s := range spots {
		tmp := scratch + fmt.Sprintf("corner_%d.png", i)
		if err := writeCrop(im, s[0], s[1], n, tmp); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(tmp)
		if err != nil {
			t.Fatal(err)
		}
		sub, derr := png.Decode(f)
		f.Close()
		if derr != nil {
			t.Fatal(derr)
		}
		x0, y0 := (i%cols)*(n+gap), (i/cols)*(n+gap)
		draw.Draw(sheet, image.Rect(x0, y0, x0+n, y0+n), sub, image.Point{}, draw.Src)
	}
	out, err := os.Create(scratch + "corners_" + name + ".png")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := png.Encode(out, sheet); err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%s %dx%d, seams at %v -> corners_%s.png\n", name, im.W, im.H, spots, name)
}

// seamSpots finds well-separated crop origins whose whole tile is covered by about two panels.
func seamSpots(cv *fits.Image, w, h, n, want int) [][2]int {
	var out [][2]int
	step := n / 2
	for y := 0; y+n < h && len(out) < want; y += step {
		for x := 0; x+n < w && len(out) < want; x += step {
			// Any fully-covered tile will do: doubling, if present, shows wherever panels overlap,
			// and on this canvas most pixels are covered about three deep.
			ok, lo, hi := true, float32(1.0), float32(9)
			for _, c := range [][2]int{{x, y}, {x + n - 1, y}, {x, y + n - 1}, {x + n - 1, y + n - 1}, {x + n/2, y + n/2}} {
				v := cv.Pix[0][c[1]*w+c[0]]
				if v < lo || v > hi {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
			far := true
			for _, p := range out {
				if iabs(p[0]-x) < n && iabs(p[1]-y) < n {
					far = false
					break
				}
			}
			if far {
				out = append(out, [2]int{x, y})
			}
		}
	}
	return out
}

func iabs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
