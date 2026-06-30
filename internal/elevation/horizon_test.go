package elevation

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScoreHorizon(t *testing.T) {
	radii := []float64{1000, 2500}

	t.Run("flat terrain is fully open", func(t *testing.T) {
		naz := 4
		ring := make([]float64, naz*len(radii))
		for i := range ring {
			ring[i] = 100
		}
		h := scoreHorizon(100, ring, radii, naz, 3)
		assert.Equal(t, 100.0, h.OpennessPct)
		assert.Equal(t, 0.0, h.MaxObstructionDeg)
	})

	t.Run("a ridge in one direction reduces openness", func(t *testing.T) {
		naz := 4
		ring := make([]float64, naz*len(radii))
		// site at 0; azimuth index 1, nearest radius (1000 m) rises 200 m → atan(200/1000)=11.3°.
		ring[1*len(radii)+0] = 200
		h := scoreHorizon(0, ring, radii, naz, 3)
		assert.InDelta(t, 75.0, h.OpennessPct, 0.01) // 3 of 4 azimuths open
		assert.InDelta(t, 11.3, h.MaxObstructionDeg, 0.3)
		assert.InDelta(t, 90.0, h.WorstAzimuthDeg, 0.01) // azimuth index 1 of 4 → 90°
	})

	t.Run("terrain below the site never obstructs", func(t *testing.T) {
		naz := 4
		ring := make([]float64, naz*len(radii))
		for i := range ring {
			ring[i] = -500
		}
		h := scoreHorizon(100, ring, radii, naz, 3)
		assert.Equal(t, 100.0, h.OpennessPct)
	})
}

func TestOffset(t *testing.T) {
	// ~1 km north of (45,5): latitude up ~0.009°, longitude ~unchanged.
	la, lo := offset(45, 5, 0, 1000)
	assert.InDelta(t, 45.009, la, 0.001)
	assert.InDelta(t, 5.0, lo, 0.0005)
	// ~1 km east: longitude increases, latitude ~unchanged.
	la2, lo2 := offset(45, 5, math.Pi/2, 1000)
	assert.InDelta(t, 45.0, la2, 0.0005)
	assert.Greater(t, lo2, 5.0)
}
