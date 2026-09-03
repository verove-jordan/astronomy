package solar

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestDerotation_Live asks whether the rotation term is helping or hurting on a real crescent.
//
// EstimateRotation correlates a profile taken around an annulus at 0.70 R. On an eclipsed Sun that
// circle can lie entirely inside the occulter — on the 12 Aug master every point of it sits 453 px
// from the Moon's centre against a lunar radius of 456 — so the profile is the Moon's dark interior,
// the correlation matches noise, and whatever it returns is applied to the Sun as a rotation.
//
//	ASTRO_SOLAR_FRAMES=work/sun_<id> go test ./internal/solar -run Derotation -v
func TestDerotation_Live(t *testing.T) {
	dir := os.Getenv("ASTRO_SOLAR_FRAMES")
	if dir == "" {
		t.Skip("set ASTRO_SOLAR_FRAMES=<a run's work dir>")
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.fits"))
	if len(paths) < 40 {
		t.Skipf("only %d frames in %s", len(paths), dir)
	}
	sort.Strings(paths)
	// A contiguous run from the middle: the same frames for every configuration.
	const n = 160
	start := len(paths)/2 - n/2
	var frames []Frame
	for i := start; i < start+n; i++ {
		im, err := fits.ReadImage(paths[i])
		if err != nil {
			continue
		}
		mono := firstPlane(im)
		g, ok := FitPair(mono)
		if !ok {
			continue
		}
		frames = append(frames, Frame{Path: paths[i], Index: i, TimeMs: int64(i) * 33,
			Limb: g.Sun, Moon: g.Moon, Score: FrameSharpnessPair(mono, g)})
	}
	t.Logf("stacking %d contiguous frames, three ways", len(frames))

	ctx := context.Background()
	run := func(label string, o StackOptions) {
		res, err := Stack(ctx, frames, o)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		p := MeasurePSF(res.Master, res.Limb)
		var edge PSF
		if res.Moon.R > 0 {
			edge = MeasureEdge(res.Master, res.Moon, edgeRising, 1)
		}
		t.Logf("  %-28s solar limb sigma %.2f | occulter edge sigma %.2f", label, p.SigmaPx, edge.SigmaPx)
	}
	base := StackOptions{Drizzle: 1.5, APAlign: true}
	run("as shipped", base)

	noRot := base
	noRot.NoDerotate = true
	run("without derotation", noRot)

	noRefine := base
	noRefine.NoRefine = true
	run("without the refinement", noRefine)

	noAP := base
	noAP.APAlign = false
	run("without the AP field", noAP)

	// The AP field is measured over the whole disc, and on a crescent most of the disc is occulter —
	// so most of its nodes correlate the Moon against the Moon. A field measured on the thing in
	// front of the Sun, then applied to the Sun, is the same failure as the refiner's in a term that
	// warps every pixel independently.
	apOnly := base
	apOnly.NoRefine, apOnly.NoDerotate = true, true
	run("AP field alone", apOnly)
}
