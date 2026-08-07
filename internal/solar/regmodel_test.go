package solar

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clipFrames builds n frames of one source, 1/30 s apart, starting at t0.
func clipFrames(source string, n int, t0 int64) []Frame {
	out := make([]Frame, n)
	for i := range out {
		out[i] = Frame{Source: source, Index: i, TimeMs: t0 + int64(i)*33}
	}
	return out
}

func TestModelRotations_RejectsTheCorrelatorsWrongPeaks(t *testing.T) {
	frames := clipFrames("/x/clip.MOV", 60, 0)
	raw := make([]float64, len(frames))
	ok := make([]bool, len(frames))
	for i := range raw {
		raw[i], ok[i] = -2.5, true
	}
	// What a real clip does: a handful of frames where the correlation found a secondary maximum.
	raw[7], raw[23], raw[41] = -6.4, -6.3, +1.2

	got, notes := ModelRotations(frames, raw, ok)

	require.Len(t, got, len(frames))
	for i, v := range got {
		assert.InDelta(t, -2.5, v, 0.05, "frame %d took the outliers with it", i)
	}
	assert.Empty(t, notes)
}

// The step between two clips is real — the camera was touched — so it must survive, while the
// scatter within each clip does not.
func TestModelRotations_KeepsThePerClipStep(t *testing.T) {
	a := clipFrames("/x/a.MOV", 40, 0)
	b := clipFrames("/x/b.MOV", 40, 60_000)
	frames := append(append([]Frame{}, a...), b...)
	raw := make([]float64, len(frames))
	ok := make([]bool, len(frames))
	for i := range frames {
		ok[i] = true
		if i < 40 {
			raw[i] = -2.5 + 0.4*math.Sin(float64(i)) // noisy
		} else {
			raw[i] = -0.08 + 0.05*math.Sin(float64(i))
		}
	}

	got, _ := ModelRotations(frames, raw, ok)

	assert.InDelta(t, -2.5, median(got[:40]), 0.2, "the first clip keeps its own rotation")
	assert.InDelta(t, -0.08, median(got[40:]), 0.1, "the second keeps its own")
	assert.Greater(t, math.Abs(median(got[:40])-median(got[40:])), 2.0, "the step between them is real")
}

// A drift within a clip is physical — an alt-az field turns while it records — so the model must
// follow it rather than flattening it to one number.
func TestModelRotations_FollowsADriftInTime(t *testing.T) {
	frames := clipFrames("/x/clip.MOV", 120, 0)
	raw := make([]float64, len(frames))
	ok := make([]bool, len(frames))
	const perMs = 0.33 / 60000 // a third of a degree a minute
	for i, f := range frames {
		raw[i], ok[i] = perMs*float64(f.TimeMs), true
	}
	raw[10], raw[80] = -5, +6 // and two frames the correlator got wrong

	got, _ := ModelRotations(frames, raw, ok)

	assert.InDelta(t, 0, got[0], 0.02)
	assert.InDelta(t, perMs*float64(frames[len(frames)-1].TimeMs), got[len(got)-1], 0.02,
		"the drift must be followed, not averaged away")
}

// A frame the correlator refused still needs a rotation: leaving it at zero would stack it degrees
// out of true, which is far worse than the estimate it could not make.
func TestModelRotations_FillsInTheFramesThatCouldNotBeSolved(t *testing.T) {
	frames := clipFrames("/x/clip.MOV", 30, 0)
	raw := make([]float64, len(frames))
	ok := make([]bool, len(frames))
	for i := range raw {
		raw[i], ok[i] = -2.5, true
	}
	raw[5], ok[5] = 0, false
	raw[6], ok[6] = 0, false

	got, _ := ModelRotations(frames, raw, ok)

	assert.InDelta(t, -2.5, got[5], 0.05)
	assert.InDelta(t, -2.5, got[6], 0.05)
}

func TestModelRotations_ReportsAClipItCannotModel(t *testing.T) {
	frames := clipFrames("/x/clip.MOV", 30, 0)
	raw := make([]float64, len(frames))
	ok := make([]bool, len(frames))

	got, notes := ModelRotations(frames, raw, ok)

	require.Len(t, got, len(frames))
	assert.NotEmpty(t, notes, "a clip whose rotation is unknowable must say so")
}

func TestRobustLine(t *testing.T) {
	tests := []struct {
		name          string
		t, y          []float64
		wantSlope     float64
		wantIntercept float64
	}{
		{
			name: "too few points for a slope", t: []float64{0, 1, 2}, y: []float64{5, 5, 9},
			wantSlope: 0, wantIntercept: 5,
		},
		{
			name: "a clean line",
			t:    seq(20), y: scaled(seq(20), 2, 3),
			wantSlope: 2, wantIntercept: 3,
		},
		{
			name: "a line with a quarter of the points wrong",
			t:    seq(20), y: corrupt(scaled(seq(20), 2, 3), 5, 100),
			wantSlope: 2, wantIntercept: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slope, intercept := robustLine(tt.t, tt.y)

			assert.InDelta(t, tt.wantSlope, slope, 1e-6)
			assert.InDelta(t, tt.wantIntercept, intercept, 1e-6)
		})
	}
}

func seq(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = float64(i)
	}
	return out
}

func scaled(t []float64, m, c float64) []float64 {
	out := make([]float64, len(t))
	for i, x := range t {
		out[i] = m*x + c
	}
	return out
}

func corrupt(y []float64, every int, by float64) []float64 {
	out := append([]float64(nil), y...)
	for i := range out {
		if i%every == 0 {
			out[i] += by
		}
	}
	return out
}
