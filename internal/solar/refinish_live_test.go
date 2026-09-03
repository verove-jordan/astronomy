package solar

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestRefinish_Live re-renders a persisted master from a real run, so the finish can be iterated on
// without paying for the ingest and the stack again.
//
// A solar run spends almost all of its time before the finish — on the 12 Aug clip, twenty-four
// minutes scanning thirty-one thousand frames against about two minutes rendering. The finish is
// also where most of what goes wrong is visible. Re-entering it from the master the run persisted is
// the difference between a half-hour iteration and a ten-second one.
//
//	ASTRO_SOLAR_MASTER=output/<object>/<run>/master_w01.fits go test ./internal/solar -run Refinish -v
func TestRefinish_Live(t *testing.T) {
	path := os.Getenv("ASTRO_SOLAR_MASTER")
	if path == "" {
		t.Skip("set ASTRO_SOLAR_MASTER=<a run's master_wNN.fits>")
	}
	im, err := fits.ReadImage(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	mono := firstPlane(im)
	g, ok := FitPair(mono)
	if !ok {
		t.Fatal("no solar limb in the master")
	}
	t.Logf("geometry: sun r=%.1f at (%.1f,%.1f) | occulter r=%.1f at (%.1f,%.1f) | obscuration %.1f%%",
		g.Sun.R, g.Sun.CX, g.Sun.CY, g.Moon.R, g.Moon.CX, g.Moon.CY, 100*g.Obscuration)

	// What the finish's statistics see, before and after the occulted region is filled.
	vis := g.VisibleSunMask(mono.W, mono.H, 0)
	t.Logf("master: visible-Sun median %.5f | whole-disc median %.5f",
		MaskedMedian(mono.Pix[0], vis),
		float64(medianOfPlane(onDiscSamples(mono.Pix[0], mono.W, mono.H, g.Sun, 0.5, nil))))
	t.Logf("masked: visible-Sun median inside 0.5R %.5f",
		float64(medianOfPlane(onDiscSamples(mono.Pix[0], mono.W, mono.H, g.Sun, 0.5, occulterSkip(g)))))

	fin, psf, notes := ResolveFinish(mono, g.Sun, DefaultFinish())
	fin.Palette = os.Getenv("ASTRO_SOLAR_PALETTE")
	if fin.Palette == "" {
		fin.Palette = PaletteGold
	}
	for _, n := range notes {
		t.Log("  ", n)
	}
	t.Logf("psf: sigma %.2f px, fwhm %.1f\", ok=%v", psf.SigmaPx, psf.FWHMArcsec, psf.OK)

	img := FinishPair(mono, g, fin)
	dst := filepath.Join(os.Getenv("ASTRO_SOLAR_OUT"), "refinish.png")
	if os.Getenv("ASTRO_SOLAR_OUT") == "" {
		dst = filepath.Join(t.TempDir(), "refinish.png")
	}
	if err := WritePNG(img, dst); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Log("wrote", dst)
}
