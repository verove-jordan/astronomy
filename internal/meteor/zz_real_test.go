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
	"github.com/verove-jordan/astronomy/internal/trail"
)

// TestZZReal runs detection over a real run's rejected layer and draws what it found.
//
//	ASTRO_METEOR_RUN=<abs output/<obj>/<runID>> go test ./internal/meteor/ -run TestZZReal -v
func TestZZReal(t *testing.T) {
	dir := os.Getenv("ASTRO_METEOR_RUN")
	if dir == "" {
		t.Skip("set ASTRO_METEOR_RUN to a run directory holding transients.fits")
	}
	img, err := fits.ReadImage(filepath.Join(dir, "transients.fits"))
	if err != nil {
		t.Skip(err)
	}
	meta, err := fits.ReadImage(filepath.Join(dir, "transients_meta.fits"))
	if err != nil {
		t.Skip(err)
	}
	l := Layer{W: img.W, H: img.H,
		Excess: meta.Pix[2],
		Frame:  make([]int32, img.W*img.H),
		Count:  make([]int32, img.W*img.H),
	}
	for i := range l.Frame {
		l.Frame[i] = int32(meta.Pix[0][i])
		l.Count[i] = int32(meta.Pix[1][i])
	}

	nonzero, peak := 0, float32(0)
	for _, e := range l.Excess {
		if e > 0 {
			nonzero++
		}
		if e > peak {
			peak = e
		}
	}
	fmt.Printf("layer %dx%d: %.1f%% of pixels were rejected at least once, peak excess %.4f\n",
		l.W, l.H, 100*float64(nonzero)/float64(len(l.Excess)), peak)

	var nz []float32
	for _, e := range l.Excess {
		if e > 0 {
			nz = append(nz, e)
		}
	}
	sort.Slice(nz, func(a, b int) bool { return nz[a] < nz[b] })
	q := func(f float64) float32 { return nz[int(f*float64(len(nz)-1))] }
	fmt.Printf("excess percentiles: p50=%.5f p90=%.5f p99=%.5f p99.9=%.5f p99.99=%.5f max=%.5f\n",
		q(.5), q(.9), q(.99), q(.999), q(.9999), nz[len(nz)-1])
	var cnt []int32
	for _, c := range l.Count {
		if c > 0 {
			cnt = append(cnt, c)
		}
	}
	sort.Slice(cnt, func(a, b int) bool { return cnt[a] < cnt[b] })
	fmt.Printf("rejection count per touched pixel: p50=%d p90=%d p99=%d max=%d\n",
		cnt[len(cnt)/2], cnt[int(.9*float64(len(cnt)-1))], cnt[int(.99*float64(len(cnt)-1))], cnt[len(cnt)-1])

	o := DefaultOptions()
	// Experiment: how much does each cheap filter thin the plane, and does Hough then bite?
	for _, f := range []struct {
		name string
		keep func(i int) bool
	}{
		{"raw", func(i int) bool { return true }},
		{"count==1", func(i int) bool { return l.Count[i] == 1 }},
		{"count==1 & top1%", func(i int) bool { return l.Count[i] == 1 && l.Excess[i] > 0.003 }},
		{"top0.1%", func(i int) bool { return l.Excess[i] > 0.0136 }},
	} {
		p := make([]float32, len(l.Excess))
		n := 0
		for i := range p {
			if f.keep(i) && l.Excess[i] > 0 {
				p[i] = l.Excess[i]
				n++
			}
		}
		sg := trail.DetectSegments(p, l.W, l.H, trail.DefaultParams(o.SeedK))
		fmt.Printf("  %-18s %5.2f%% of pixels lit -> %d segment(s)\n",
			f.name, 100*float64(n)/float64(len(p)), len(sg))
	}
	segs := trail.DetectSegments(l.Excess, l.W, l.H, trail.DefaultParams(o.SeedK))
	fmt.Printf("hough returned %d segment(s); member cut %.5f\n", len(segs), lowCut(l, o))
	for _, sg := range segs {
		st, ok := fromSegment(l, sg, o)
		fmt.Printf("  seg score %.2f width %.1f extent %.0f..%.0f -> kept=%v len %.0f px %d px frame %d\n",
			sg.Score, sg.Width, sg.T0, sg.T1, ok, st.LengthPx, st.Pixels, st.Frame)
	}

	got := Detect(l, o)
	counts := Counts(got)
	fmt.Printf("%d streaks: %d meteor, %d satellite, %d aircraft, %d hot\n",
		len(got), counts[Meteor], counts[Satellite], counts[Aircraft], counts[HotPixel])

	sort.Slice(got, func(a, b int) bool { return got[a].LengthPx > got[b].LengthPx })
	for i, s := range got {
		if i >= 12 {
			break
		}
		fmt.Printf("  %-9s len %6.1f px  width %4.1f  frame %3d  peak %.3f  at (%.0f,%.0f)-(%.0f,%.0f)  %s\n",
			s.Class, s.LengthPx, s.WidthPx, s.Frame, s.PeakExcess, s.X1, s.Y1, s.X2, s.Y2, s.Why)
	}
	if err := drawFound(img, got, filepath.Join(dir, "meteors_found.png")); err != nil {
		t.Fatal(err)
	}
	fmt.Println("-> meteors_found.png")
}

// drawFound renders the transient layer with each streak boxed: green kept, red dropped.
func drawFound(im *fits.Image, ss []Streak, path string) error {
	s := 1
	for im.W/s > 2000 || im.H/s > 2000 {
		s++
	}
	w, h := im.W/s, im.H/s
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	var v []float32
	for _, p := range im.Pix[1] {
		if p > 0 {
			v = append(v, p)
		}
	}
	hi := float32(1)
	if len(v) > 0 {
		sort.Slice(v, func(a, b int) bool { return v[a] < v[b] })
		hi = v[int(0.999*float64(len(v)-1))]
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			g := im.Pix[1][(y*s)*im.W+x*s] / hi
			c := uint8(255 * math.Min(math.Max(math.Sqrt(float64(g)), 0), 1))
			out.Set(x, y, color.RGBA{c, c, c, 255})
		}
	}
	for _, k := range ss {
		col := color.RGBA{0, 255, 0, 255}
		if k.Class != Meteor {
			col = color.RGBA{255, 40, 40, 255}
		}
		x1, y1 := int(math.Min(k.X1, k.X2))/s-4, int(math.Min(k.Y1, k.Y2))/s-4
		x2, y2 := int(math.Max(k.X1, k.X2))/s+4, int(math.Max(k.Y1, k.Y2))/s+4
		for x := x1; x <= x2; x++ {
			set(out, x, y1, col)
			set(out, x, y2, col)
		}
		for y := y1; y <= y2; y++ {
			set(out, x1, y, col)
			set(out, x2, y, col)
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, out)
}

func set(im *image.RGBA, x, y int, c color.RGBA) {
	if x >= 0 && y >= 0 && x < im.Bounds().Dx() && y < im.Bounds().Dy() {
		im.Set(x, y, c)
	}
}
