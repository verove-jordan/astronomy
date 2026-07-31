package solar

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/noise"
)

// TestSharpenSweep_Live renders one persisted master at a range of deconvolution strengths.
//
// This exists because of what the noise measurements showed: a stack of 300 phone-video frames
// converges onto the true detail while carrying roughly a seventeenth of a single frame's noise, and
// a single frame's apparent crispness is mostly codec noise in the same 2-5 px band as real solar
// structure. A stack therefore LOOKS flat beside a single frame while actually holding more real
// signal — and the way to cash that in is deconvolution, which is exactly what high signal-to-noise
// buys. Iteration counts that would amplify a single frame into mush are safe here.
//
// The reported detail is measured on the LINEAR result before tone mapping, so it tracks recovered
// structure rather than contrast.
//
//	ASTRO_SOLAR_MASTER=<master.fits> ASTRO_SOLAR_OUT=/tmp/sweep \
//	  go test ./internal/solar -run SharpenSweep -v
func TestSharpenSweep_Live(t *testing.T) {
	master := os.Getenv("ASTRO_SOLAR_MASTER")
	if master == "" {
		t.Skip("set ASTRO_SOLAR_MASTER=<master.fits>")
	}
	im, err := fits.ReadImage(master)
	require.NoError(t, err)
	mono := firstPlane(im)
	l, ok := FitLimb(mono)
	require.True(t, ok)

	sigma := noise.Measure(&fits.Image{W: mono.W, H: mono.H, C: 1, Pix: [][]float32{mono.Pix[0]}}).Sigma
	base := FrameSharpness(mono, l)
	t.Logf("master %dx%d r=%.0f | noise sigma %.3e | detail before finishing %.5f", mono.W, mono.H, l.R, sigma, base)

	out := os.Getenv("ASTRO_SOLAR_OUT")
	for _, c := range []struct {
		tag   string
		fwhm  float64
		iters int
	}{
		{"none", 0, 0},
		{"d1.4x12", 1.4, 12},
		{"d1.4x30", 1.4, 30},
		{"d1.4x60", 1.4, 60},
		{"d2.0x30", 2.0, 30},
		{"d2.0x60", 2.0, 60},
		{"d2.4x60", 2.4, 60},
	} {
		p := append([]float32(nil), mono.Pix[0]...)
		if c.iters > 0 {
			p = RichardsonLucy(p, mono.W, mono.H, l, c.fwhm, c.iters, sigma)
		}
		lin := &fits.Image{W: mono.W, H: mono.H, C: 1, Pix: [][]float32{p}}
		d := FrameSharpness(lin, l)
		// Ringing is the thing that goes wrong first: deconvolution overshoots at the limb long
		// before it overshoots on the disc, so this watches the sub-limb annulus for a dark rim.
		rim := annulusMedian(p, mono.W, mono.H, l, 0.955, 0.985)
		mid := annulusMedian(p, mono.W, mono.H, l, 0.30, 0.60)
		t.Logf("  %-9s detail %.5f (%4.0f%% of unsharpened)  rim/mid %.3f",
			c.tag, d, 100*d/math.Max(base, 1e-12), rim/math.Max(mid, 1e-12))
		if out != "" {
			o := DefaultFinish()
			o.DeconvSigma, o.DeconvIters = c.fwhm, c.iters
			require.NoError(t, WritePNG(Finish(mono, l, o), filepath.Join(out+"_"+c.tag+".png")))
		}
	}
	if out != "" {
		t.Logf("wrote %s_*.png", out)
	}
	fmt.Fprintln(os.Stderr)
}
