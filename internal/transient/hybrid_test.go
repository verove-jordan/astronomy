package transient

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// memFrame builds an in-memory mono frame: noisy background + a static star, plus a 45° streak of the
// given amplitude (0 = none). Mirrors the file-based writeSeqFrame used by the MaskSequence tests.
func memFrame(frame int, streakAmp float32) *fits.Image {
	im := fits.NewImage(seqW, seqH, 1)
	p := im.Pix[0]
	for i := range p {
		p[i] = seqBg + seqNoise(i, frame)
	}
	p[starY*seqW+starX] = starVal
	if streakAmp > 0 {
		addStreakAmp(p, seqW, seqH, streakAmp)
	}
	return im
}

// addStreakAmp paints a Gaussian-profile 45° streak (line y=x, extent [30,270]) of peak amplitude amp.
func addStreakAmp(p []float32, w, h int, amp float32) {
	addStreakSpan(p, w, h, amp, 30, 270)
}

// addStreakSpan paints a Gaussian-profile 45° streak segment on the line y=x, covering only
// along ∈ [a0,a1] where along = (x+y)/2 — a marching satellite's per-frame stretch of a shared track.
func addStreakSpan(p []float32, w, h int, amp float32, a0, a1 float64) {
	const sigma = 1.2
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if along := float64(x+y) / 2; along < a0 || along > a1 {
				continue
			}
			d := math.Abs(float64(x-y)) / math.Sqrt2
			if d > 3 {
				continue
			}
			g := amp * float32(math.Exp(-d*d/(2*sigma*sigma)))
			if v := seqBg + g; v > p[y*w+x] {
				p[y*w+x] = v
			}
		}
	}
}

func maxAlongDiagonal(im *fits.Image) float32 {
	var m float32
	for x := 30; x < 270; x++ {
		if v := im.Pix[0][x*seqW+x]; v > m {
			m = v
		}
	}
	return m
}

// TestMaskCrossFrameValidated_CleansOneFrameTrail: a streak in ONE frame is detected, accepted (it is
// unique to that frame), painted back to the clean median, and the static star survives everywhere.
func TestMaskCrossFrameValidated_CleansOneFrameTrail(t *testing.T) {
	frames := make([]*fits.Image, 8)
	for i := range frames {
		amp := float32(0)
		if i == 0 {
			amp = 0.6
		}
		frames[i] = memFrame(i, amp)
	}
	rep, err := MaskCrossFrameValidated(frames, 3.0, nil)
	require.NoError(t, err)
	s := rep.Summary()
	assert.GreaterOrEqual(t, s.Segments, 1, "the one-frame streak is accepted as a real trail")
	assert.Equal(t, 0, s.Rejected, "a unique trail is not rejected as fixed pattern")
	assert.Less(t, maxAlongDiagonal(frames[0]), float32(0.2), "streak painted down to ~background (was 0.6)")
	for i, f := range frames {
		assert.InDelta(t, starVal, f.Pix[0][starY*seqW+starX], 0.05, "static star preserved in frame %d", i)
	}
}

// TestMaskCrossFrameValidated_RejectsFixedPattern: a streak recurring at the SAME corridor in a large
// MINORITY of frames (a walking-noise stand-in) is REJECTED by the line pass — every window of its
// corridor is lit in ≥30% of the other frames (the windowed test agrees with the old whole-corridor
// one exactly when the recurrence covers the full corridor in the same frames). It stays a minority so
// it does NOT contaminate the cross-frame median (the geostationary pass would legitimately repair a
// median-present streak), isolating the fixed-pattern rejection the naive line mask lacked. NOTE: the
// recurring-corridor pass may still repair these pixels via the mean plane — physically a bright
// same-track minority IS indistinguishable from a reused satellite track — so this test pins the LINE
// pass's counters (rejection), not the pixel outcome.
func TestMaskCrossFrameValidated_RejectsFixedPattern(t *testing.T) {
	const n, hot = 10, 4 // 4/10 keeps the per-pixel median clean; 3 other hot frames = 33% > 30%
	frames := make([]*fits.Image, n)
	for i := range frames {
		amp := float32(0)
		if i < hot {
			amp = 0.6 // same corridor, several frames → a recurring positive-residual line, not a median streak
		}
		frames[i] = memFrame(i, amp)
	}
	rep, err := MaskCrossFrameValidated(frames, 3.0, nil)
	require.NoError(t, err)
	s := rep.Summary()
	// The line pass sees the candidate (a strong residual streak) but REJECTS it as fixed pattern — the
	// discriminator the naive line mask lacked. (Whether a given pixel then survives is up to the separate
	// blanket pass, which correctly clips minority-frame bright outliers; that is not what we assert here.)
	assert.Positive(t, s.Rejected, "the recurring streak is rejected as fixed pattern")
	assert.Equal(t, 0, s.Segments, "no fixed-pattern candidate is accepted as a trail")
	assert.Equal(t, 0, s.Geostationary, "the minority streak does not reach the cross-frame median")
}

