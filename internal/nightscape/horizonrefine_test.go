package nightscape

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// frameWithShoreline builds the profile the real panel has, top to bottom: sky glow, then a DARK
// strip of shoreline, then a BRIGHT line of town lights, then the beach.
//
// The lights are the strongest edge in the frame and they are on the wrong side of the answer, which
// is the whole reason refineHorizon looks for the most negative gradient rather than the largest.
func frameWithShoreline(w, h, skyEnds, lightsAt int, sky, dark, lights, beach float32) []float32 {
	lum := make([]float32, w*h)
	for y := 0; y < h; y++ {
		v := sky
		switch {
		case y >= lightsAt+3:
			v = beach
		case y >= lightsAt:
			v = lights
		case y >= skyEnds:
			v = dark
		}
		for x := 0; x < w; x++ {
			lum[y*w+x] = v
		}
	}
	return lum
}

// priorCutAt is the half-plane groundPrior produces: ground below row cut.
func priorCutAt(w, h, cut int) []bool {
	p := make([]bool, w*h)
	for y := cut; y < h; y++ {
		for x := 0; x < w; x++ {
			p[y*w+x] = true
		}
	}
	return p
}

// firstGroundRow reports where the mask says the ground starts in a given column.
func firstGroundRow(mask []bool, w, h, x int) int {
	for y := 0; y < h; y++ {
		if mask[y*w+x] {
			return y
		}
	}
	return -1
}

func TestRefineHorizon(t *testing.T) {
	// Proportionate to a real frame: the search band is 6% of the extent, so the pointing error
	// under test (16 rows of 400) sits inside it the way a real one does.
	const w, h = 200, 400
	const skyEnds, lightsAt = 220, 248
	const sky, dark, lights, beach = float32(0.60), float32(0.18), float32(0.95), float32(0.45)

	t.Run("it snaps to the sky-to-shoreline edge, not to the brighter lights below", func(t *testing.T) {
		lum := frameWithShoreline(w, h, skyEnds, lightsAt, sky, dark, lights, beach)
		prior := priorCutAt(w, h, 204) // the pointing sits 16 rows high of the real edge

		got, moved, ok := refineHorizon(prior, lum, w, h)

		require.True(t, ok)
		assert.InDelta(t, skyEnds, firstGroundRow(got, w, h, w/2), 4,
			"the boundary must land on the sky/shoreline drop")
		assert.Less(t, firstGroundRow(got, w, h, w/2), lightsAt,
			"it must never settle on the lights, which are the stronger edge")
		assert.Greater(t, moved, 5.0, "it should report having moved off the prior")
	})

	t.Run("a frame with no edge keeps the pointing's line", func(t *testing.T) {
		flat := make([]float32, w*h)
		for i := range flat {
			flat[i] = sky
		}
		prior := priorCutAt(w, h, 204)

		_, _, ok := refineHorizon(prior, flat, w, h)

		assert.False(t, ok, "with nothing to snap to it must decline and leave the prior alone")
	})

	// A star inside the search band is a bright spot, so the drop just past it can rival the horizon.
	t.Run("a bright spot does not drag its own line", func(t *testing.T) {
		lum := frameWithShoreline(w, h, skyEnds, lightsAt, sky, dark, lights, beach)
		for y := 208; y < 214; y++ {
			for x := 100; x < 106; x++ {
				lum[y*w+x] = 3.0
			}
		}
		prior := priorCutAt(w, h, 204)

		got, _, ok := refineHorizon(prior, lum, w, h)

		require.True(t, ok)
		assert.InDelta(t, skyEnds, firstGroundRow(got, w, h, 101), 5,
			"the column through the spot must follow its neighbours, not the spot")
	})

	t.Run("the boundary is continuous across the frame", func(t *testing.T) {
		lum := frameWithShoreline(w, h, skyEnds, lightsAt, sky, dark, lights, beach)
		// A sloping shoreline, which is what a real one does.
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				if y >= skyEnds+x/20 && y < lightsAt {
					lum[y*w+x] = dark
				} else if y < skyEnds+x/20 {
					lum[y*w+x] = sky
				}
			}
		}
		prior := priorCutAt(w, h, 204)

		got, _, ok := refineHorizon(prior, lum, w, h)

		require.True(t, ok)
		prev := firstGroundRow(got, w, h, 0)
		for x := 1; x < w; x++ {
			cur := firstGroundRow(got, w, h, x)
			require.LessOrEqual(t, abs(cur-prev), 6, "the boundary jumped at column %d", x)
			prev = cur
		}
	})

	t.Run("a prior that is not a half-plane is declined rather than misread", func(t *testing.T) {
		lum := frameWithShoreline(w, h, skyEnds, lightsAt, sky, dark, lights, beach)
		all := make([]bool, w*h) // no transition at all
		_, _, ok := refineHorizon(all, lum, w, h)
		assert.False(t, ok)

		require.NotPanics(t, func() { refineHorizon(nil, lum, w, h) })
		require.NotPanics(t, func() { refineHorizon(priorCutAt(w, h, 204), nil, w, h) })
	})
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
