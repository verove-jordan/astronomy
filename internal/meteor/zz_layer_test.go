package meteor

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

// TestZZLayer builds the blendable meteor layer for a real run and renders it.
//
//	ASTRO_METEOR_RUN=<abs output/<obj>/<runID>> go test ./internal/meteor/ -run TestZZLayer -v
func TestZZLayer(t *testing.T) {
	dir := os.Getenv("ASTRO_METEOR_RUN")
	if dir == "" {
		t.Skip("set ASTRO_METEOR_RUN")
	}
	tran, err := fits.ReadImage(filepath.Join(dir, "transients.fits"))
	if err != nil {
		t.Skip(err)
	}
	meta, err := fits.ReadImage(filepath.Join(dir, "transients_meta.fits"))
	if err != nil {
		t.Skip(err)
	}
	sky, err := fits.ReadImage(filepath.Join(dir, "lin_sky.fits"))
	if err != nil {
		t.Skip(err)
	}
	w, h := tran.W, tran.H
	l := Layer{W: w, H: h, Excess: meta.Pix[2],
		Frame: make([]int32, w*h), Count: make([]int32, w*h)}
	for i := range l.Frame {
		l.Frame[i] = int32(meta.Pix[0][i])
		l.Count[i] = int32(meta.Pix[1][i])
	}
	det := starfield.Detect(sky.Pix[1], w, h,
		starfield.Options{Sigma: 5, BoxRadius: 5, MinSeparation: 6, Max: 30000})
	st := Stars{}
	for _, s := range det {
		st.X, st.Y = append(st.X, s.X), append(st.Y, s.Y)
	}

	o := DefaultLayerOptions()
	if v := os.Getenv("ASTRO_LAYER_MINLEN"); v != "" {
		fmt.Sscanf(v, "%f", &o.MinLengthPx)
	}
	if v := os.Getenv("ASTRO_LAYER_MARGIN"); v != "" {
		fmt.Sscanf(v, "%f", &o.Margin)
	}
	// Stage-by-stage survivor counts, so a zero result names the filter that caused it.
	count := func(b []bool) int {
		n := 0
		for _, v := range b {
			if v {
				n++
			}
		}
		return n
	}
	noStars := o
	noStars.StarRadiusPx = 0
	fmt.Printf("  margin+count<=%d:      %8d px\n", o.MaxRejectFrames, count(cleanMask(l, Stars{}, noStars)))
	usable := cleanMask(l, st, o)
	fmt.Printf("  minus star discs:     %8d px\n", count(usable))
	cut := float32(quantileOfPositive(l.Excess, o.NoiseQuantile) * o.Margin)
	seed := make([]bool, w*h)
	for i := range seed {
		seed[i] = usable[i] && l.Excess[i] > cut
	}
	fmt.Printf("  above cut %.5f:     %8d px\n", cut, count(seed))

	img, keep := BuildLayer(tran, l, st, o)
	n := 0
	for _, k := range keep {
		if k {
			n++
		}
	}
	fmt.Printf("%d stars; layer keeps %d px (%.4f%% of the frame) at min length %.0f, margin x%.0f\n",
		len(det), n, 100*float64(n)/float64(w*h), o.MinLengthPx, o.Margin)

	out := filepath.Join(dir, "meteor_layer.png")
	if err := renderLayer(img, keep, w, h, out); err != nil {
		t.Fatal(err)
	}
	fmt.Println("->", out)
}

// renderLayer draws the kept pixels white on black, max-pooled so a thin streak survives downscaling.
func renderLayer(img *fits.Image, keep []bool, w, h int, path string) error {
	s := 1
	for w/s > 1600 || h/s > 1600 {
		s++
	}
	ow, oh := w/s, h/s
	var v []float32
	for i, k := range keep {
		if k {
			v = append(v, img.Pix[1][i])
		}
	}
	hi := float32(1)
	if len(v) > 0 {
		sort.Slice(v, func(a, b int) bool { return v[a] < v[b] })
		hi = v[int(0.99*float64(len(v)-1))]
	}
	out := image.NewRGBA(image.Rect(0, 0, ow, oh))
	for y := 0; y < oh; y++ {
		for x := 0; x < ow; x++ {
			var m float32
			for by := y * s; by < (y+1)*s && by < h; by++ {
				for bx := x * s; bx < (x+1)*s && bx < w; bx++ {
					if i := by*w + bx; keep[i] && img.Pix[1][i] > m {
						m = img.Pix[1][i]
					}
				}
			}
			g := uint8(255 * math.Min(math.Sqrt(float64(m/hi)), 1))
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
