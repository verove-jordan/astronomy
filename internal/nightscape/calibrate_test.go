package nightscape

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

func TestMedianInPlace(t *testing.T) {
	tests := []struct {
		name string
		in   []float32
		want float32
	}{
		{"odd", []float32{3, 1, 2}, 2},
		{"even", []float32{4, 1, 3, 2}, 2.5},
		{"single", []float32{7}, 7},
		{"with outlier", []float32{1, 1, 1, 1, 100}, 1}, // median rejects the hot pixel
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := medianInPlace(append([]float32(nil), tt.in...)); got != tt.want {
				t.Fatalf("median(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestNormalizeFlat_MeanOne(t *testing.T) {
	flat := fits.NewImage(4, 1, 1)
	flat.Pix[0] = []float32{1, 2, 3, 4} // mean 2.5
	if !normalizeFlat(flat) {
		t.Fatal("normalizeFlat reported the flat unusable")
	}
	var sum float64
	for _, v := range flat.Pix[0] {
		sum += float64(v)
	}
	if mean := sum / 4; math.Abs(mean-1) > 1e-6 {
		t.Fatalf("normalized flat mean = %v, want 1", mean)
	}
	// A zero-mean flat is unusable.
	zero := fits.NewImage(2, 1, 1)
	if normalizeFlat(zero) {
		t.Fatal("normalizeFlat accepted an all-zero flat")
	}
}

// TestCalibrationMath verifies the in-memory calibration arithmetic recovers a known signal:
// light = signal·flat + dark, so (light − dark) / flatNorm should return the signal (up to the flat's
// mean, which normalizeFlat removes).
func TestCalibrationMath(t *testing.T) {
	const n = 6
	signal := []float32{0.10, 0.20, 0.30, 0.40, 0.50, 0.60}
	flatRaw := []float32{0.8, 0.9, 1.0, 1.1, 1.2, 1.0} // vignetting profile (mean 1.0 here)
	dark := []float32{0.02, 0.02, 0.02, 0.02, 0.02, 0.02}

	light := fits.NewImage(n, 1, 1)
	for i := 0; i < n; i++ {
		light.Pix[0][i] = signal[i]*flatRaw[i] + dark[i]
	}
	darkM := fits.NewImage(n, 1, 1)
	copy(darkM.Pix[0], dark)
	flatM := fits.NewImage(n, 1, 1)
	copy(flatM.Pix[0], flatRaw)

	subtractImage(light, darkM)
	if !normalizeFlat(flatM) {
		t.Fatal("flat unusable")
	}
	divideImage(light, flatM)

	for i := 0; i < n; i++ {
		if math.Abs(float64(light.Pix[0][i]-signal[i])) > 1e-5 {
			t.Fatalf("pixel %d: calibrated %v, want %v", i, light.Pix[0][i], signal[i])
		}
	}
}

func TestDivideImage_GuardsTinyFlat(t *testing.T) {
	im := fits.NewImage(2, 1, 1)
	im.Pix[0] = []float32{0.5, 0.5}
	flat := fits.NewImage(2, 1, 1)
	flat.Pix[0] = []float32{1.0, 1e-9} // second value below the guard → left unchanged
	divideImage(im, flat)
	if im.Pix[0][0] != 0.5 || im.Pix[0][1] != 0.5 {
		t.Fatalf("divideImage mishandled tiny flat: %v", im.Pix[0])
	}
}

func TestHasCalibration(t *testing.T) {
	if hasCalibration(Options{}) {
		t.Fatal("no cal dirs should report false")
	}
	if !hasCalibration(Options{DarkDir: "/d"}) {
		t.Fatal("a dark dir should report true")
	}
}

// TestCalibrateLights_UnreadableLight is the soft-fail contract: an unreadable reference light (needed
// first for its dimensions) returns a "skipped" note and never touches the lights.
func TestCalibrateLights_UnreadableLight(t *testing.T) {
	o := Options{DarkDir: t.TempDir(), WorkDir: t.TempDir()}
	note := calibrateLights(context.Background(), o, calPlan{}, t.TempDir(), []string{"/does/not/matter.fits"})
	if !strings.Contains(note, "skipped") {
		t.Fatalf("expected a 'skipped' note from an unreadable light, got %q", note)
	}
}

// TestCalibrateLights_NoLights returns early (no work) when there are no lights.
func TestCalibrateLights_NoLights(t *testing.T) {
	if note := calibrateLights(context.Background(), Options{DarkDir: t.TempDir(), WorkDir: t.TempDir()}, calPlan{}, t.TempDir(), nil); note != "" {
		t.Fatalf("no lights should return no note, got %q", note)
	}
}

// TestPixelMath_SurvivesAMismatchedMaster: a master larger or smaller than the light must degrade,
// never panic. A run once died here — the dimension guard was checked against ONE reference light and
// then the master was applied to every light, so a folder holding two sensor resolutions ran the
// subtraction off the end of the smaller plane.
func TestPixelMath_SurvivesAMismatchedMaster(t *testing.T) {
	tests := []struct {
		name            string
		lightN, masterN int
	}{
		{"master smaller than the light", 8, 4},
		{"master larger than the light", 4, 8},
		{"same size", 6, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			light := fits.NewImage(tt.lightN, 1, 1)
			for i := range light.Pix[0] {
				light.Pix[0][i] = 0.5
			}
			master := fits.NewImage(tt.masterN, 1, 1)
			for i := range master.Pix[0] {
				master.Pix[0][i] = 0.25
			}

			subtractImage(light, master) // must not panic
			divideImage(light, master)

			// The overlapping pixels are still calibrated: (0.5 - 0.25) / 0.25 = 1.
			if got := light.Pix[0][0]; math.Abs(float64(got)-1) > 1e-6 {
				t.Fatalf("first pixel calibrated to %v, want 1", got)
			}
		})
	}
}
