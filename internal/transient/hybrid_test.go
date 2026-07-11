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
	const sigma = 1.2
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if along := float64(x+y) / 2; along < 30 || along > 270 {
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
	rep, err := MaskCrossFrameValidated(frames, 3.0)
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
// MINORITY of frames (a walking-noise stand-in) is REJECTED by the line pass — its corridor is elevated
// in ≥30% of the other frames. It stays a minority so it does NOT contaminate the cross-frame median
// (the geostationary pass would legitimately repair a median-present streak), isolating the fixed-pattern
// rejection the naive line mask lacked. The streak is left in place, not painted out.
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
	rep, err := MaskCrossFrameValidated(frames, 3.0)
	require.NoError(t, err)
	s := rep.Summary()
	// The line pass sees the candidate (a strong residual streak) but REJECTS it as fixed pattern — the
	// discriminator the naive line mask lacked. (Whether a given pixel then survives is up to the separate
	// blanket pass, which correctly clips minority-frame bright outliers; that is not what we assert here.)
	assert.Positive(t, s.Rejected, "the recurring streak is rejected as fixed pattern")
	assert.Equal(t, 0, s.Segments, "no fixed-pattern candidate is accepted as a trail")
	assert.Equal(t, 0, s.Geostationary, "the minority streak does not reach the cross-frame median")
}

// TestMaskCrossFrameValidated_SkipsSaturatedFrame: a frame that saturates the line detector (4 confirmed
// streaks) is treated as noise-dominated — its line pass is skipped (only blanket + validation run).
func TestMaskCrossFrameValidated_SkipsSaturatedFrame(t *testing.T) {
	frames := make([]*fits.Image, 8)
	for i := range frames {
		frames[i] = memFrame(i, 0)
	}
	paintFourLines(frames[0].Pix[0], seqW, seqH) // frame 0 only
	rep, err := MaskCrossFrameValidated(frames, 3.0)
	require.NoError(t, err)
	assert.Positive(t, rep.Summary().SkippedFrames, "the detector-saturated frame skips its line pass")
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
