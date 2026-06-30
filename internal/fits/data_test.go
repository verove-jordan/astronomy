package fits

import (
	"math"
	"path/filepath"
	"testing"
)

func TestImage_FITSRoundTrip(t *testing.T) {
	tests := []struct{ name string; w, h, c int }{
		{"mono", 5, 4, 1},
		{"rgb", 7, 3, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src := NewImage(tt.w, tt.h, tt.c)
			for c := 0; c < tt.c; c++ {
				for i := range src.Pix[c] {
					src.Pix[c][i] = float32(c)*0.25 + float32(i)*0.001
				}
			}
			path := filepath.Join(t.TempDir(), "rt.fits")
			if err := src.WriteFITS(path); err != nil {
				t.Fatalf("write: %v", err)
			}
			got, err := ReadImage(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if got.W != tt.w || got.H != tt.h || got.C != tt.c {
				t.Fatalf("dims: got %dx%dx%d want %dx%dx%d", got.W, got.H, got.C, tt.w, tt.h, tt.c)
			}
			for c := 0; c < tt.c; c++ {
				for i := range src.Pix[c] {
					if math.Abs(float64(got.Pix[c][i]-src.Pix[c][i])) > 1e-6 {
						t.Fatalf("pixel c=%d i=%d: got %v want %v", c, i, got.Pix[c][i], src.Pix[c][i])
					}
				}
			}
		})
	}
}
