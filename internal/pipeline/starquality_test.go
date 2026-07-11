package pipeline

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

func TestStarReport_NeedsFix(t *testing.T) {
	base := starReport{Detected: 100, TrueSat: 0.3, TrueSpread: 0.1, FinalBurnt: 0.004, FinalWarm: 0.3, FinalSpread: 0.10}
	tests := []struct {
		name string
		mod  func(*starReport)
		want bool
	}{
		{"clean", func(r *starReport) {}, false},
		{"too few stars", func(r *starReport) { r.Detected = 10; r.FinalBurnt = 0.5 }, false},
		{"burnt cores", func(r *starReport) { r.FinalBurnt = 0.05 }, true},
		{"flattened colour, was colourful", func(r *starReport) { r.FinalSpread = 0.01 }, true},
		{"flattened but stars truly grey", func(r *starReport) { r.FinalSpread = 0.01; r.TrueSat = 0.05 }, false},
		{"uniformly warm, varied truth", func(r *starReport) { r.FinalWarm = 0.8 }, true},
		{"warm but truly warm field", func(r *starReport) { r.FinalWarm = 0.8; r.TrueSpread = 0.01 }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := base
			tt.mod(&r)
			if got := r.needsFix(); got != tt.want {
				t.Errorf("needsFix() = %v, want %v (%+v)", got, tt.want, r)
			}
		})
	}
}

func TestMaxWhiteClip(t *testing.T) {
	m := finishMetrics{WhiteClip: [3]float64{0.10, 0.50, 0.20}}
	if got := maxWhiteClip(m); got != 0.50 {
		t.Errorf("maxWhiteClip = %.3f, want 0.50", got)
	}
}

func TestMedianOf(t *testing.T) {
	tests := []struct {
		name string
		in   []float64
		want float64
	}{
		{"empty", nil, 0},
		{"odd", []float64{3, 1, 2}, 2},
		{"even", []float64{4, 1, 3, 2}, 2.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := medianOf(tt.in); got != tt.want {
				t.Errorf("medianOf(%v) = %.3f, want %.3f", tt.in, got, tt.want)
			}
		})
	}
}

func TestStddevOf(t *testing.T) {
	tests := []struct {
		name string
		in   []float64
		want float64
	}{
		{"single", []float64{5}, 0},
		{"flat", []float64{2, 2, 2}, 0},
		{"spread", []float64{1, 3}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := stddevOf(tt.in); math.Abs(got-tt.want) > 1e-9 {
				t.Errorf("stddevOf(%v) = %.4f, want %.4f", tt.in, got, tt.want)
			}
		})
	}
}

// writeStarFieldFITS writes a low-background RGB star field to a temp FITS and returns its path.
func writeStarFieldFITS(t *testing.T, w, h, spacing int) string {
	t.Helper()
	r := make([]float32, w*h)
	g := make([]float32, w*h)
	b := make([]float32, w*h)
	for i := range r {
		r[i], g[i], b[i] = 0.02, 0.02, 0.02
	}
	for y := spacing; y < h-spacing; y += spacing {
		for x := spacing; x < w-spacing; x += spacing {
			idx := y*w + x
			if (x/spacing+y/spacing)%2 == 0 {
				r[idx], g[idx], b[idx] = 0.20, 0.40, 0.70
			} else {
				r[idx], g[idx], b[idx] = 0.70, 0.40, 0.20
			}
		}
	}
	im := &fits.Image{W: w, H: h, C: 3, Pix: [][]float32{r, g, b}}
	path := filepath.Join(t.TempDir(), "rgb_base.fits")
	if err := im.WriteFITS(path); err != nil {
		t.Fatalf("write synthetic base: %v", err)
	}
	return path
}

func TestAnalyzeStars_PropagatesFinalMetricsAndDetects(t *testing.T) {
	path := writeStarFieldFITS(t, 160, 160, 16)
	m := finishMetrics{WhiteClip: [3]float64{0.01, 0.06, 0.02}, StarWarmFrac: 0.4, StarColorSpread: 0.07}
	rep, err := analyzeStars(path, m)
	if err != nil {
		t.Fatalf("analyzeStars: %v", err)
	}
	if rep.Detected < 30 {
		t.Errorf("Detected = %d, want ≥ 30", rep.Detected)
	}
	if rep.FinalBurnt != 0.06 || rep.FinalWarm != 0.4 || rep.FinalSpread != 0.07 {
		t.Errorf("final metrics not propagated: %+v", rep)
	}
	if rep.TrueSat <= 0.2 || rep.TrueSpread <= 0 {
		t.Errorf("expected a colourful varied true field, got TrueSat=%.3f TrueSpread=%.3f", rep.TrueSat, rep.TrueSpread)
	}
}

func TestAnalyzeStars_MissingBase(t *testing.T) {
	if _, err := analyzeStars(filepath.Join(t.TempDir(), "nope.fits"), finishMetrics{}); err == nil {
		t.Error("expected an error for a missing base file")
	}
}
