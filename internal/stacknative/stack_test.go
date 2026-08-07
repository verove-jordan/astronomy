package stacknative

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/stackalg"
)

// rng is a deterministic pseudo-random source — a stacking test that flakes is worthless.
type rng struct{ s uint64 }

func (r *rng) next() float64 {
	r.s = r.s*6364136223846793005 + 1442695040888963407
	return float64(r.s>>11) / float64(1<<53)
}

// writeSeq writes n synthetic mono frames of w×h into dir and returns their paths. fill(frame,x,y)
// supplies each pixel, so a test can plant trails, ramps or level differences.
func writeSeq(t *testing.T, dir string, n, w, h int, fill func(frame, x, y int) float32) []string {
	t.Helper()
	var paths []string
	for i := 0; i < n; i++ {
		im := fits.NewImage(w, h, 1)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				im.Pix[0][y*w+x] = fill(i, x, y)
			}
		}
		p := filepath.Join(dir, "f_"+string(rune('a'+i))+".fits")
		require.NoError(t, im.WriteFITS(p))
		paths = append(paths, p)
	}
	return paths
}

func readMaster(t *testing.T, path string) *fits.Image {
	t.Helper()
	im, err := fits.ReadImage(path)
	require.NoError(t, err)
	return im
}

// meanOf is the average of a plane, the summary statistic most of these tests compare.
func meanOf(im *fits.Image) float64 {
	var sum float64
	for _, x := range im.Pix[0] {
		sum += float64(x)
	}
	return sum / float64(len(im.Pix[0]))
}

func TestStack_AveragesACleanSequence(t *testing.T) {
	dir := t.TempDir()
	const n, w, h = 12, 40, 90 // h > bandRows so the band streaming is genuinely exercised
	r := &rng{s: 7}
	paths := writeSeq(t, dir, n, w, h, func(frame, x, y int) float32 {
		return float32(0.5 + (r.next()-0.5)*0.01)
	})

	out := filepath.Join(dir, "master.fits")
	o := stackalg.DefaultLights()
	o.OutputNorm = false // compare in the input's own units
	res, err := Stack(context.Background(), Request{Frames: paths, Out: out, Options: o})
	require.NoError(t, err)
	assert.Equal(t, n, res.Frames)
	assert.Equal(t, w, res.Width)
	assert.Equal(t, h, res.Height)

	im := readMaster(t, out)
	require.Equal(t, w, im.W)
	assert.InDelta(t, 0.5, meanOf(im), 0.002, "the master must sit at the sequence level")
}

// TestStack_RemovesATrail is the whole point: a bright streak present in ONE frame must not survive
// into the master, at any of the rejection algorithms the panel offers.
func TestStack_RemovesATrail(t *testing.T) {
	for _, algo := range []stackalg.Reject{
		stackalg.RejectWinsorized, stackalg.RejectSigma, stackalg.RejectGESD,
		stackalg.RejectMAD, stackalg.RejectLinearFit, stackalg.RejectRCR,
		stackalg.RejectAdaptiveWeighted,
	} {
		t.Run(string(algo), func(t *testing.T) {
			dir := t.TempDir()
			const n, w, h = 16, 40, 80
			r := &rng{s: 11}
			paths := writeSeq(t, dir, n, w, h, func(frame, x, y int) float32 {
				v := float32(0.5 + (r.next()-0.5)*0.01)
				if frame == 5 && y == 20 { // a satellite crossing one sub
					v += 0.4
				}
				return v
			})
			out := filepath.Join(dir, "m.fits")
			o := stackalg.DefaultLights()
			o.Reject, o.OutputNorm = algo, false
			_, err := Stack(context.Background(), Request{Frames: paths, Out: out, Options: o})
			require.NoError(t, err)

			im := readMaster(t, out)
			// The trail row must read like every other row. Averaged in, it would sit 0.4/16 = 0.025
			// above the sky.
			var trailRow, cleanRow float64
			for x := 0; x < w; x++ {
				trailRow += float64(im.Pix[0][20*w+x])
				cleanRow += float64(im.Pix[0][21*w+x])
			}
			trailRow, cleanRow = trailRow/float64(w), cleanRow/float64(w)
			assert.InDelta(t, cleanRow, trailRow, 0.004,
				"the trail leaked through: row 20 is %.4f against %.4f on a clean row", trailRow, cleanRow)
		})
	}
}

// TestStack_NormalizationLevelsTheFrames: frames captured at different sky levels must be brought
// onto one footing before combination, or the master lands between them instead of on the reference.
func TestStack_NormalizationLevelsTheFrames(t *testing.T) {
	dir := t.TempDir()
	const n, w, h = 8, 32, 70
	r := &rng{s: 23}
	// Frame i sits 0.05·i above the reference — a session where the sky brightened steadily.
	paths := writeSeq(t, dir, n, w, h, func(frame, x, y int) float32 {
		return float32(0.4 + 0.05*float64(frame) + (r.next()-0.5)*0.004)
	})

	run := func(norm stackalg.Norm) float64 {
		out := filepath.Join(dir, "m_"+string(norm)+".fits")
		o := stackalg.DefaultLights()
		o.Norm, o.OutputNorm, o.Reject = norm, false, stackalg.RejectNone
		_, err := Stack(context.Background(), Request{Frames: paths, Out: out, Options: o})
		require.NoError(t, err)
		return meanOf(readMaster(t, out))
	}

	// Un-normalized, the master lands on the mean of the drifting levels (0.4 + 0.05·3.5 = 0.575).
	assert.InDelta(t, 0.575, run(stackalg.NormNone), 0.01)
	// Additive normalization pulls every frame onto the reference's level (0.4).
	assert.InDelta(t, 0.4, run(stackalg.NormAdd), 0.01)
	assert.InDelta(t, 0.4, run(stackalg.NormAddScale), 0.02)
}

