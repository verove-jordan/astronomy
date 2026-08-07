package solar

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestStack_APAlign_Live stacks the same real frames twice — once with a rigid global transform,
// once with the alignment-point field — and compares the detail recovered.
//
// This is the measurement behind the claim that multi-point alignment matters. A global similarity
// assumes the atmosphere displaced the whole disc as one rigid object; it did not, and stacking on
// that assumption averages every feature against slightly shifted copies of itself. Opt-in:
//
//	ASTRO_SOLAR_FRAMES=<dir of ingested *.fits> go test ./internal/solar -run APAlign_Live -v
func TestStack_APAlign_Live(t *testing.T) {
	dir := os.Getenv("ASTRO_SOLAR_FRAMES")
	if dir == "" {
		t.Skip("set ASTRO_SOLAR_FRAMES=<dir of ingested frames>")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.fits"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "no ingested frames in %s", dir)
	sort.Strings(paths)
	if max := 60; len(paths) > max {
		paths = paths[:max] // a controlled comparison, not a full run
	}

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
	require.GreaterOrEqual(t, len(frames), 20, "need a real set to compare on")
	t.Logf("%d frames, disc r=%.1f px", len(frames), frames[0].Limb.R)

	ctx := context.Background()
	run := func(ap bool) (*StackResult, time.Duration) {
		start := time.Now()
		res, err := Stack(ctx, frames, StackOptions{APAlign: ap})
		require.NoError(t, err)
		return res, time.Since(start)
	}

	rigid, tRigid := run(false)
	multi, tMulti := run(true)

	// The comparison uses the NOISE-CORRECTED metric. A plain gradient energy would rank a noisy
	// single frame above a clean stack and make this whole comparison meaningless.
	detail := func(r *StackResult) float64 { return FrameSharpness(r.Master, r.Limb) }
	dRigid, dMulti := detail(rigid), detail(multi)
	// The best single frame is the bar that matters: a stack softer than its own sharpest input has
	// averaged away more than it gained.
	best := 0.0
	for _, f := range frames {
		if f.Score > best {
			best = f.Score
		}
	}
	t.Logf("detail — best single frame %.5f | rigid stack %.5f (%.0f%% of best, %s) | AP stack %.5f (%.0f%% of best, %s)",
		best, dRigid, 100*dRigid/best, tRigid.Round(time.Second), dMulti, 100*dMulti/best, tMulti.Round(time.Second))

	assert.Greater(t, dMulti, dRigid,
		"alignment-point stacking must recover more detail than a rigid transform")

	if out := os.Getenv("ASTRO_SOLAR_OUT"); out != "" {
		o := DefaultFinish()
		require.NoError(t, WritePNG(Finish(rigid.Master, rigid.Limb, o), out+"_rigid.png"))
		require.NoError(t, WritePNG(Finish(multi.Master, multi.Limb, o), out+"_ap.png"))
		t.Logf("wrote %s_rigid.png and %s_ap.png", out, out)
	}
}
