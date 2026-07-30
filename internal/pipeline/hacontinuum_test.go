package pipeline

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

func writeHaTestFITS(t *testing.T, dir, name string, w, h int, fill func(x, y int) float32) string {
	t.Helper()
	im := fits.NewImage(w, h, 1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.Pix[0][y*w+x] = fill(x, y)
		}
	}
	p := filepath.Join(dir, name)
	if err := im.WriteFITS(p); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestHaContinuumSubtract pins the emission isolation: with Ha = 0.5·R + blob, the fitted k must be
// 0.5, continuum/star pixels must cancel to ~0 in the excess layer, and the blob must survive intact.
func TestHaContinuumSubtract(t *testing.T) {
	const w, h = 512, 256
	dir := t.TempDir()
	// Smooth continuum ramp: the P85–P99.9 selection band is well-populated and blob-free (the blob
	// sits at low x, where the reference is dim). File names + channel entries mirror production:
	// Siril-relative base names ("combine_R"), no directory, no extension — the resolver must map
	// them onto <workDir>/<name>.fits exactly as Siril `load` does.
	writeHaTestFITS(t, dir, "combine_R.fits", w, h, func(x, y int) float32 {
		return 0.1 + 0.5*float32(x)/float32(w)
	})
	inBlob := func(x, y int) bool { return x < 64 && y < 64 }
	writeHaTestFITS(t, dir, "combine_Ha.fits", w, h, func(x, y int) float32 {
		v := 0.5 * (0.1 + 0.5*float32(x)/float32(w))
		if inBlob(x, y) {
			v += 0.3
		}
		return v
	})

	hc, why := haContinuumSubtract(dir, map[string]string{"Ha": "combine_Ha", "R": "combine_R"}, dir)
	if hc == nil {
		t.Fatalf("subtraction failed: %s", why)
	}
	if math.Abs(hc.K-0.5) > 0.01 {
		t.Fatalf("k = %.4f, want 0.5", hc.K)
	}
	if hc.Ref != "R" {
		t.Fatalf("ref = %s, want R", hc.Ref)
	}
	if hc.Faint {
		t.Fatal("a 0.3 emission blob must not be classified faint")
	}
	ex, err := fits.ReadImage(hc.ExcessPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := ex.Pix[0][32*w+32]; math.Abs(float64(got)-0.3) > 0.01 {
		t.Fatalf("blob excess = %.4f, want 0.3", got)
	}
	if got := ex.Pix[0][200*w+400]; got > 0.005 {
		t.Fatalf("continuum excess = %.4f, want ~0", got)
	}
}

// TestHaContinuumSubtract_Faint: an Ha layer that is pure scaled continuum + noise has no emission —
// the excess must be flagged faint so the caller drops the screen instead of painting noise red.
func TestHaContinuumSubtract_Faint(t *testing.T) {
	const w, h = 512, 256
	dir := t.TempDir()
	ref := writeHaTestFITS(t, dir, "r.fits", w, h, func(x, y int) float32 {
		return 0.1 + 0.5*float32(x)/float32(w)
	})
	seed := uint32(1)
	noise := func() float32 {
		seed = seed*1664525 + 1013904223
		return (float32(seed%1000)/1000 - 0.5) * 0.008
	}
	ha := writeHaTestFITS(t, dir, "ha.fits", w, h, func(x, y int) float32 {
		return 0.5*(0.1+0.5*float32(x)/float32(w)) + noise()
	})

	hc, why := haContinuumSubtract(dir, map[string]string{"Ha": ha, "R": ref}, dir)
	if hc == nil {
		t.Fatalf("subtraction failed: %s", why)
	}
	if !hc.Faint {
		t.Fatalf("noise-only excess must be faint (P99.5 = %.2fσ)", hc.P995Sigma)
	}
}

// TestHaContinuumSubtract_NoReference: without a broadband channel the subtraction reports why and
// the caller keeps the raw-layer path.
func TestHaContinuumSubtract_NoReference(t *testing.T) {
	dir := t.TempDir()
	ha := writeHaTestFITS(t, dir, "ha.fits", 32, 32, func(x, y int) float32 { return 0.2 })
	hc, why := haContinuumSubtract(dir, map[string]string{"Ha": ha}, dir)
	if hc != nil || why == "" {
		t.Fatalf("want nil + reason, got %+v / %q", hc, why)
	}
}
