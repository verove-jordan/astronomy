package solar

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestRegistrationNoise_Live measures how precisely each term of the registration is known.
//
// A stack blurs by exactly the scatter of its own alignment, so every term has a pixel budget. At a
// 900 px radius the budget is brutal: 0.1° of rotation error is 1.5 px of smear at the limb, and a
// half-pixel of random centring error costs about 40% of the contrast at the 3 px scale where solar
// detail lives. This test measures each term's scatter directly rather than trusting that a fit
// which passed on a synthetic disc also holds on a compressed 120 fps phone video.
//
// The method for the limb terms is to exploit the capture: consecutive frames of a video are
// separated by ~8 ms, over which the true disc position moves smoothly and slowly. So a frame's
// deviation from the LOCAL TREND of its neighbours is measurement noise, not motion. Rotation is
// measured against a fixed reference, where the true value drifts by well under a hundredth of a
// degree per frame — so its frame-to-frame scatter is likewise the estimator's own noise.
//
//	ASTRO_SOLAR_FRAMES=<dir of ingested *.fits> go test ./internal/solar -run RegistrationNoise -v
func TestRegistrationNoise_Live(t *testing.T) {
	dir := os.Getenv("ASTRO_SOLAR_FRAMES")
	if dir == "" {
		t.Skip("set ASTRO_SOLAR_FRAMES=<dir of ingested frames>")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.fits"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	sort.Strings(paths) // chronological: the frames are named by source index

	type entry struct {
		limb  Limb
		score float64
		im    *fits.Image
	}
	all := make([]entry, 0, len(paths))
	for _, p := range paths {
		im, err := fits.ReadImage(p)
		require.NoError(t, err)
		mono := firstPlane(im)
		l, ok := FitLimb(mono)
		if !ok {
			continue
		}
		e := entry{limb: l, score: FrameSharpness(mono, l)}
		if len(all) < 40 { // only the rotation probe needs pixels; holding 300 frames would not fit
			e.im = mono
		}
		all = append(all, e)
	}
	require.GreaterOrEqual(t, len(all), 30)

	cx := make([]float64, len(all))
	cy := make([]float64, len(all))
	rr := make([]float64, len(all))
	for i, e := range all {
		cx[i], cy[i], rr[i] = e.limb.CX, e.limb.CY, e.limb.R
	}
	t.Logf("limb-fit jitter over %d frames (deviation from the local 5-frame trend):", len(all))
	t.Logf("  centre x %.3f px | centre y %.3f px | radius %.3f px",
		detrendRMS(cx), detrendRMS(cy), detrendRMS(rr))
	t.Logf("  radius spread: min %.2f max %.2f median %.2f px",
		minOf(rr), maxOf(rr), median(append([]float64(nil), rr...)))

	// Rotation, measured against the sharpest of the frames we kept pixels for. Its true value drifts
	// smoothly and by a negligible amount across this many frames, so the scatter IS the noise.
	ref := 0
	for i := 0; i < 40 && i < len(all); i++ {
		if all[i].im != nil && all[i].score > all[ref].score {
			ref = i
		}
	}
	var degs []float64
	for i := 0; i < 40 && i < len(all); i++ {
		if i == ref || all[i].im == nil {
			continue
		}
		if d, ok := EstimateRotation(all[ref].im, all[i].im, all[ref].limb, all[i].limb); ok {
			degs = append(degs, d)
		}
	}
	require.NotEmpty(t, degs, "rotation never resolved")
	m, sd := meanSD(degs)
	r := all[0].limb.R
	t.Logf("rotation over %d frames: mean %+.3f° sd %.3f° (min %+.3f max %+.3f)",
		len(degs), m, sd, minOf(degs), maxOf(degs))
	t.Logf("  => smear at 0.7R (%.0f px): %.2f px | at the limb: %.2f px",
		0.7*r, 0.7*r*sd*math.Pi/180, r*sd*math.Pi/180)

	// And the decisive experiment: does applying that estimate help or hurt?
	frames := make([]Frame, 0, len(all))
	for i, p := range paths {
		if i >= len(all) {
			break
		}
		frames = append(frames, Frame{Path: p, Index: i, Limb: all[i].limb, Score: all[i].score})
	}
	sort.Slice(frames, func(a, b int) bool { return frames[a].Score > frames[b].Score })
	sub := frames[:15]
	ctx := context.Background()
	for _, c := range []struct {
		name string
		opts StackOptions
	}{
		{"rigid, derotate", StackOptions{}},
		{"rigid, no derotate", StackOptions{NoDerotate: true}},
		{"ap, derotate", StackOptions{APAlign: true}},
		{"ap, no derotate", StackOptions{APAlign: true, NoDerotate: true}},
	} {
		res, err := Stack(ctx, sub, c.opts)
		require.NoError(t, err)
		t.Logf("  N=15 %-20s detail %.5f", c.name, FrameSharpness(res.Master, res.Limb))
	}
}

// detrendRMS is the RMS deviation of a series from its own local 5-point linear trend.
func detrendRMS(v []float64) float64 {
	if len(v) < 7 {
		return 0
	}
	var sum float64
	var n int
	for i := 2; i < len(v)-2; i++ {
		// A 5-point quadratic-preserving smoother: the mid-point of a straight line through the
		// neighbours, which removes real motion (smooth) and leaves measurement noise (not).
		pred := (-v[i-2] + 4*v[i-1] + 4*v[i+1] - v[i+2]) / 6
		d := v[i] - pred
		sum += d * d
		n++
	}
	return math.Sqrt(sum / float64(n) / (1 + 34.0/36.0)) // undo the smoother's own noise gain
}

func meanSD(v []float64) (float64, float64) {
	var s float64
	for _, x := range v {
		s += x
	}
	m := s / float64(len(v))
	var q float64
	for _, x := range v {
		q += (x - m) * (x - m)
	}
	return m, math.Sqrt(q / float64(len(v)))
}

func minOf(v []float64) float64 {
	m := v[0]
	for _, x := range v {
		if x < m {
			m = x
		}
	}
	return m
}

func maxOf(v []float64) float64 {
	m := v[0]
	for _, x := range v {
		if x > m {
			m = x
		}
	}
	return m
}
