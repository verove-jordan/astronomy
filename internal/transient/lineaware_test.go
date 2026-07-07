package transient

import (
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

const (
	seqW, seqH = 300, 300
	seqBg      = float32(0.05)
	starX      = 150
	starY      = 60
	starVal    = float32(0.9)
)

// seqNoise is small deterministic per-(pixel,frame) noise so the cross-frame MADσ is non-zero, as on
// real data (a noiseless background gives MAD 0, which the detector correctly refuses to act on).
func seqNoise(i, frame int) float32 {
	h := uint32(i*2654435761 + frame*40503 + 12345)
	h ^= h >> 13
	return (float32(h&0x3ff)/1023.0 - 0.5) * 0.01 // ±0.005
}

// writeSeqFrame writes an 80×80 mono float32 frame: noisy background + a static star, plus an optional
// bright diagonal trail (for the single frame that carries it).
func writeSeqFrame(t *testing.T, dir, name string, frame int, withTrail bool) string {
	t.Helper()
	im := fits.NewImage(seqW, seqH, 1)
	p := im.Pix[0]
	for i := range p {
		p[i] = seqBg + seqNoise(i, frame)
	}
	p[starY*seqW+starX] = starVal // a compact star present in every frame
	if withTrail {
		addDiagonalStreak(p, seqW, seqH)
	}
	path := filepath.Join(dir, name)
	require.NoError(t, im.WriteFITS(path))
	return path
}

// addDiagonalStreak paints a bright Gaussian-profile 45° streak (line y=x, extent [30,270]) as a
// SOLID band rasterized by perpendicular distance — so the band is 4-connected (a 45° centreline of
// single pixels is only diagonally connected, which the detector's 4-connected component confirmation
// discards) with the flux concentrated near the centreline (so the Hough peak dominates).
func addDiagonalStreak(p []float32, w, h int) {
	const sigma = 1.2
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			along := float64(x+y) / 2 // position along the y=x line
			if along < 30 || along > 270 {
				continue
			}
			d := math.Abs(float64(x-y)) / math.Sqrt2 // perpendicular distance to the line
			if d > 3 {
				continue
			}
			g := float32(0.65 * math.Exp(-d*d/(2*sigma*sigma)))
			if v := seqBg + g; v > p[y*w+x] {
				p[y*w+x] = v
			}
		}
	}
}

func readFrame(t *testing.T, path string) *fits.Image {
	t.Helper()
	im, err := fits.ReadImage(path)
	require.NoError(t, err)
	return im
}

func TestMaskSequence_CleansTrailKeepsStar(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for i := 0; i < 8; i++ {
		paths = append(paths, writeSeqFrame(t, dir, fmt.Sprintf("r_light_%05d.fits", i+1), i, i == 0))
	}

	rep, err := MaskSequence(paths, 3.0, SeqOptions{})
	require.NoError(t, err)
	require.NotNil(t, rep)
	assert.False(t, rep.PerFrameFallback)
	s := rep.Summary()
	assert.Positive(t, s.MaskedPx, "the trail frame should have masked pixels")
	assert.GreaterOrEqual(t, s.Segments, 1, "the diagonal streak should be detected as a segment")

	// The trailed frame's streak is painted back toward the clean median (~background).
	trailFrame := readFrame(t, paths[0])
	var maxAlong float32
	for x := 30; x < 270; x++ {
		if v := trailFrame.Pix[0][x*seqW+x]; v > maxAlong {
			maxAlong = v
		}
	}
	assert.Less(t, maxAlong, float32(0.2), "trail pixels cleaned to near background (was 0.7)")

	// The static star survives in every frame (median replacement of a consistent pixel is ~lossless).
	for _, p := range paths {
		f := readFrame(t, p)
		assert.InDelta(t, starVal, f.Pix[0][starY*seqW+starX], 0.05, "star preserved in %s", filepath.Base(p))
	}
}

func TestMaskSequence_NoTrailDetectsNoLineKeepsStar(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for i := 0; i < 6; i++ {
		paths = append(paths, writeSeqFrame(t, dir, fmt.Sprintf("r_light_%05d.fits", i+1), i, false))
	}
	rep, err := MaskSequence(paths, 3.0, SeqOptions{})
	require.NoError(t, err)
	s := rep.Summary()
	// No straight streak exists, so no line segment is detected. The per-pixel blanket pass may still
	// clip a handful of positive noise outliers (as the legacy MaskCrossFrame always has) — that must
	// stay negligible, not a trail.
	assert.Equal(t, 0, s.Segments, "a clean sequence has no line segments")
	assert.Less(t, float64(s.MaskedPx), 0.01*float64(6*seqW*seqH), "only negligible noise-outlier cleanup")
	f := readFrame(t, paths[0])
	assert.InDelta(t, starVal, f.Pix[0][starY*seqW+starX], 0.02, "the static star survives")
}

func TestMaskSequence_FewFramesFallback(t *testing.T) {
	dir := t.TempDir()
	var paths []string
	for i := 0; i < 3; i++ { // < MinFrames → per-frame detector
		paths = append(paths, writeSeqFrame(t, dir, fmt.Sprintf("r_light_%05d.fits", i+1), i, i == 0))
	}
	rep, err := MaskSequence(paths, 3.0, SeqOptions{})
	require.NoError(t, err)
	assert.True(t, rep.PerFrameFallback, "below MinFrames uses the per-frame path")
	assert.Len(t, rep.PerFrame, 3)
}
