package solar

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestAPSweep_Live sweeps the alignment-point field's measurement resolution and density.
//
// The field is measured on a REDUCED copy of the canonical raster, because measuring at full
// resolution is an order of magnitude more expensive. That is a sound trade only while the residual
// being measured is comfortably larger than the reduced grid can resolve. It is not obviously true
// here: after a limb fit the residual is already sub-pixel, and a correlator working on a raster
// reduced 8× cannot see a shift of half a pixel at full scale no matter how good its interpolator
// is. This sweep answers whether the cheap measurement is throwing away the correction it exists to
// make.
//
//	ASTRO_SOLAR_FRAMES=<dir of ingested *.fits> go test ./internal/solar -run APSweep -v
func TestAPSweep_Live(t *testing.T) {
	dir := os.Getenv("ASTRO_SOLAR_FRAMES")
	if dir == "" {
		t.Skip("set ASTRO_SOLAR_FRAMES=<dir of ingested frames>")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.fits"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)
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
	sort.Slice(frames, func(a, b int) bool { return frames[a].Score > frames[b].Score })
	best := frames[0].Score

	n := 15
	if v := os.Getenv("ASTRO_SOLAR_N"); v != "" {
		if k, err := time.ParseDuration(v + "ns"); err == nil && int(k) > 0 {
			n = int(k)
		}
	}
	if n > len(frames) {
		n = len(frames)
	}
	sub := frames[:n]
	t.Logf("N=%d of %d frames | best single %.5f | r=%.0f px", n, len(frames), best, frames[0].Limb.R)

	ctx := context.Background()
	cases := []struct {
		label string
		opts  StackOptions
	}{
		{"rigid only", StackOptions{}},
		{"ap reduce 8", StackOptions{APAlign: true, APScale: 8}},
		{"ap reduce 4", StackOptions{APAlign: true, APScale: 4}},
		{"ap reduce 2", StackOptions{APAlign: true, APScale: 2}},
		{"ap reduce 1", StackOptions{APAlign: true, APScale: 1}},
		{"ap reduce 2, dense", StackOptions{APAlign: true, APScale: 2, APPoints: 26 * 26}},
		{"ap reduce 1, dense", StackOptions{APAlign: true, APScale: 1, APPoints: 26 * 26}},
	}
	for _, c := range cases {
		start := time.Now()
		res, err := Stack(ctx, sub, c.opts)
		require.NoError(t, err)
		d := FrameSharpness(res.Master, res.Limb)
		t.Logf("  %-20s detail %.5f  (%3.0f%% of best single)  %s",
			c.label, d, 100*d/best, time.Since(start).Round(time.Second))
	}
}
