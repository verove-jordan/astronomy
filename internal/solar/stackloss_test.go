package solar

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStackLoss_Synthetic measures what stacking costs when the answer is known.
//
// On real frames a stack scores well below the sharpest frame that went into it, and that single
// number has two very different explanations which call for opposite responses:
//
//   - The stack really is blurrier, because registration is imperfect. Fix the registration.
//   - The stack is fine and the METRIC is biased, because a single frame's band-pass energy is
//     inflated by its own photon noise. FrameSharpness subtracts a noise floor measured on the sky,
//     but the disc is far brighter than the sky and therefore noisier, so the subtraction
//     under-corrects — and it under-corrects a single frame much more than a 300-frame stack, whose
//     noise has already averaged away. That alone would make every honest stack look like a loss.
//
// Only ground truth separates them, so this builds frames from a known disc: identical structure,
// independent noise, and a known sub-pixel jitter. Perfect registration then bounds what stacking
// can cost, and the gap to the jittered case is what better registration is worth.
func TestStackLoss_Synthetic(t *testing.T) {
	const n = 24
	spec := defaultSun()
	spec.w, spec.h = 900, 900
	spec.cx, spec.cy, spec.r = 450, 450, 330
	spec.features = 18
	spec.ringAmp = 0
	spec.psfSigma = 1.6

	clean := spec
	clean.noise = 0
	truth := drawSun(clean)
	tl, ok := FitLimb(truth)
	require.True(t, ok)
	dTruth := FrameSharpness(truth, tl)

	dir := t.TempDir()
	// build writes n frames jittered by the given per-axis sigma and returns their mean single-frame
	// score alongside the paths.
	build := func(tag string, jitter float64) ([]Frame, float64) {
		var frames []Frame
		var sum float64
		for i := 0; i < n; i++ {
			s := spec
			// The SCENE is identical in every frame — same filaments, same plage — and only the noise
			// and the sub-pixel placement change. That is what a real video of the Sun is over a few
			// seconds, and it is the only configuration in which "the stack should equal the truth"
			// is a meaningful claim.
			s.noiseSeed = uint64(1000 + i)
			// A deterministic jitter: the point is a known, non-zero sub-pixel offset per frame, not a
			// random one, so the comparison between cases is exact.
			s.cx += jitter * math.Cos(float64(i)*2.399)
			s.cy += jitter * math.Sin(float64(i)*2.399)
			im := drawSun(s)
			p := filepath.Join(dir, fmt.Sprintf("%s_%03d.fits", tag, i))
			require.NoError(t, im.WriteFITS(p))
			l, ok := FitLimb(im)
			require.True(t, ok)
			d := FrameSharpness(im, l)
			sum += d
			frames = append(frames, Frame{Path: p, Index: i, Limb: l, Score: d})
		}
		return frames, sum / float64(n)
	}

	ctx := context.Background()
	truthPSF := MeasurePSF(truth, tl)
	require.True(t, truthPSF.OK)
	t.Logf("noise-free truth: detail %.5f, PSF sigma %.2f px (drawn at %.2f)",
		dTruth, truthPSF.SigmaPx, spec.psfSigma)
	for _, c := range []struct {
		tag    string
		jitter float64
	}{
		{"aligned", 0},    // registration is exact: the floor on what stacking costs
		{"jitter05", 0.5}, // about what the limb fit actually delivers on real frames
		{"jitter10", 1.0},
	} {
		frames, meanIn := build(c.tag, c.jitter)
		res, err := Stack(ctx, frames, StackOptions{})
		require.NoError(t, err)
		d := FrameSharpness(res.Master, res.Limb)
		// The PSF alongside the band-pass, because the two disagree about stacking in a way that is
		// entirely explained by noise: FrameSharpness subtracts a floor measured on the sky, which
		// under-corrects a single grainy frame far more than a stack, so an honest stack scores like a
		// loss whether or not it is one. The limb settles it — noise does not make an edge narrower.
		psf := MeasurePSF(res.Master, res.Limb)
		require.True(t, psf.OK)
		t.Logf("jitter %.1f px: mean single %.5f (%3.0f%% of truth) | stack %.5f (%3.0f%% of truth, %3.0f%% of single) | stack PSF %.2f px",
			c.jitter, meanIn, 100*meanIn/dTruth, d, 100*d/dTruth, 100*d/meanIn, psf.SigmaPx)
		// Perfect registration must cost essentially nothing, and this is the control that says so:
		// if the warp and the combiner were themselves softening, every conclusion drawn from the
		// jittered cases would be measuring the wrong thing.
		if c.jitter == 0 {
			assert.Less(t, psf.SigmaPx, truthPSF.SigmaPx+0.15,
				"with registration exact, stacking must not blur — this bounds what the warp and the combiner cost")
		}
	}
}
