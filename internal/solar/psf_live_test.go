package solar

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/noise"
)

// psf_live_test.go measures what a real stacked master actually resolves, and renders the finish at
// several sharpening settings so the choice can be looked at rather than argued about. Opt-in:
//
//	ASTRO_SOLAR_MASTERS='output/<run>/master_w01.fits,...' ASTRO_SOLAR_OUT=/tmp/x \
//	  go test ./internal/solar -run PSF_Live -v
func TestPSF_Live(t *testing.T) {
	list := os.Getenv("ASTRO_SOLAR_MASTERS")
	if list == "" {
		t.Skip("set ASTRO_SOLAR_MASTERS=<comma-separated master paths> to run the PSF measurement")
	}
	outDir := os.Getenv("ASTRO_SOLAR_OUT")

	for _, path := range strings.Split(list, ",") {
		if path = strings.TrimSpace(path); path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join("..", "..", path)
		}
		t.Run(filepath.Base(filepath.Dir(filepath.Dir(path)))+"/"+filepath.Base(path), func(t *testing.T) {
			im, err := fits.ReadImage(path)
			require.NoError(t, err)
			mono := firstPlane(im)
			l, ok := FitLimb(mono)
			require.True(t, ok, "the master must carry a fittable limb")

			psf := MeasurePSF(mono, l)
			require.True(t, psf.OK, "the limb must yield a point spread function")
			sigma, over := psf.SigmaPx, psf.Overshoot
			t.Logf("%s: %dx%d, R=%.1f px, %.2f\"/px", filepath.Base(path), mono.W, mono.H, l.R,
				sunAngularDiameterArcsec/(2*l.R))
			t.Logf("  limb edge: PSF sigma %.2f px (FWHM %.2f px = %.1f\"), overshoot %+.1f%%",
				sigma, 2.355*sigma, psf.FWHMArcsec, 100*over)

			n := noise.Measure(mono).Sigma
			_, coeffs := noise.Decompose(mono.Pix[0], mono.W, mono.H, 5)
			for s, c := range coeffs {
				t.Logf("  starlet scale %d (~%d px): rms %.3g = %.1f x the frame noise",
					s, 1<<uint(s+1), planeRMS(c, mono, l), planeRMS(c, mono, l)/math.Max(n*scaleNoise(s), 1e-12))
			}

			if outDir == "" {
				return
			}
			require.NoError(t, os.MkdirAll(outDir, 0o755))
			base := filepath.Base(filepath.Dir(filepath.Dir(path))) + "_" + strings.TrimSuffix(filepath.Base(path), ".fits")
			for _, v := range finishVariants(sigma) {
				o := v.opts
				if o.DeconvAuto {
					var notes []string
					o, _, notes = ResolveFinish(mono, l, o)
					for _, n := range notes {
						t.Logf("  %s: %s", v.name, n)
					}
				}
				img := Finish(mono, l, o)
				dst := filepath.Join(outDir, fmt.Sprintf("%s_%s.png", base, v.name))
				require.NoError(t, WritePNG(img, dst))
				t.Logf("  wrote %s", dst)
			}
		})
	}
}

type finishVariant struct {
	name string
	opts FinishOptions
}

// finishVariants is the ladder from "no sharpening at all" up to the current default, plus the
// settings the measured PSF argues for.
func finishVariants(psf float64) []finishVariant {
	var out []finishVariant
	add := func(name string, sigma float64, iters int, gains []float64, thr []float64) {
		o := DefaultFinish()
		o.DeconvSigma, o.DeconvIters = sigma, iters
		o.Sharpen = SharpenOptions{DeconvSigma: sigma, DeconvIters: iters, Gains: gains, Thresholds: thr}
		out = append(out, finishVariant{name: name, opts: o})
	}
	flat := []float64{1, 1, 1, 1, 1}
	soft := []float64{0.35, 0.85, 1.20, 1.20, 1.05}
	cur := []float64{0.8, 1.15, 1.35, 1.25, 1.10}
	add("a_raw", 0, 0, flat, []float64{0, 0, 0, 0, 0})
	add("b_default", 2.0, 50, cur, []float64{4, 2, 1, 0, 0})
	add("c_nodeconv_soft", 0, 0, soft, []float64{6, 3, 1, 0, 0})
	add("d_psf_soft", psf, 12, soft, []float64{6, 3, 1, 0, 0})
	add("e_psf_mid", psf, 25, soft, []float64{6, 3, 1, 0, 0})
	add("f_psf_cur", psf, 25, cur, []float64{4, 2, 1, 0, 0})
	out = append(out, finishVariant{name: "g_shipping", opts: DefaultFinish()})
	return out
}

// planeRMS is the robust scale of a starlet plane, measured on the disc only.
func planeRMS(plane []float32, im *fits.Image, l Limb) float64 {
	var vals []float64
	r2 := (0.8 * l.R) * (0.8 * l.R)
	for y := 0; y < im.H; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < im.W; x++ {
			dx := float64(x) - l.CX
			if dx*dx+dy*dy <= r2 {
				vals = append(vals, math.Abs(float64(plane[y*im.W+x])))
			}
		}
	}
	return 1.4826 * median(vals)
}
