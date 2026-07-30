package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/photom"
)

// flatCurve builds a FrameCurve whose probes all sit at bg (pure sky) with the given noise.
func flatCurve(bg, noise float64) photom.FrameCurve {
	var fc photom.FrameCurve
	for i := range fc.Q {
		fc.Q[i] = bg
	}
	fc.Bg, fc.Noise = bg, noise
	return fc
}

func TestSeamOffsetDelta_Cases(t *testing.T) {
	anchor := flatCurve(0.10, 0.010)
	tests := []struct {
		name       string
		group      photom.FrameCurve
		wantDelta  float64
		wantReason string
	}{
		{"nominal pedestal corrected", flatCurve(0.13, 0.010), -0.03, ""},
		{"negative pedestal corrected", flatCurve(0.08, 0.010), 0.02, ""},
		{"below stack visibility skipped", flatCurve(0.1001, 0.010), 0, "below stack visibility"},
		{"beyond cap degraded", flatCurve(0.30, 0.010), 0, "sanity cap"},
		{"clip guard degraded", flatCurve(0.145, 0.060), 0, "clip"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			delta, reason := seamOffsetDelta(tt.group, anchor)
			if tt.wantReason == "" {
				require.Empty(t, reason)
				assert.InDelta(t, tt.wantDelta, delta, 1e-9)
				return
			}
			assert.Zero(t, delta)
			assert.Contains(t, reason, tt.wantReason)
		})
	}
}

func TestSeamOffsetDelta_ContaminatedOverlap(t *testing.T) {
	anchor := flatCurve(0.10, 0.010)
	group := flatCurve(0.14, 0.010)
	group.Q[2] = 0.10 // P20 says no pedestal while the background says +0.04 → extended signal
	delta, reason := seamOffsetDelta(group, anchor)
	assert.Zero(t, delta)
	assert.Contains(t, reason, "contaminated")
}

func TestOverlapKeepFn_MapsThroughHomography(t *testing.T) {
	const w, h = 640, 480
	cv := canvasSpec{W: w, H: h}
	gridW := (w + coverageDownscale - 1) / coverageDownscale
	gridH := (h + coverageDownscale - 1) / coverageDownscale
	mask := make([]bool, gridW*gridH)
	for gy := 0; gy < gridH; gy++ { // admit the canvas' left half only
		for gx := 0; gx < gridW/2; gx++ {
			mask[gy*gridW+gx] = true
		}
	}

	keep := overlapKeepFn(identityH, mask, cv, gridW, gridH)
	assert.True(t, keep(10, 10))
	assert.False(t, keep(w-10, 10))

	shifted := identityH
	shifted[2] = -float64(w) / 2 // frame maps onto the canvas' left half
	keepShifted := overlapKeepFn(shifted, mask, cv, gridW, gridH)
	assert.True(t, keepShifted(w-10, 10), "right frame edge lands on the admitted canvas half")
	assert.False(t, keepShifted(10, 10), "left frame edge maps off-canvas")
}

// writeMergedFrames writes n 256×256 frames of the given level as light_%05d.fits.
func writeMergedFrames(t *testing.T, dir string, start, n int, level float32) {
	t.Helper()
	for i := 0; i < n; i++ {
		im := fits.NewImage(256, 256, 1)
		for j := range im.Pix[0] {
			im.Pix[0][j] = level
		}
		require.NoError(t, im.WriteFITS(filepath.Join(dir, fmt.Sprintf("light_%05d.fits", start+i+1))))
	}
}

func TestRefitGroupOffsets_AppliesPlantedDelta(t *testing.T) {
	dir := t.TempDir()
	writeMergedFrames(t, dir, 0, 2, 0.10) // anchor group: frames 1-2 at the reference sky
	writeMergedFrames(t, dir, 2, 2, 0.13) // second night: frames 3-4 with a +0.03 pedestal

	review := registrationReview{FrameH: map[int][9]float64{
		0: identityH, 1: identityH, 2: identityH, 3: identityH,
	}}
	groups := []lightGroup{{Session: "2020-05-06"}, {Session: "2020-04-26"}}
	spans := []groupSpan{{Start: 0, End: 2}, {Start: 2, End: 4}}
	ch := &ChannelResult{}

	refitGroupOffsets(context.Background(), Options{}, ch, groups, spans, 0,
		review, dir, "light", "L", 256, 256, stepRef{})

	require.NotNil(t, ch.Seam)
	require.Len(t, ch.Seam.Offsets, 1)
	rec := ch.Seam.Offsets[0]
	assert.True(t, rec.Applied, "reason: %s", rec.Reason)
	assert.InDelta(t, -0.03, rec.Delta, 2e-3)

	anchor, err := fits.ReadImage(filepath.Join(dir, "light_00001.fits"))
	require.NoError(t, err)
	assert.InDelta(t, 0.10, float64(anchor.Pix[0][0]), 1e-6, "anchor frames must stay untouched")
	corrected, err := fits.ReadImage(filepath.Join(dir, "light_00003.fits"))
	require.NoError(t, err)
	assert.InDelta(t, 0.10, float64(corrected.Pix[0][0]), 2e-3, "night frames moved onto the anchor sky")
}

func TestRefitGroupOffsets_SoftFailsOnUnreadableFrame(t *testing.T) {
	dir := t.TempDir()
	writeMergedFrames(t, dir, 0, 2, 0.10) // anchor only; the second group's files are missing

	review := registrationReview{FrameH: map[int][9]float64{
		0: identityH, 1: identityH, 2: identityH, 3: identityH,
	}}
	groups := []lightGroup{{Session: "a"}, {Session: "b"}}
	spans := []groupSpan{{Start: 0, End: 2}, {Start: 2, End: 4}}
	ch := &ChannelResult{}

	refitGroupOffsets(context.Background(), Options{}, ch, groups, spans, 0,
		review, dir, "light", "L", 256, 256, stepRef{})

	require.NotNil(t, ch.Seam)
	require.Len(t, ch.Seam.Offsets, 1)
	assert.False(t, ch.Seam.Offsets[0].Applied)
	assert.NotEmpty(t, ch.Seam.Offsets[0].Reason)
}
