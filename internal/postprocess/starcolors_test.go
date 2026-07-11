package postprocess

import (
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// syntheticStarField builds a low-background RGB image with single-pixel stars on a grid, half warm
// (R>B) and half cool (B>R), so detection + colour measurement have a known, varied population.
func syntheticStarField(w, h, spacing int) (*fits.Image, int) {
	r := make([]float32, w*h)
	g := make([]float32, w*h)
	b := make([]float32, w*h)
	for i := range r {
		r[i], g[i], b[i] = 0.02, 0.02, 0.02 // dark sky
	}
	warm := [3]float32{0.70, 0.40, 0.20}
	cool := [3]float32{0.20, 0.40, 0.70}
	n := 0
	for y := spacing; y < h-spacing; y += spacing {
		for x := spacing; x < w-spacing; x += spacing {
			c := warm
			if (x/spacing+y/spacing)%2 == 0 {
				c = cool
			}
			idx := y*w + x
			r[idx], g[idx], b[idx] = c[0], c[1], c[2]
			n++
		}
	}
	return &fits.Image{W: w, H: h, C: 3, Pix: [][]float32{r, g, b}}, n
}

func TestStarColors_DetectsVariedField(t *testing.T) {
	im, placed := syntheticStarField(160, 160, 16)
	got := StarColors(im, 0)
	if len(got) < placed/2 {
		t.Fatalf("detected %d of %d placed stars (want ≥ %d)", len(got), placed, placed/2)
	}
	warm, cool, coloured := 0, 0, 0
	for _, s := range got {
		if s.Sat() > 0.2 {
			coloured++
		}
		switch {
		case s.R > s.B:
			warm++
		case s.B > s.R:
			cool++
		}
	}
	if warm == 0 || cool == 0 {
		t.Errorf("expected both warm and cool stars, got warm=%d cool=%d", warm, cool)
	}
	if coloured < len(got)/2 {
		t.Errorf("expected most stars to read as coloured, got %d/%d", coloured, len(got))
	}
}

func TestStarColor_Sat(t *testing.T) {
	tests := []struct {
		name   string
		s      StarColor
		wantLo float64
		wantHi float64
	}{
		{"grey", StarColor{R: 0.5, G: 0.5, B: 0.5}, 0, 0},
		{"saturated red", StarColor{R: 1.0, G: 0.2, B: 0.0}, 0.99, 1.0},
		{"mild", StarColor{R: 1.0, G: 0.8, B: 0.8}, 0.19, 0.21},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.s.Sat()
			if got < tt.wantLo || got > tt.wantHi {
				t.Errorf("Sat() = %.4f, want in [%.4f,%.4f]", got, tt.wantLo, tt.wantHi)
			}
		})
	}
}

func TestStarColors_NonRGB(t *testing.T) {
	im := &fits.Image{W: 4, H: 4, C: 1, Pix: [][]float32{make([]float32, 16)}}
	if got := StarColors(im, 0); got != nil {
		t.Errorf("StarColors on a mono image = %v, want nil", got)
	}
}
