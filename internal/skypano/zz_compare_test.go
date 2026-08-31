package skypano

import (
	"encoding/json"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestZZBeforeAfter renders the same patch of sky twice — once with the per-panel solutions and once
// with the shared-lens bundle — and puts them side by side.
func TestZZBeforeAfter(t *testing.T) {
	requireHarness(t)
	load := func(suffix string) []Panel {
		var out []Panel
		for _, name := range []string{"p01", "p03", "p04", "p05", "p06", "p07", "p08"} {
			b, err := os.ReadFile(scratch + "cam_" + name + suffix + ".json")
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
			out = append(out, Panel{Name: name, Cam: cam, Img: im})
		}
		return out
	}
	before, after := load(""), load("_bundled")
	if len(before) < 2 || len(after) < 2 {
		t.Skip("need both camera sets")
	}

	// One canvas for both, so the crop is the same sky at the same scale.
	c, err := PlanCanvas(after, Equirectangular, Galactic, 0.03)
	if err != nil {
		t.Fatal(err)
	}
	const n = 700
	x0, y0 := c.W/2-n/2, c.H/2-n/2

	for _, s := range []struct {
		label  string
		panels []Panel
	}{{"before", before}, {"after", after}} {
		MatchPhotometry(s.panels, c, 40000, 8)
		img, cov, err := Render(s.panels, c, DefaultRenderOptions())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Flatten(img, cov, c, DefaultFlattenOptions()); err != nil {
			t.Fatal(err)
		}
		if err := writeCrop(img, x0, y0, n, scratch+"cmp_"+s.label+".png"); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("%s crop written\n", s.label)
	}

	// Stitch, with a gap so the join is not mistaken for a seam.
	const gap = 12
	a := decodePNG(t, scratch+"cmp_before.png")
	b := decodePNG(t, scratch+"cmp_after.png")
	out := image.NewRGBA(image.Rect(0, 0, n*2+gap, n))
	draw.Draw(out, image.Rect(0, 0, n, n), a, image.Point{}, draw.Src)
	draw.Draw(out, image.Rect(n+gap, 0, n*2+gap, n), b, image.Point{}, draw.Src)
	f, err := os.Create(scratch + "cmp_side_by_side.png")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, out); err != nil {
		t.Fatal(err)
	}
	fmt.Println("-> cmp_side_by_side.png (left: per-panel solve, right: shared-lens bundle)")
}

func decodePNG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	im, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return im
}
