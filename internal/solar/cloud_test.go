package solar

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clipScan builds a scan whose transmission follows the given per-frame profile.
func clipScan(profile []float64) []frameScan {
	out := make([]frameScan, len(profile))
	for i, t := range profile {
		out[i] = frameScan{index: i, ok: true, level: 0.075 * t, score: 0.008, limb: Limb{R: 900}}
	}
	return out
}

// clearRun returns a run of transparent frames, wobbling by the half-percent a real clear clip does.
func clearRun(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = 1 - 0.002*float64(i%3)
	}
	return out
}

// dip returns n frames at the given transmission, ramping in and out the way a real cloud edge does.
func dip(n int, depth float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		t := math.Abs(float64(i)/float64(n-1)*2 - 1) // 1 at the edges, 0 in the middle
		out[i] = depth + (1-depth)*t*t
	}
	return out
}

// belowCut counts the frames a floor should remove, from the profile itself — so the test states the
// contract rather than a hand-computed number that drifts with the fixture.
func belowCut(profile []float64, floor float64) int {
	ref := percentileOf(profile, transparencyReferencePct)
	n := 0
	for _, t := range profile {
		if t < floor*ref {
			n++
		}
	}
	return n
}

func TestGateTransparency(t *testing.T) {
	tests := []struct {
		name     string
		profile  []float64
		floor    float64
		wantNote string // empty = the gate must stay silent
	}{
		{
			name:    "a clear clip loses nothing",
			profile: clearRun(200),
			floor:   defaultTransparencyFloor,
		},
		{
			name:     "a passing cloud is removed, the clear frames are not",
			profile:  append(append(clearRun(150), dip(60, 0.90)...), clearRun(150)...),
			floor:    defaultTransparencyFloor,
			wantNote: "cloud dropped",
		},
		{
			name:    "the gate off keeps everything",
			profile: append(clearRun(100), dip(60, 0.5)...),
			floor:   0,
		},
		{
			name:    "too few frames to know what clear looked like",
			profile: []float64{1, 0.9, 0.8, 0.7},
			floor:   defaultTransparencyFloor,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frames := clipScan(tt.profile)

			kept, note := gateTransparency(frames, tt.floor)

			want := len(tt.profile)
			if tt.wantNote != "" {
				want -= belowCut(tt.profile, tt.floor)
			}
			assert.Equal(t, want, len(kept))
			if tt.wantNote == "" {
				assert.Empty(t, note)
			} else {
				assert.Contains(t, note, tt.wantNote)
			}
			// Whatever survives stays in capture order — the windowing and the time-lapse depend on it.
			for i := 1; i < len(kept); i++ {
				require.Greater(t, kept[i].index, kept[i-1].index)
			}
		})
	}
}

// A cloud is not a sharpness problem, so the gate must fire even when the clouded frames score as
// sharp as the clear ones — which is exactly what a thin, steady cloud produces.
func TestGateTransparency_FiresOnEquallySharpFrames(t *testing.T) {
	profile := append(clearRun(100), dip(50, 0.90)...)
	frames := clipScan(profile)
	for i := range frames {
		frames[i].score = 0.008 // identical sharpness throughout
	}

	kept, note := gateTransparency(frames, defaultTransparencyFloor)

	assert.Equal(t, len(profile)-belowCut(profile, defaultTransparencyFloor), len(kept))
	assert.Less(t, len(kept), len(profile), "the gate must fire on transmission alone")
	assert.True(t, strings.Contains(note, "90%"), "the note must say how bad it got, got %q", note)
}

// The gate must judge against how clear the clip GOT, not against its median — otherwise a clip that
// spent most of its time under cloud grades the cloud as normal and keeps it.
func TestGateTransparency_JudgesAgainstTheClearestNotTheMedian(t *testing.T) {
	profile := append(clearRun(70), dip(130, 0.88)...) // two thirds clouded: the median IS the cloud

	kept, note := gateTransparency(clipScan(profile), defaultTransparencyFloor)

	assert.Contains(t, note, "cloud")
	for _, f := range kept {
		assert.GreaterOrEqual(t, f.level, 0.075*defaultTransparencyFloor,
			"no clouded frame may survive while clear ones exist")
	}
}

// Broken cloud all session is still a session: the gate must hand back the clearest frames it has
// rather than the handful that happened to clear the bar.
func TestGateTransparency_KeepsTheClearestWhenMostOfTheClipIsClouded(t *testing.T) {
	profile := append(clearRun(20), dip(180, 0.70)...)

	kept, note := gateTransparency(clipScan(profile), defaultTransparencyFloor)

	assert.Equal(t, int(float64(len(profile))*(1-transparencyMaxDrop)), len(kept))
	assert.Contains(t, note, "most of the clip")
	assert.Greater(t, len(kept), belowCut(profile, defaultTransparencyFloor)*0,
		"a compromised session still produces a stack")
	for i := 1; i < len(kept); i++ {
		require.Greater(t, kept[i].index, kept[i-1].index)
	}
}
