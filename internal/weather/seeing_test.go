package weather

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// calmProfile is a settled high-pressure night: light winds at every level, a shallow boundary layer.
func calmProfile() shear {
	return shear{jetKmh: 40, w500Kmh: 30, w850Kmh: 15, surfaceKmh: 5, blHeightM: 300}
}

func TestDerivedSeeing_RealisticProfilesLandInTheRightBand(t *testing.T) {
	tests := []struct {
		name    string
		profile shear
		wantMin float64
		wantMax float64
	}{
		{
			name:    "settled high pressure",
			profile: calmProfile(),
			wantMin: 0.6, wantMax: 1.6,
		},
		{
			name:    "average night, moderate flow",
			profile: shear{jetKmh: 70, w500Kmh: 45, w850Kmh: 25, surfaceKmh: 8, blHeightM: 400},
			wantMin: 1.2, wantMax: 2.5,
		},
		{
			name:    "jet stream overhead",
			profile: shear{jetKmh: 200, w500Kmh: 90, w850Kmh: 40, surfaceKmh: 15, blHeightM: 800},
			wantMin: 4.0, wantMax: 5.0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := derivedSeeing(tt.profile)

			assert.GreaterOrEqual(t, got, tt.wantMin)
			assert.LessOrEqual(t, got, tt.wantMax)
		})
	}
}

func TestDerivedSeeing_MonotonicInShear(t *testing.T) {
	prev := 0.0
	for _, jet := range []float64{40, 80, 120, 160, 200, 260} {
		p := calmProfile()
		p.jetKmh = jet

		got := derivedSeeing(p)

		assert.Greater(t, got, prev, "seeing must worsen as the upper-level flow strengthens (jet=%v)", jet)
		prev = got
	}
}

func TestDerivedSeeing_MonotonicInSurfaceWind(t *testing.T) {
	prev := 0.0
	for _, wind := range []float64{2, 8, 15, 25, 40} {
		p := calmProfile()
		p.surfaceKmh = wind

		got := derivedSeeing(p)

		assert.Greater(t, got, prev, "ground turbulence must grow with surface wind (wind=%v)", wind)
		prev = got
	}
}

func TestDerivedSeeing_DeepBoundaryLayerIsWorseThanShallow(t *testing.T) {
	shallow, deep := calmProfile(), calmProfile()
	shallow.blHeightM, deep.blHeightM = 200, 2000

	assert.Less(t, derivedSeeing(shallow), derivedSeeing(deep),
		"a deep mixed layer stirs more air over the telescope than a shallow inversion-capped one")
}

func TestDerivedSeeing_ClampedToThePublishedRange(t *testing.T) {
	still := shear{jetKmh: 0.5, w500Kmh: 0.5, w850Kmh: 0.5, surfaceKmh: 0.1, blHeightM: 100}
	storm := shear{jetKmh: 380, w500Kmh: 10, w850Kmh: 5, surfaceKmh: 120, blHeightM: 3000}

	assert.GreaterOrEqual(t, derivedSeeing(still), seeingFloorArcsec)
	assert.LessOrEqual(t, derivedSeeing(storm), seeingFloorArcsec+seeingSpanArcsec)
}

// Half a wind profile is not a seeing forecast. Returning 0 ("unknown") keeps the hourly verdict from
// applying a seeing penalty it cannot justify.
func TestDerivedSeeing_IncompleteProfileIsUnknown(t *testing.T) {
	tests := []struct {
		name    string
		profile shear
	}{
		{name: "no jet level", profile: shear{w500Kmh: 30, w850Kmh: 15, surfaceKmh: 5}},
		{name: "no mid level", profile: shear{jetKmh: 40, w850Kmh: 15, surfaceKmh: 5}},
		{name: "nothing at all", profile: shear{}},
		{name: "implausible jet reading", profile: shear{jetKmh: 900, w500Kmh: 30}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Zero(t, derivedSeeing(tt.profile))
		})
	}
}

// Losing the lowest level should degrade precision, not the answer.
func TestDerivedSeeing_WorksWithoutTheLowLevelWind(t *testing.T) {
	p := calmProfile()
	p.w850Kmh = 0

	got := derivedSeeing(p)

	assert.Greater(t, got, 0.0)
	assert.LessOrEqual(t, got, derivedSeeing(calmProfile())+0.5)
}
