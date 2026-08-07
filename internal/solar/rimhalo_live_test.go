package solar

import (
	"math"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// TestRimHalo_Live profiles the two artefacts that survive the finish: a dark rim just inside the
// limb, and structure in the sky beyond it.
//
// Both are radial-looking, which makes them easy to misdiagnose. The fix for each depends on one
// question this measures directly: is the feature AZIMUTHALLY SYMMETRIC about the disc centre? The
// off-limb correction subtracts a per-radius median about that centre, so it can only remove things
// that are. A vignette or field stop from the eyepiece is centred on the optical axis, not on the
// Sun, and no amount of radial-median subtraction about the wrong centre will take it out — it will
// just carve a ring-shaped bite out of the sky.
//
// The spread column is the discriminator: p90−p10 within each annulus, relative to the disc. Small
// means symmetric and removable radially; large means it is not, and the correction must be
// two-dimensional or must not be attempted at all.
//
//	ASTRO_SOLAR_MASTER=<master.fits> go test ./internal/solar -run RimHalo -v
func TestRimHalo_Live(t *testing.T) {
	master := os.Getenv("ASTRO_SOLAR_MASTER")
	if master == "" {
		t.Skip("set ASTRO_SOLAR_MASTER=<master.fits>")
	}
	im, err := fits.ReadImage(master)
	require.NoError(t, err)
	mono := firstPlane(im)
	l, ok := FitLimb(mono)
	require.True(t, ok)

	discRef := imgops.Percentile(imgops.Subsample(onDiscSamples(mono.Pix[0], mono.W, mono.H, l, 0.5), 100000), 50)
	t.Logf("master %dx%d r=%.1f at (%.1f,%.1f) | disc level %.4f", mono.W, mono.H, l.R, l.CX, l.CY, discRef)

	// The finished image, so the rim is measured where it is actually seen.
	fin := Finish(mono, l, DefaultFinish())

	t.Logf("%8s %10s %10s %10s %10s", "r/R", "linear", "fin.med", "fin.p10", "fin.p90")
	for _, frac := range []float64{0.80, 0.90, 0.94, 0.96, 0.97, 0.98, 0.99, 1.00,
		1.01, 1.02, 1.05, 1.10, 1.20, 1.30, 1.45, 1.60} {
		lo, hi := frac-0.005, frac+0.005
		lin := annulusSamples(mono.Pix[0], mono.W, mono.H, l, lo, hi)
		fm := annulusSamples(fin.Pix[0], fin.W, fin.H, l, lo, hi)
		if len(lin) < 64 || len(fm) < 64 {
			continue
		}
		t.Logf("%8.2f %10.4f %10.4f %10.4f %10.4f", frac,
			float64(imgops.Percentile(imgops.Subsample(lin, 60000), 50)),
			float64(imgops.Percentile(imgops.Subsample(fm, 60000), 50)),
			float64(imgops.Percentile(imgops.Subsample(fm, 60000), 10)),
			float64(imgops.Percentile(imgops.Subsample(fm, 60000), 90)))
	}

	// Is the off-limb background symmetric about the disc centre? Compare the azimuthal spread of
	// the LINEAR sky against the halo's own height, in units of the disc.
	t.Logf("off-limb asymmetry (linear, as a fraction of the disc level):")
	sky := offLimbLevel(mono.Pix[0], mono.W, mono.H, l)
	for _, frac := range []float64{1.02, 1.05, 1.10, 1.20, 1.30, 1.45} {
		v := annulusSamples(mono.Pix[0], mono.W, mono.H, l, frac-0.01, frac+0.01)
		if len(v) < 256 {
			continue
		}
		s := imgops.Subsample(v, 60000)
		p10 := float64(imgops.Percentile(s, 10))
		p50 := float64(imgops.Percentile(s, 50))
		p90 := float64(imgops.Percentile(s, 90))
		t.Logf("  r/R %.2f: median %+.5f  spread(p90-p10) %.5f  (halo above sky %+.5f)",
			frac, (p50-sky)/discRef, (p90-p10)/discRef, (p50-sky)/discRef)
	}
	// Quadrant medians expose a background centred somewhere other than the disc.
	t.Logf("quadrant medians of the sky at r/R 1.05-1.25 (fraction of disc):")
	for _, q := range []struct {
		name   string
		a0, a1 float64
	}{{"E  ", -45, 45}, {"N  ", 45, 135}, {"W  ", 135, 225}, {"S  ", 225, 315}} {
		v := sectorSamples(mono.Pix[0], mono.W, mono.H, l, 1.05, 1.25, q.a0, q.a1)
		if len(v) < 256 {
			continue
		}
		m := float64(imgops.Percentile(imgops.Subsample(v, 60000), 50))
		t.Logf("  %s %+.5f", q.name, (m-sky)/discRef)
	}
}

// sectorSamples collects pixels in a radial band restricted to an angular wedge.
func sectorSamples(p []float32, w, h int, l Limb, lo, hi, a0, a1 float64) []float32 {
	var out []float32
	lo2, hi2 := (lo*l.R)*(lo*l.R), (hi*l.R)*(hi*l.R)
	for y := 0; y < h; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < w; x++ {
			dx := float64(x) - l.CX
			d2 := dx*dx + dy*dy
			if d2 < lo2 || d2 > hi2 {
				continue
			}
			a := math.Atan2(dy, dx) * 180 / math.Pi
			for a < a0 {
				a += 360
			}
			if a <= a1 {
				out = append(out, p[y*w+x])
			}
		}
	}
	return out
}
