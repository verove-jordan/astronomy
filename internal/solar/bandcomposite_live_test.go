package solar

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// bandcomposite_live_test.go combines two masters shot at DIFFERENT etalon tunings.
//
// Rotating the etalon moves the passband across Hα, and the two images that come back are not two
// samples of one scene — they are two heights in the atmosphere. On band the chromosphere fills the
// disc with plage and filaments and swallows the sunspot; off band the line goes transparent, the
// continuum comes through, and the same spot is the sharpest thing in the frame. Averaging them is
// therefore wrong in the exact way averaging across a retune is wrong: it dilutes each with the
// other's absence.
//
// What is defensible is to composite by ORIGIN. The chromosphere is the picture; the wing is
// consulted only where it is genuinely dark, which on a quiet disc means the photospheric features
// the chromosphere is hiding. The mask is the wing's own darkness relative to its own disc, so it
// needs no hand-drawn region and cannot invent a feature that is not in the data.
//
//	ASTRO_SOLAR_CORE=<on-band master.fits> ASTRO_SOLAR_WING=<off-band master.fits> \
//	ASTRO_SOLAR_OUT=/tmp/out go test ./internal/solar -run BandComposite_Live -v
func TestBandComposite_Live(t *testing.T) {
	corePath, wingPath := os.Getenv("ASTRO_SOLAR_CORE"), os.Getenv("ASTRO_SOLAR_WING")
	if corePath == "" || wingPath == "" {
		t.Skip("set ASTRO_SOLAR_CORE and ASTRO_SOLAR_WING to two masters at different tunings")
	}
	outDir := os.Getenv("ASTRO_SOLAR_OUT")
	if outDir == "" {
		outDir = t.TempDir()
	}
	require.NoError(t, os.MkdirAll(outDir, 0o755))

	coreIm, err := fits.ReadImage(corePath)
	require.NoError(t, err)
	wingIm, err := fits.ReadImage(wingPath)
	require.NoError(t, err)
	core, wing := firstPlane(coreIm), firstPlane(wingIm)

	margin := envFloat(t, "ASTRO_SOLAR_MARGIN", defaultCropMargin)
	g := Pair{Sun: canonicalLimbOf(core.W, margin)}
	wl := canonicalLimbOf(wing.W, margin)
	t.Logf("core: r=%.1f at (%.1f,%.1f) %dx%d", g.Sun.R, g.Sun.CX, g.Sun.CY, core.W, core.H)
	t.Logf("wing: r=%.1f at (%.1f,%.1f) %dx%d", wl.R, wl.CX, wl.CY, wing.W, wing.H)

	aligned, cov, deg, err := alignToReference(core, g.Sun, Exposure{Master: wing, Limb: wl, Label: "wing"})
	require.NoError(t, err)
	t.Logf("registered the wing onto the core raster, rotation %.3f°, disc match %.4f", deg, discMatch(core, aligned, g.Sun))

	coreMed := discMedian(core, g.Sun)
	wingMed := discMedian(aligned, g.Sun)
	require.Greater(t, coreMed, 0.0)
	require.Greater(t, wingMed, 0.0)
	t.Logf("disc medians: core %.5g, wing %.5g (ratio %.2f)", coreMed, wingMed, wingMed/coreMed)

	// The wing has to be judged against its own LOCAL level, not against one disc median and not
	// against a radial profile either. A flat threshold on the median selects a wide annulus of
	// ordinary disc — measured, 46% of the frame — because limb darkening alone puts a quiet pixel
	// at 0.9 R well under it. A radial model fixes that and still leaves 22%, because the etalon's
	// sweet spot is not centred on the disc, so what remains is a large-scale gradient no profile
	// about the disc centre can describe. Dividing by a heavily blurred copy removes limb darkening
	// and the off-centre vignette together, and leaves ≈1 everywhere the photosphere is quiet.
	blurSigma := compositeBlurFrac * g.Sun.R
	local := blurFIR(aligned.Pix[0], aligned.W, aligned.H, gaussianKernel(blurSigma))
	t.Logf("wing local background: gaussian sigma %.0f px (%.3f R)", blurSigma, compositeBlurFrac)

	lo, hi := envFloat(t, "ASTRO_SOLAR_MASK_LO", 0.72), envFloat(t, "ASTRO_SOLAR_MASK_HI", 0.93)
	out := fits.NewImage(core.W, core.H, 1)
	var pulled int
	for y := 0; y < core.H; y++ {
		dy := float64(y) - g.Sun.CY
		for x := 0; x < core.W; x++ {
			i := y*core.W + x
			c := float64(core.Pix[0][i])
			out.Pix[0][i] = float32(c)
			dx := float64(x) - g.Sun.CX
			d := math.Hypot(dx, dy)
			// Strictly inside the disc: the limb's own edge is not a photospheric feature, and off
			// the limb the wing holds no chromosphere at all — the prominences are the core's.
			if d > compositeInnerR*g.Sun.R || (cov != nil && !cov[i]) {
				continue
			}
			bg := float64(local[i])
			if bg <= 1e-9 {
				continue
			}
			wf := float64(aligned.Pix[0][i]) / bg
			if wf >= hi {
				continue
			}
			// Transfer the wing's RELATIVE darkening onto the core multiplicatively, so the composite
			// cannot shift the chromosphere's overall level — it can only carve the spot back in.
			m := smoothstep(clampF((hi-wf)/(hi-lo), 0, 1))
			out.Pix[0][i] = float32(c * (1 - m*(1-wf)))
			pulled++
		}
	}
	t.Logf("composited: %d px (%.2f%%) took some of the wing", pulled, 100*float64(pulled)/float64(len(out.Pix[0])))

	fin, psf, notes := ResolveFinish(out, g.Sun, DefaultFinish())
	for _, n := range notes {
		t.Log("  ", n)
	}
	if v := os.Getenv("ASTRO_SOLAR_SIGMA"); v != "" {
		s, perr := strconv.ParseFloat(v, 64)
		require.NoError(t, perr)
		fin.DeconvAuto, fin.DeconvSigma, fin.Sharpen.DeconvSigma = false, s, s
	}
	t.Logf("psf: sigma %.2f px, fwhm %.1f\"", psf.SigmaPx, psf.FWHMArcsec)
	fin = naturalFinish(fin)
	fin.ProminenceBoost, fin.GlowStrength = 0.2, 0.5

	dst := filepath.Join(outDir, "band_composite.png")
	require.NoError(t, WritePNG(FinishPair(out, g, fin), dst))
	t.Log("wrote", dst)
}

