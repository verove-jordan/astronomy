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
)

// TestZZStreaks runs single-frame detection over a real, UNREGISTERED sequence and writes out what it
// found so it can be looked at.
//
//	ASTRO_METEOR_SEQ=<abs work/<panel>/<run>/01_seq> go test ./internal/meteor/ -run TestZZStreaks -v
func TestZZStreaks(t *testing.T) {
	dir := os.Getenv("ASTRO_METEOR_SEQ")
	if dir == "" {
		t.Skip("set ASTRO_METEOR_SEQ to a sequence directory holding light_*.fits")
	}
	glob := os.Getenv("ASTRO_METEOR_GLOB")
	if glob == "" {
		glob = "light_*.fits"
	}
	paths, err := filepath.Glob(filepath.Join(dir, glob))
	if err != nil || len(paths) == 0 {
		t.Skipf("no %s under %s (%v)", glob, dir, err)
	}
	sort.Strings(paths)
	out := filepath.Join(dir, "streaks")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}

	o := DefaultStreakOptions()
	var all []Streak
	frames := map[int]string{}
	for n, p := range paths {
		im, err := fits.ReadImage(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		got := DetectStreaks(im, n, o)
		frames[n] = p
		if len(got) > 0 {
			// Rank within the frame so the log reads worst-first.
			sort.Slice(got, func(a, b int) bool { return got[a].LengthPx > got[b].LengthPx })
			fmt.Printf("%s: %d candidate(s)\n", filepath.Base(p), len(got))
			for _, s := range got {
				fmt.Printf("    len %6.1f  width %5.1f  aspect %5.1f  bend %4.1f  duty %.2f  full %.2f  peak %6.1f  (%.0f,%.0f)-(%.0f,%.0f)\n",
					s.LengthPx, s.WidthPx, s.LengthPx/s.WidthPx, s.StraightnessPx, s.Duty, s.Fullness, s.PeakExcess,
					s.X1, s.Y1, s.X2, s.Y2)
			}
		}
		all = append(all, got...)
	}
	fmt.Printf("\n%d frame(s), %d candidate(s) total\n", len(paths), len(all))
	if len(all) == 0 {
		return
	}

	co := DefaultOptions()
	Classify(all, co)
	c := Counts(all)
	fmt.Printf("classified: %d meteor, %d satellite, %d aircraft, %d not-straight\n",
		c[Meteor], c[Satellite], c[Aircraft], c[HotPixel])

	sort.Slice(all, func(a, b int) bool { return all[a].LengthPx > all[b].LengthPx })
	fmt.Println("\ntop candidates:")
	for i, s := range all {
		if i >= 25 {
			break
		}
		fmt.Printf("  %-10s frame %2d  len %6.1f  aspect %5.1f  bend %4.1f  duty %.2f  full %.2f  peak %6.1f  %s\n",
			s.Class, s.Frame, s.LengthPx, s.LengthPx/s.WidthPx, s.StraightnessPx, s.Duty, s.Fullness, s.PeakExcess, s.Why)
	}

	// Write a crop of each of the strongest candidates so the decision can be checked by eye.
	for i, s := range all {
		if i >= 12 {
			break
		}
		im, err := fits.ReadImage(frames[s.Frame])
		if err != nil {
			continue
		}
		cx, cy := s.Midpoint()
		half := int(math.Max(s.LengthPx*0.8, 300))
		name := fmt.Sprintf("cand%02d_f%02d_%s_len%.0f.png", i, s.Frame, s.Class, s.LengthPx)
		if err := cropPNG(im, int(cx), int(cy), half, filepath.Join(out, name)); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("-> %s\n", name)
	}
}

// TestZZContact renders every frame's noise-normalised view into one sheet, so that whether a meteor
// is present at all can be settled INDEPENDENTLY of whether the detector finds it. That question was
// never actually answered on this panel, and every failed detector was interpreted without it.
//
//	ASTRO_METEOR_SEQ=<abs .../01_seq> go test ./internal/meteor/ -run TestZZContact -v
func TestZZContact(t *testing.T) {
	dir := os.Getenv("ASTRO_METEOR_SEQ")
	if dir == "" {
		t.Skip("set ASTRO_METEOR_SEQ to a sequence directory holding light_*.fits")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "light_*.fits"))
	if err != nil || len(paths) == 0 {
		t.Skipf("no light_*.fits under %s (%v)", dir, err)
	}
	sort.Strings(paths)
	o := DefaultStreakOptions()
	o.Bin = 8 // one tile per frame, small enough that the whole night fits on a screen

	cols := 6
	rows := (len(paths) + cols - 1) / cols
	var tw, th int
	tiles := make([][]float32, len(paths))
	for n, p := range paths {
		im, err := fits.ReadImage(p)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		z, w, h, ok := whiten(im, o)
		if !ok {
			t.Fatalf("%s: could not be whitened", p)
		}
		tiles[n], tw, th = z, w, h
	}
	write := func(name string, pick func(n int) []float32, scale float64) {
		sheet := image.NewRGBA(image.Rect(0, 0, cols*tw, rows*th))
		for n := range tiles {
			v := pick(n)
			ox, oy := (n%cols)*tw, (n/cols)*th
			for y := 0; y < th; y++ {
				for x := 0; x < tw; x++ {
					g := math.Min(math.Max(float64(v[y*tw+x])/scale, 0), 1)
					c := uint8(255 * math.Sqrt(g))
					sheet.Set(ox+x, oy+y, color.RGBA{c, c, c, 255})
				}
			}
		}
		path := filepath.Join(dir, name)
		f, err := os.Create(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, sheet); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("-> %s (%d frames, %dx%d each)\n", path, len(paths), tw, th)
	}
	write("contact.png", func(n int) []float32 { return tiles[n] }, 6)

	// The same frames seen the way the detector sees them: stars deleted, only what is linear left.
	// At this binning the element is short in sky terms, so the response is a coarse preview of where
	// linear structure lives rather than the detector's real verdict — but it is enough to point at a
	// streak, which reading a stretched frame by eye is not.
	ro := o
	ro.LenPx = 13
	resp := make([][]float32, len(tiles))
	for n := range tiles {
		resp[n] = LinearResponse(tiles[n], tw, th, ro)
	}
	write("contact_response.png", func(n int) []float32 { return resp[n] }, 8)
}

// cropPNG writes a noise-normalised crop around a point, hard-stretched so a faint streak is visible
// next to the stars it hides among.
func cropPNG(im *fits.Image, cx, cy, half int, path string) error {
	o := DefaultStreakOptions()
	o.Bin = 1
	z, w, h, ok := whiten(im, o)
	if !ok {
		return fmt.Errorf("crop: frame could not be whitened")
	}
	x0, y0 := clampInt(cx-half, 0, w-1), clampInt(cy-half, 0, h-1)
	x1, y1 := clampInt(cx+half, 0, w-1), clampInt(cy+half, 0, h-1)
	out := image.NewRGBA(image.Rect(0, 0, x1-x0+1, y1-y0+1))
	for y := y0; y <= y1; y++ {
		for x := x0; x <= x1; x++ {
			g := math.Min(math.Max(float64(z[y*w+x])/8, 0), 1)
			c := uint8(255 * math.Sqrt(g))
			out.Set(x-x0, y-y0, color.RGBA{c, c, c, 255})
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, out)
}
