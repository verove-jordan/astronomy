package nightscape

import (
	"context"
	"math"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// TestDeriveSolve_PlateScale checks the EXIF-focal → plate-scale derivation: a 24 mm-equivalent phone
// field over a 4032 px frame solves at ~66 arcsec/px, and the synthesized focal/pixel encode that scale.
func TestDeriveSolve_PlateScale(t *testing.T) {
	got := deriveSolve(siril.SolveOptions{}, 24, 4032)
	if got.FocalMM <= 0 || got.PixelUm <= 0 {
		t.Fatalf("expected a synthesized focal+pixel, got %+v", got)
	}
	scale := 206.265 * got.PixelUm / got.FocalMM // arcsec/px Siril will compute
	want := 2 * math.Atan(36.0/(2*24)) * (180 / math.Pi) * 3600 / 4032
	if math.Abs(scale-want) > 0.5 {
		t.Fatalf("plate scale %.2f arcsec/px, want %.2f", scale, want)
	}
}

func TestDeriveSolve_NoOpCases(t *testing.T) {
	base := siril.SolveOptions{FocalMM: 700, PixelUm: 3.8}
	if got := deriveSolve(base, 24, 4032); got != base {
		t.Fatal("an already-set focal must be left untouched")
	}
	if got := deriveSolve(siril.SolveOptions{}, 0, 4032); got.FocalMM != 0 {
		t.Fatal("focal35=0 must leave the scale to Siril")
	}
}

func TestHasNonFinite(t *testing.T) {
	good := fits.NewImage(3, 1, 1)
	good.Pix[0] = []float32{0, 0.5, 1}
	if hasNonFinite(good) {
		t.Fatal("finite image flagged as non-finite")
	}
	nan := fits.NewImage(3, 1, 1)
	nan.Pix[0] = []float32{0, float32(math.NaN()), 1}
	if !hasNonFinite(nan) {
		t.Fatal("NaN not detected")
	}
	inf := fits.NewImage(3, 1, 1)
	inf.Pix[0] = []float32{0, float32(math.Inf(1)), 1}
	if !hasNonFinite(inf) {
		t.Fatal("Inf not detected")
	}
}

// TestEnhanceSky_SoftFailNilRunners is the soft-fail contract: with no Graxpert/Siril runners (and no
// SPCC sensor), enhanceSky is a pure no-op — it must not touch the filesystem, panic, or alter the sky.
// Gradient flattening + neutralisation now live upstream (removeSkyGradient + autoStretch), so enhanceSky
// only runs the optional GraXpert denoise / SPCC enhancements when their runner is present.
func TestEnhanceSky_SoftFailNilRunners(t *testing.T) {
	sky := fits.NewImage(8, 8, 3)
	for i := 0; i < 64; i++ {
		sky.Pix[0][i] = 0.10
		sky.Pix[1][i] = 0.05
		sky.Pix[2][i] = 0.05
	}
	before := sky.Clone()
	res := &Result{}
	enhanceSky(context.Background(), sky, Options{}, res)
	if len(res.Warnings) != 0 {
		t.Fatalf("nil runners should produce no warnings, got %v", res.Warnings)
	}
	for c := 0; c < 3; c++ {
		for i := range sky.Pix[c] {
			if sky.Pix[c][i] != before.Pix[c][i] {
				t.Fatalf("nil-runner enhanceSky altered the sky at ch%d[%d]: %v != %v", c, i, sky.Pix[c][i], before.Pix[c][i])
			}
		}
	}
}
