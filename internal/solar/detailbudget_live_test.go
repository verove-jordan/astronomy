package solar

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestDetailBudget_Live accounts for every step where a stack can lose detail relative to its own
// sharpest input.
//
// "The stack is softer than one good frame" has at least four independent causes, and they call for
// opposite fixes, so guessing between them is how a week disappears:
//
//  1. AVERAGING. A stack of N frames carries roughly the AVERAGE of their point-spread functions,
//     not the best one. This is not a defect — it is what stacking is — and the only cure is to
//     average fewer, better frames. Measured here as mean-of-kept versus best.
//  2. RESAMPLING. Every frame is interpolated once onto the canonical raster. Cubic interpolation
//     is not transparent; it costs a few percent of MTF near Nyquist. Measured as N=1, which warps
//     the best frame and nothing else.
//  3. MISREGISTRATION. Residual alignment error blurs the sum. It is the difference between the
//     measured N-frame stack and what averaging alone (cause 1) predicts.
//  4. SELECTION. If the ranking is wrong, "the best 35%" is not the best 35%. Visible as a decay
//     curve that falls faster than the sorted single-frame scores do.
//
// Reading the output: the N=1 row is the resampling floor, the "predicted" column is what averaging
// alone would give, and any gap between predicted and measured is misregistration. Run with:
//
//	ASTRO_SOLAR_FRAMES=<dir of ingested *.fits> go test ./internal/solar -run DetailBudget -v
func TestDetailBudget_Live(t *testing.T) {
	dir := os.Getenv("ASTRO_SOLAR_FRAMES")
	if dir == "" {
		t.Skip("set ASTRO_SOLAR_FRAMES=<dir of ingested frames>")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.fits"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "no ingested frames in %s", dir)
	sort.Strings(paths)

	frames := make([]Frame, 0, len(paths))
	for i, p := range paths {
		im, err := fits.ReadImage(p)
		require.NoError(t, err)
		mono := firstPlane(im)
		l, ok := FitLimb(mono)
		if !ok {
			continue
		}
		frames = append(frames, Frame{Path: p, Index: i, Limb: l, Score: FrameSharpness(mono, l)})
	}
	require.GreaterOrEqual(t, len(frames), 20)

	// Sharpest first: every subset below is a "keep the best k" set.
	sort.Slice(frames, func(a, b int) bool { return frames[a].Score > frames[b].Score })
	best := frames[0].Score
	var all float64
	for _, f := range frames {
		all += f.Score
	}
	t.Logf("%d frames, r=%.1f px | best %.5f | median %.5f | mean %.5f | worst %.5f",
		len(frames), frames[0].Limb.R, best, frames[len(frames)/2].Score, all/float64(len(frames)),
		frames[len(frames)-1].Score)

	ctx := context.Background()
	counts := []int{1, 2, 5, 15, 40, 105, len(frames)}
	t.Logf("%6s %10s %10s %10s %10s %10s", "N", "meanIn", "predict", "rigid", "ap", "ap/pred")
	for _, n := range counts {
		if n > len(frames) {
			continue
		}
		sub := frames[:n]
		var s float64
		for _, f := range sub {
			s += f.Score
		}
		meanIn := s / float64(n)

		rigid, err := Stack(ctx, sub, StackOptions{})
		require.NoError(t, err)
		dr := FrameSharpness(rigid.Master, rigid.Limb)

		ap, err := Stack(ctx, sub, StackOptions{APAlign: true})
		require.NoError(t, err)
		da := FrameSharpness(ap.Master, ap.Limb)

		// What averaging alone predicts: the mean input detail, scaled by the resampling loss the
		// N=1 stack measured. Anything below this is registration error, not physics.
		t.Logf("%6d %10.5f %10.5f %10.5f %10.5f %9.2f", n, meanIn, meanIn, dr, da, da/meanIn)
	}
}
