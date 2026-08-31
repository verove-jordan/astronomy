package meteor

import (
	"encoding/json"
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

// TestZZBlend is the whole of phase 3 end to end on a real panel: detect on the REGISTERED frames,
// classify, paint what is worth keeping into a layer, and blend it over the stacked sky.
//
//	ASTRO_METEOR_SEQ=<abs .../01_seq> ASTRO_METEOR_SKY=<abs .../sky_linear.fits> \
//	  go test ./internal/meteor/ -run TestZZBlend -v
func TestZZBlend(t *testing.T) {
	dir := os.Getenv("ASTRO_METEOR_SEQ")
	sky := os.Getenv("ASTRO_METEOR_SKY")
	if dir == "" || sky == "" {
		t.Skip("set ASTRO_METEOR_SEQ and ASTRO_METEOR_SKY")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "r_light_*.fits"))
	if err != nil || len(paths) == 0 {
		t.Skipf("no r_light_*.fits under %s (%v)", dir, err)
	}
	sort.Strings(paths)
	out := filepath.Join(dir, "streaks")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}

	so := DefaultStreakOptions()
	var all []Streak
	for n, p := range paths {
		im, err := fits.ReadImage(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		all = append(all, DetectStreaks(im, n, so)...)
	}
	Classify(all, DefaultOptions())
	c := Counts(all)
	fmt.Printf("%d candidates: %d meteor, %d satellite, %d aircraft, %d bent\n",
		len(all), c[Meteor], c[Satellite], c[Aircraft], c[HotPixel])

	co := DefaultOptions()
	kept := Confident(Kept(all), co)
	sort.Slice(kept, func(a, b int) bool { return kept[a].PeakExcess > kept[b].PeakExcess })
	fmt.Printf("%d classified meteor, %d confident enough to paint:\n", len(Kept(all)), len(kept))
	for _, s := range kept {
		fmt.Printf("  frame %2d  len %6.1f  aspect %5.1f  peak %5.1f  full %.2f  duty %.2f\n",
			s.Frame, s.LengthPx, s.LengthPx/s.WidthPx, s.PeakExcess, s.Fullness, s.Duty)
	}
	if b, err := json.MarshalIndent(all, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(out, "meteors.json"), b, 0o644)
	}

	base, err := fits.ReadImage(sky)
	if err != nil {
		t.Fatal(err)
	}
	layer, err := RenderLayer(func(f int) (*fits.Image, error) { return fits.ReadImage(paths[f]) },
		base, kept, DefaultRenderOptions())
	if err != nil {
		t.Fatal(err)
	}
	if layer == nil {
		fmt.Println("nothing to blend")
		return
	}
	fmt.Printf("sky %dx%dx%d, layer %dx%dx%d\n", base.W, base.H, base.C, layer.W, layer.H, layer.C)
	stretchPNG(layer, filepath.Join(out, "meteor_layer.png"))
	stretchPNG(base, filepath.Join(out, "sky_clean.png"))
	stretchPNG(Blend(base, layer, 1), filepath.Join(out, "sky_with_meteors.png"))
	fmt.Println("-> meteor_layer.png, sky_clean.png, sky_with_meteors.png")
}

// stretchPNG writes an autoscaled preview, downsampled to something a screen can hold.
func stretchPNG(im *fits.Image, path string) {
	s := 1
	for im.W/s > 1600 || im.H/s > 1600 {
		s++
	}
	w, h := im.W/s, im.H/s
	var v []float32
	for i := 0; i < len(im.Pix[0]); i += 7 {
		if im.Pix[0][i] > 0 {
			v = append(v, im.Pix[0][i])
		}
	}
	hi := float32(1)
	if len(v) > 16 {
		sort.Slice(v, func(a, b int) bool { return v[a] < v[b] })
		hi = v[int(0.999*float64(len(v)-1))]
	}
	if hi <= 0 {
		hi = 1
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := (y*s)*im.W + x*s
			px := [3]uint8{}
			for ch := 0; ch < 3; ch++ {
				p := im.Pix[0]
				if ch < im.C {
					p = im.Pix[ch]
				}
				g := math.Min(math.Max(float64(p[i]/hi), 0), 1)
				px[ch] = uint8(255 * math.Pow(g, 0.45))
			}
			out.Set(x, y, color.RGBA{px[0], px[1], px[2], 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return
	}
	defer f.Close()
	_ = png.Encode(f, out)
}
