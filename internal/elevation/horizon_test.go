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

func TestScoreHorizonCanopy(t *testing.T) {
	radii := []float64{30, 1000} // one near-field ring (trees) + one far ring (terrain)
	naz := 4                     // az0=N(0°), az1=E(90°), az2=S(180°), az3=W(270°)

	t.Run("no canopy + no eye is byte-identical to terrain-only scoreHorizon", func(t *testing.T) {
		ring := make([]float64, naz*len(radii))
		ring[1*len(radii)+1] = 200 // a ridge to the east at 1000 m
		want := scoreHorizon(0, ring, radii, naz, 3)
		got := scoreHorizonCanopy(horizonScore{site: 0, ring: ring, radii: radii, naz: naz, openDeg: 3, southAz: 180})
		assert.Equal(t, want.OpennessPct, got.OpennessPct)
		assert.Equal(t, want.MaxObstructionDeg, got.MaxObstructionDeg)
		assert.Equal(t, want.WorstAzimuthDeg, got.WorstAzimuthDeg)
	})

	t.Run("a nearby southern treeline blocks that azimuth", func(t *testing.T) {
		ring := make([]float64, naz*len(radii))
		canopy := make([]float64, naz*len(radii))
		canopy[2*len(radii)+0] = 20 // 20 m canopy due south at the 30 m ring → atan(20/30)=33.7°
		got := scoreHorizonCanopy(horizonScore{site: 0, ring: ring, canopy: canopy, radii: radii, naz: naz, openDeg: 3, southAz: 180})
		assert.InDelta(t, 33.7, got.MaxObstructionDeg, 0.3)
		assert.InDelta(t, 180.0, got.WorstAzimuthDeg, 0.01)
		assert.InDelta(t, 75.0, got.OpennessPct, 0.01) // 3 of 4 azimuths still open
		assert.InDelta(t, 33.7, got.SouthObstructionDeg, 0.3)
		assert.InDelta(t, 0.0, got.SouthOpennessPct, 0.01) // the only south-arc azimuth is blocked
	})

	t.Run("eye height lowers the obstruction angle", func(t *testing.T) {
		ring := make([]float64, naz*len(radii))
		canopy := make([]float64, naz*len(radii))
		canopy[0] = 20 // 20 m canopy at the 30 m ring, azimuth 0
		noEye := scoreHorizonCanopy(horizonScore{site: 0, ring: ring, canopy: canopy, radii: radii, naz: naz, openDeg: 3, southAz: 180})
		withEye := scoreHorizonCanopy(horizonScore{site: 0, ring: ring, canopy: canopy, radii: radii, naz: naz, openDeg: 3, eyeHeight: 1.6, southAz: 180})
		assert.Less(t, withEye.MaxObstructionDeg, noEye.MaxObstructionDeg)
	})

	t.Run("a northern block leaves the south metric clear", func(t *testing.T) {
		ring := make([]float64, naz*len(radii))
		canopy := make([]float64, naz*len(radii))
		canopy[0*len(radii)+0] = 20 // due north
		got := scoreHorizonCanopy(horizonScore{site: 0, ring: ring, canopy: canopy, radii: radii, naz: naz, openDeg: 3, southAz: 180})
		assert.InDelta(t, 33.7, got.MaxObstructionDeg, 0.3) // overall still sees the block
		assert.Equal(t, 0.0, got.SouthObstructionDeg)       // but it is due north
		assert.InDelta(t, 100.0, got.SouthOpennessPct, 0.01)
	})
}