func TestStack_OutputNormRescalesToUnit(t *testing.T) {
	dir := t.TempDir()
	paths := writeSeq(t, dir, 6, 20, 70, func(frame, x, y int) float32 {
		return float32(0.2 + 0.001*float64(x)) // a gentle ramp, nowhere near 0 or 1
	})
	out := filepath.Join(dir, "m.fits")
	o := stackalg.DefaultLights() // OutputNorm is on by default
	_, err := Stack(context.Background(), Request{Frames: paths, Out: out, Options: o})
	require.NoError(t, err)

	im := readMaster(t, out)
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, x := range im.Pix[0] {
		lo, hi = math.Min(lo, float64(x)), math.Max(hi, float64(x))
	}
	assert.InDelta(t, 0, lo, 1e-6)
	assert.InDelta(t, 1, hi, 1e-6)
}

func TestStack_CarriesProvenanceCards(t *testing.T) {
	dir := t.TempDir()
	const n = 5
	paths := writeSeq(t, dir, n, 16, 70, func(frame, x, y int) float32 { return 0.3 })
	out := filepath.Join(dir, "m.fits")
	_, err := Stack(context.Background(), Request{Frames: paths, Out: out, Options: stackalg.DefaultLights()})
	require.NoError(t, err)

	// STACKCNT is what the combined-mono integration weights by and what the run reports as depth.
	f, err := fits.Open(out)
	require.NoError(t, err)
	got, ok := f.Header.Int("STACKCNT")
	require.True(t, ok, "the master must record how many frames went into it")
	assert.Equal(t, int64(n), got)
}

func TestStack_RefusesAMismatchedFrame(t *testing.T) {
	dir := t.TempDir()
	paths := writeSeq(t, dir, 3, 20, 70, func(frame, x, y int) float32 { return 0.3 })
	odd := fits.NewImage(21, 70, 1) // one pixel wider — not co-registered
	odd.Pix[0][0] = 0.3
	oddPath := filepath.Join(dir, "odd.fits")
	require.NoError(t, odd.WriteFITS(oddPath))

	_, err := Stack(context.Background(), Request{
		Frames: append(paths, oddPath), Out: filepath.Join(dir, "m.fits"), Options: stackalg.DefaultLights(),
	})
	require.Error(t, err, "a geometry mismatch must fail loudly, not produce a garbage master")
	assert.Contains(t, err.Error(), "not co-registered")
}

func TestStack_NeedsTwoFrames(t *testing.T) {
	dir := t.TempDir()
	paths := writeSeq(t, dir, 1, 16, 70, func(frame, x, y int) float32 { return 0.3 })
	_, err := Stack(context.Background(), Request{Frames: paths, Out: filepath.Join(dir, "m.fits")})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "at least 2 frames")
}

func TestStack_HonoursCancellation(t *testing.T) {
	dir := t.TempDir()
	paths := writeSeq(t, dir, 4, 32, 400, func(frame, x, y int) float32 { return 0.3 })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Stack(ctx, Request{Frames: paths, Out: filepath.Join(dir, "m.fits"), Options: stackalg.DefaultLights()})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

// TestStack_ReportsTheRejectedFraction: the diagnostic must be believable — a clean sequence rejects
// almost nothing, and a contaminated one rejects more.
func TestStack_ReportsTheRejectedFraction(t *testing.T) {
	dir := t.TempDir()
	const n, w, h = 16, 32, 70
	r := &rng{s: 5}
	cleanPaths := writeSeq(t, filepath.Join(t.TempDir()), n, w, h, func(frame, x, y int) float32 {
		return float32(0.5 + (r.next()-0.5)*0.01)
	})
	r2 := &rng{s: 5}
	dirtyPaths := writeSeq(t, dir, n, w, h, func(frame, x, y int) float32 {
		v := float32(0.5 + (r2.next()-0.5)*0.01)
		if frame%4 == 0 && y%7 == 0 {
			v += 0.5
		}
		return v
	})
	run := func(paths []string, out string) float64 {
		res, err := Stack(context.Background(), Request{
			Frames: paths, Out: out, Options: stackalg.DefaultLights(),
		})
		require.NoError(t, err)
		return res.Rejected
	}
	cleanFrac := run(cleanPaths, filepath.Join(dir, "clean.fits"))
	dirtyFrac := run(dirtyPaths, filepath.Join(dir, "dirty.fits"))
	assert.Less(t, cleanFrac, 0.10, "a clean sequence should barely reject anything")
	assert.Greater(t, dirtyFrac, cleanFrac, "the contaminated one must reject more")
}
