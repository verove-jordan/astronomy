package meteor

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestZZAtSpot crops a named place in a named frame and reports what the detector found there.
//
// It exists to close the loop that every earlier attempt left open: a streak seen by eye in the
// contact sheet is located in the original frame, and the detector's own numbers AT THAT SPOT are
// printed next to it. Without this, a detector that reports nothing and a sky that contains nothing
// are indistinguishable, which is exactly how three failed detectors were each read as evidence
// about the data.
//
//	ASTRO_METEOR_SEQ=<abs .../01_seq> ASTRO_METEOR_SPOTS="6:2832,480 12:3200,608" \
//	  go test ./internal/meteor/ -run TestZZAtSpot -v
func TestZZAtSpot(t *testing.T) {
	dir := os.Getenv("ASTRO_METEOR_SEQ")
	spots := os.Getenv("ASTRO_METEOR_SPOTS")
	if dir == "" || spots == "" {
		t.Skip("set ASTRO_METEOR_SEQ and ASTRO_METEOR_SPOTS (\"frame:x,y frame:x,y\")")
	}
	out := filepath.Join(dir, "streaks")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, spec := range strings.Fields(spots) {
		var n, x, y int
		if _, err := fmt.Sscanf(strings.ReplaceAll(spec, ",", " "), "%d:%d %d", &n, &x, &y); err != nil {
			t.Fatalf("bad spot %q: %v", spec, err)
		}
		pat := os.Getenv("ASTRO_METEOR_GLOB")
		if pat == "" {
			pat = "light_%05d.fits"
		}
		path := filepath.Join(dir, fmt.Sprintf(pat, n))
		im, err := fits.ReadImage(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		name := fmt.Sprintf("spot_f%02d_%d_%d.png", n, x, y)
		if err := cropPNG(im, x, y, 700, filepath.Join(out, name)); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("%s at (%d,%d) -> %s\n", filepath.Base(path), x, y, name)

		o := DefaultStreakOptions()
		got := DetectStreaks(im, n, o)
		type hit struct {
			d float64
			s Streak
		}
		var near []hit
		for _, s := range got {
			cx, cy := s.Midpoint()
			d := math.Hypot(cx-float64(x), cy-float64(y))
			if d < 900 {
				near = append(near, hit{d, s})
			}
		}
		sort.Slice(near, func(a, b int) bool { return near[a].d < near[b].d })
		fmt.Printf("  %d detection(s) in the whole frame, %d within 900 px of the spot:\n", len(got), len(near))
		for i, h := range near {
			if i >= 8 {
				break
			}
			fmt.Printf("    d=%4.0f  len %6.1f  width %5.1f  aspect %5.1f  bend %4.1f  duty %.2f  peak %5.1f  (%.0f,%.0f)-(%.0f,%.0f)\n",
				h.d, h.s.LengthPx, h.s.WidthPx, h.s.LengthPx/h.s.WidthPx, h.s.StraightnessPx, h.s.Duty,
				h.s.PeakExcess, h.s.X1, h.s.Y1, h.s.X2, h.s.Y2)
		}
		// The response actually present at the spot, whether or not a component was kept there.
		z, w, hh, ok := whiten(im, o)
		if !ok {
			continue
		}
		r := LinearResponse(z, w, hh, o)
		bx, by := x/o.Bin, y/o.Bin
		best := 0.0
		for dy := -40; dy <= 40; dy++ {
			for dx := -40; dx <= 40; dx++ {
				px, py := bx+dx, by+dy
				if px < 0 || py < 0 || px >= w || py >= hh {
					continue
				}
				best = math.Max(best, float64(r[py*w+px]))
			}
		}
		sorted := append([]float32(nil), r...)
		sort.Slice(sorted, func(a, b int) bool { return sorted[a] < sorted[b] })
		q := func(f float64) float64 { return float64(sorted[int(f*float64(len(sorted)-1))]) }
		fmt.Printf("  response at the spot: %.2f  (frame p50 %.2f  p99 %.2f  p99.9 %.2f  max %.2f)\n",
			best, q(.5), q(.99), q(.999), q(1))
	}
	_ = strconv.Itoa
}