// compositeInnerR is how far into the disc the wing is allowed to contribute. The last two percent
// is the limb's own darkening edge, where a small registration error between two independently
// stacked masters shows up as a rim rather than as a feature.
const (
	compositeInnerR = 0.98
	// compositeBlurFrac is the width of the wing's local-background blur, as a fraction of the disc
	// radius. It has to be comfortably wider than a sunspot group so the spot survives as a deviation
	// rather than being absorbed into its own background, and narrow enough to follow the vignette.
	compositeBlurFrac = 0.035
)

func envFloat(t *testing.T, key string, def float64) float64 {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	require.NoError(t, err)
	return f
}

// canonicalLimbOf reconstructs the geometry a stack's master ACTUALLY has, from the raster it was
// written on.
//
// Re-fitting the limb on a master looks like the obvious thing and is badly wrong. Registration puts
// the disc on a canonical raster by construction — centred at (side-1)/2 with a radius the canvas
// size encodes — while a fresh FitLimb on the stacked, scattered-light-skirted master reads the disc
// several percent too large: measured on a 2878 px canvas it returned R=1404 against the true 1332,
// and the centre 18 px off in Y. Everything downstream in the finish is radial about that circle —
// the instrument flat, the limb-darkening profile, the off-limb background, the glow — so a 5% error
// in R does not shift the picture, it prints concentric rings across the whole disc and a bright
// collar at the limb. That is what a re-finish that "mysteriously" looks worse than the run is.
//
// CanonicalSide rounds down to an even size, so inverting it leaves R known to about half a pixel;
// taking the middle of the range that maps back to this side is as exact as the raster allows.
func canonicalLimbOf(side int, margin float64) Limb {
	half := float64(side-1) / 2
	lo, hi := float64(side-1), float64(side+1)
	r := (lo + hi) / 4 / (1 + margin) // midpoint of the radii that round to this side
	return Limb{CX: half, CY: half, R: r}
}
