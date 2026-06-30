package nightscape

import (
	"math"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

func TestPercentile(t *testing.T) {
	v := []float32{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	tests := []struct {
		name string
		p    float64
		want float64
	}{
		{"min", 0, 0},
		{"max", 100, 10},
		{"median", 50, 5},
		{"p90", 90, 9},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := percentile(v, tt.p); math.Abs(got-tt.want) > 1e-9 {
				t.Fatalf("percentile(%v) = %v, want %v", tt.p, got, tt.want)
			}
		})
	}
}

func TestGaussianBlur_PreservesMeanOnFlat(t *testing.T) {
	w, h := 40, 30
	src := make([]float32, w*h)
	for i := range src {
		src[i] = 0.42
	}
	got := gaussianBlur(src, w, h, 8)
	for i, v := range got {
		if math.Abs(float64(v-0.42)) > 1e-3 {
			t.Fatalf("flat blur drifted at %d: %v", i, v)
		}
	}
}

func TestLabel_CountsComponents(t *testing.T) {
	// Two separate 4-connected blobs in a 5x3 grid.
	w, h := 5, 3
	m := make([]bool, w*h)
	set := func(x, y int) { m[y*w+x] = true }
	set(0, 0)
	set(1, 0) // blob A
	set(4, 2) // blob B (isolated)
	labels, n := label(m, w, h)
	if n != 2 {
		t.Fatalf("component count = %d, want 2", n)
	}
	if labels[0] != labels[1] {
		t.Fatalf("adjacent pixels got different labels: %d %d", labels[0], labels[1])
	}
	if labels[2*w+4] == 0 {
		t.Fatalf("isolated pixel unlabeled")
	}
}

func TestAsinhStretch_MonotonicInRange(t *testing.T) {
	im := fits.NewImage(4, 1, 1)
	im.Pix[0] = []float32{0.0, 0.1, 0.4, 1.0}
	asinhStretch(im, 30, 99.9, 35, false)
	p := im.Pix[0]
	for i := 1; i < len(p); i++ {
		if p[i] < p[i-1] {
			t.Fatalf("not monotonic at %d: %v < %v", i, p[i], p[i-1])
		}
		if p[i] < 0 || p[i] > 1 {
			t.Fatalf("out of range at %d: %v", i, p[i])
		}
	}
}

func TestCompositeLayers_Blends(t *testing.T) {
	sky := fits.NewImage(2, 1, 3)
	fg := fits.NewImage(2, 1, 3)
	for c := 0; c < 3; c++ {
		sky.Pix[c] = []float32{1, 1}
		fg.Pix[c] = []float32{0, 0}
	}
	alpha := []float32{1, 0} // pixel0 = sky, pixel1 = fg
	out, err := compositeLayers(sky, fg, alpha)
	if err != nil {
		t.Fatal(err)
	}
	if out.Pix[0][0] != 1 || out.Pix[0][1] != 0 {
		t.Fatalf("composite blend wrong: %v", out.Pix[0])
	}
}

func TestBuildSkyAlpha_BrightTopIsSky(t *testing.T) {
	// Bright top half (sky) over a dark bottom half (foreground): alpha→1 at top, →0 at bottom.
	w, h := 20, 20
	im := fits.NewImage(w, h, 3)
	for y := 0; y < h; y++ {
		val := float32(0.8) // sky: top 60%
		if y >= 12 {
			val = 0.001 // foreground: bottom 40% (a clear minority, as real horizons are)
		}
		for x := 0; x < w; x++ {
			for c := 0; c < 3; c++ {
				im.Pix[c][y*w+x] = val
			}
		}
	}
	alpha := buildSkyAlpha(im, 45, 1, 2)
	top := alpha[2*w+w/2]     // near top row
	bot := alpha[(h-2)*w+w/2] // near bottom row
	if top < 0.6 {
		t.Fatalf("top (sky) alpha too low: %v", top)
	}
	if bot > 0.4 {
		t.Fatalf("bottom (foreground) alpha too high: %v", bot)
	}
}

func TestRotate_DimsAndInvolution(t *testing.T) {
	im := fits.NewImage(3, 2, 1)
	for i := range im.Pix[0] {
		im.Pix[0][i] = float32(i)
	}
	r := rotate90(im, true)
	if r.W != 2 || r.H != 3 {
		t.Fatalf("rotate90 dims = %dx%d, want 2x3", r.W, r.H)
	}
	back := rotate180(rotate180(im))
	for i := range im.Pix[0] {
		if back.Pix[0][i] != im.Pix[0][i] {
			t.Fatalf("rotate180 twice not identity at %d", i)
		}
	}
}

func TestSRGBRoundTrip(t *testing.T) {
	im := fits.NewImage(5, 1, 1)
	orig := []float32{0, 0.05, 0.2, 0.5, 1.0}
	copy(im.Pix[0], orig)
	linearizeSRGB(im)
	for i, v := range im.Pix[0] {
		back := encodeSRGB(float64(v))
		if math.Abs(back-float64(orig[i])) > 1e-4 {
			t.Fatalf("sRGB round-trip at %d: %v -> %v", i, orig[i], back)
		}
	}
}
