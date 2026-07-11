package nightscape

import (
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// normalizeADU must bring Siril's 16-bit convert output (0..65535 ADU) into the [0,1] range the
// linear recipe assumes, and leave an already-normalized float frame untouched — the fix for the
// calibration/foreground blow-up that made every calibrated light uniform white (register failed).
func TestNormalizeADU(t *testing.T) {
	tests := []struct {
		name   string
		vals   []float32
		wantMx float32
	}{
		{"16-bit convert output scaled to [0,1]", []float32{0, 32768, 62914}, 62914.0 / 65535},
		{"already [0,1] left untouched", []float32{0, 0.5, 0.97}, 0.97},
		{"a single low ADU frame still scales (max>1.5)", []float32{0, 2, 100}, 100.0 / 65535},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			im := &fits.Image{W: len(tt.vals), H: 1, C: 1, Pix: [][]float32{append([]float32(nil), tt.vals...)}}
			normalizeADU(im)
			mx := im.Pix[0][0]
			for _, v := range im.Pix[0] {
				if v > mx {
					mx = v
				}
			}
			if diff := mx - tt.wantMx; diff > 1e-5 || diff < -1e-5 {
				t.Fatalf("max = %v, want %v", mx, tt.wantMx)
			}
			// After normalization every value must be in the sRGB-valid [0,1] domain.
			for _, v := range im.Pix[0] {
				if v < 0 || v > 1.0001 {
					t.Fatalf("value %v out of [0,1] after normalize", v)
				}
			}
		})
	}
}