// TestMaskCrossFrameValidated_MasksAllFourUniqueLines: a frame carrying four distinct streaks gets
// ALL of them validated and painted. CONTRACT CHANGE (2026-07-16, task #316): this fixture used to pin
// the opposite — a detector-saturated frame skipped its whole line pass as a hallucination guard. That
// guard predated per-candidate cross-frame validation and leaked real trails on geostationary-belt
// nights, where a frame legitimately carries several streaks (job #316: 4 frames skipped, trail
// stacked through). The windowed fixed-pattern test now judges each candidate individually, so the
// wholesale skip is gone and multi-trail frames are cleaned.
func TestMaskCrossFrameValidated_MasksAllFourUniqueLines(t *testing.T) {
	frames := make([]*fits.Image, 8)
	for i := range frames {
		frames[i] = memFrame(i, 0)
	}
	paintFourLines(frames[0].Pix[0], seqW, seqH) // frame 0 only
	rep, err := MaskCrossFrameValidated(frames, 3.0, nil)
	require.NoError(t, err)
	s := rep.Summary()
	assert.GreaterOrEqual(t, s.Segments, 3, "the unique streaks are accepted, not skipped")
	assert.Equal(t, 0, s.Rejected, "unique streaks are not fixed pattern")
	maxRow := float32(0)
	for x := 40; x < 260; x++ {
		if v := frames[0].Pix[0][75*seqW+x]; v > maxRow {
			maxRow = v
		}
	}
	assert.Less(t, maxRow, float32(0.2), "the horizontal streak is painted down (was 0.8)")
	assert.InDelta(t, starVal, frames[0].Pix[0][starY*seqW+starX], 0.05, "static star preserved")
}

// TestMaskCrossFrameValidated_AcceptsMarchingBeltTrail: a slow satellite on a reused sky track leaves
// a SHORT segment per frame, marching along one line — each stretch too short for the per-frame
// detector (elongation gate) and, before the windowed rework, any detected candidate was rejected
// because the shared track lights the full corridor in too many frames. The recurring-corridor pass
// finds the track on the coverage-aware MEAN residual (where the stretches sum coherently, exactly as
// they would in the stack) and each frame repairs the windows it lights.
func TestMaskCrossFrameValidated_AcceptsMarchingBeltTrail(t *testing.T) {
	const n = 8
	frames := make([]*fits.Image, n)
	for i := range frames {
		frames[i] = memFrame(i, 0)
		a0 := 20 + 34*float64(i)
		addStreakSpan(frames[i].Pix[0], seqW, seqH, 0.6, a0, a0+30)
	}
	rep, err := MaskCrossFrameValidated(frames, 3.0, nil)
	require.NoError(t, err)
	s := rep.Summary()
	assert.GreaterOrEqual(t, s.Recurring, 1, "the shared track is found on the mean residual")
	for i, f := range frames {
		mid := 20 + 34*float64(i) + 15 // centre of this frame's stretch, on y=x
		px := int(mid)
		assert.Less(t, f.Pix[0][px*seqW+px], float32(0.2), "frame %d's marching stretch painted down (was 0.6)", i)
		assert.InDelta(t, starVal, f.Pix[0][starY*seqW+starX], 0.05, "static star preserved in frame %d", i)
	}
}

// TestMaskCrossFrameValidated_ZeroBorderTrail: frames of a rotated night carry large zero-fill
// borders after registration onto the anchor canvas. A trail crossing the data region must still be
// accepted (window statistics exclude zero pixels, so the borders can't dilute its significance), and
// witnesses whose zero band covers a corridor window are counted as UNOBSERVED there, not dark.
func TestMaskCrossFrameValidated_ZeroBorderTrail(t *testing.T) {
	zeroBand := func(im *fits.Image) { // the left band x<100 reads zero-fill, like a rotated border
		for y := 0; y < seqH; y++ {
			for x := 0; x < 100; x++ {
				im.Pix[0][y*seqW+x] = 0
			}
		}
	}
	frames := make([]*fits.Image, 8)
	for i := range frames {
		frames[i] = memFrame(i, 0)
	}
	addStreakAmp(frames[0].Pix[0], seqW, seqH, 0.6) // full streak, then its border zeroed
	zeroBand(frames[0])
	zeroBand(frames[1]) // two witnesses share the rotated footprint
	zeroBand(frames[2])
	rep, err := MaskCrossFrameValidated(frames, 3.0, nil)
	require.NoError(t, err)
	s := rep.Summary()
	assert.GreaterOrEqual(t, s.Segments, 1, "the data-region trail is accepted despite the zero border")
	assert.Equal(t, 0, s.Rejected, "zero-band witnesses read unobserved, not dark-pattern")
	maxData := float32(0)
	for x := 110; x < 260; x++ {
		if v := frames[0].Pix[0][x*seqW+x]; v > maxData {
			maxData = v
		}
	}
	assert.Less(t, maxData, float32(0.2), "the streak's data-region stretch is painted down")
	for x := 0; x < 90; x++ { // untouched zero band in a clean witness stays zero-fill
		require.Equal(t, float32(0), frames[1].Pix[0][60*seqW+x], "witness zero band must stay zero at x=%d", x)
	}
}

// paintFourLines draws four bright bands at four distinct angles (horizontal, vertical, both diagonals),
// enough to saturate trail.DetectSegments' four-segment-per-plane ceiling.
func paintFourLines(p []float32, w, h int) {
	set := func(x, y int, v float32) {
		if x >= 0 && x < w && y >= 0 && y < h && v > p[y*w+x] {
			p[y*w+x] = v
		}
	}
	for t := 20; t < w-20; t++ {
		for d := -2; d <= 2; d++ {
			set(t, 75+d, 0.8)      // horizontal
			set(75+d, t, 0.8)      // vertical
			set(t, t+d, 0.8)       // diagonal y=x
			set(t, (w-1-t)+d, 0.8) // anti-diagonal
		}
	}
}
