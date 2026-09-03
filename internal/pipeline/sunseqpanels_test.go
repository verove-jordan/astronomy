package pipeline

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/eclipsegeom"
	"github.com/verove-jordan/astronomy/internal/solar"
)

// writeEclipseFrames lays a window of frames on disk, as ingest would have left them: the true
// geometry for each instant, drifting a little frame to frame the way a hand-held rig does.
func writeEclipseFrames(t *testing.T, dir string, at time.Time, n int, stepMs int64) []solar.Frame {
	t.Helper()
	var frames []solar.Frame
	for i := 0; i < n; i++ {
		when := at.Add(time.Duration(int64(i)-int64(n)/2) * time.Duration(stepMs) * time.Millisecond)
		circ := eclipsegeom.At(when, piriacSite)
		im := drawEclipseFrame(circ, 120, 31, false)
		path := filepath.Join(dir, fmt.Sprintf("f_%04d.fits", len(frames)))
		require.NoError(t, im.WriteFITS(path))
		g, ok := solar.FitPair(im)
		require.True(t, ok)
		frames = append(frames, solar.Frame{
			Path: path, Source: filepath.Join(dir, "hero.MOV"), Index: i,
			TimeMs: when.UnixMilli(), Score: 1 + float64(i%3)/10, Limb: g.Sun, Moon: g.Moon,
		})
	}
	return frames
}

func TestBuildPanel_TakesTheSharpestFrameWithoutStacking(t *testing.T) {
	// The default is a single frame placed by one resample and nothing else. Stacking a crescent
	// pays for registration twice and, on this capture, renders a hard seam where the occulter's
	// swept band is recovered plus a mottled crust where sharpening meets the averaged noise.
	dir := t.TempDir()
	at := time.Date(2026, 8, 12, 18, 16, 0, 0, time.UTC)
	frames := writeEclipseFrames(t, dir, at, 15, 2000)

	p := solar.DefaultPreset()
	p.TwoBody = true
	p.WindowSeconds = 30
	p.MinFrames = 4
	p.Drizzle = 1

	panel, warnings, err := buildPanel(context.Background(), eclipsegeom.Panel{
		Side: eclipsegeom.Ingress, At: at, Magnitude: 0.9,
	}, frames, p, piriacSite)

	require.NoError(t, err)
	for _, w := range warnings {
		t.Logf("note: %s", w)
	}
	require.NotNil(t, panel.Master)
	assert.Equal(t, "frame", panel.Choice)
	assert.Equal(t, 1, panel.Frames, "one frame, not an average of many")
	assert.False(t, panel.StackPSF.OK, "no stack is built at all, so there is nothing to measure")
	assert.True(t, panel.FramePSF.OK, "the frame's own edge is measured")
	assert.True(t, panel.Pair.Eclipsed(), "the panel carries its occulter")
	// The phase the sky predicts and the phase the circles measure must agree.
	assert.InDelta(t, panel.Circ.Obscuration, panel.Pair.Obscuration, 0.03,
		"measured %.3f vs predicted %.3f", panel.Pair.Obscuration, panel.Circ.Obscuration)
	// Dated by the frame it USED, not by the middle of the window it chose from — a panel described
	// by an instant its pixels do not come from reports a phase it is not. Which frame that is comes
	// from the selector, which shortlists on score and then decides on the measured edge.
	window := framesAround(frames, at, p.WindowSeconds, p.MinFrames)
	best, _ := sharpestAgreeingWithTheSky(window, piriacSite, requireWholeDisc)
	assert.Equal(t, best.TimeMs, panel.At.UnixMilli(), "the panel carries its own frame's clock")
	assert.InDelta(t, at.UnixMilli(), panel.At.UnixMilli(), 16000, "and that frame is inside the window")
}

func TestBuildPanel_SequenceStackWeighsBothAndSaysWhichWon(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 8, 12, 18, 16, 0, 0, time.UTC)
	frames := writeEclipseFrames(t, dir, at, 15, 2000)

	p := solar.DefaultPreset()
	p.TwoBody = true
	p.WindowSeconds = 30
	p.MinFrames = 4
	p.Drizzle = 1
	p.SequenceStack = true

	panel, _, err := buildPanel(context.Background(), eclipsegeom.Panel{At: at}, frames, p, piriacSite)

	require.NoError(t, err)
	t.Logf("kept the %s: stack σ %.2f (ok=%v), single frame σ %.2f (ok=%v)",
		panel.Choice, panel.StackPSF.SigmaPx, panel.StackPSF.OK, panel.FramePSF.SigmaPx, panel.FramePSF.OK)

	assert.True(t, panel.StackPSF.OK, "with the knob on, the stack is built and measured")
	assert.True(t, panel.FramePSF.OK)
	assert.Contains(t, []string{"stack", "frame"}, panel.Choice)
	if panel.Choice == "stack" {
		assert.Less(t, panel.StackPSF.SigmaPx, panel.FramePSF.SigmaPx, "a stack is only kept when it measures sharper")
		assert.Greater(t, panel.Frames, 1)
	}
}

func TestBuildPanel_RefusesAPhaseWithNoFrames(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 8, 12, 18, 16, 0, 0, time.UTC)
	frames := writeEclipseFrames(t, dir, at, 4, 1000)

	p := solar.DefaultPreset()
	p.TwoBody = true

	_, _, err := buildPanel(context.Background(), eclipsegeom.Panel{At: at}, nil, p, piriacSite)
	require.Error(t, err)

	// With frames present it must succeed even though the window is far shorter than asked for.
	_, _, err = buildPanel(context.Background(), eclipsegeom.Panel{At: at}, frames, p, piriacSite)
	require.NoError(t, err)
}

func TestStackSharpest_StacksExactlyOneFrame(t *testing.T) {
	// The single-frame candidate goes through the identical path the window stack takes so the two
	// land on the same raster. A stacker that quietly needs more than one frame would make that
	// candidate unavailable, and the choice would always fall to the stack without saying so.
	dir := t.TempDir()
	at := time.Date(2026, 8, 12, 18, 16, 0, 0, time.UTC)
	frames := writeEclipseFrames(t, dir, at, 6, 1000)

	p := solar.DefaultPreset()
	p.TwoBody = true
	p.Drizzle = 1

	got, err := stackSharpest(context.Background(), sharpestOf(frames), p)

	require.NoError(t, err)
	require.NotNil(t, got.Master)
	assert.Equal(t, 1, got.Frames)
	assert.True(t, got.Pair().Eclipsed())
}
