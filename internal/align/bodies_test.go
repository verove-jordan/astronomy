package align

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/astro"
)

func TestSkyBodies_OnlyAboveHorizon(t *testing.T) {
	bodies := skyBodies(planParams())

	assert.LessOrEqual(t, len(bodies), 6, "at most the Moon + 5 naked-eye planets")
	for _, b := range bodies {
		assert.Contains(t, []string{"moon", "planet"}, b.Kind)
		assert.NotEmpty(t, b.Name)
		assert.Positive(t, astro.ApparentAltitude(b.AltDeg), "only bodies above the horizon are returned")
		assert.GreaterOrEqual(t, b.AzDeg, 0.0)
		assert.LessOrEqual(t, b.AzDeg, 360.0)
		if b.Kind == "moon" {
			assert.GreaterOrEqual(t, b.Phase, 0.0)
			assert.LessOrEqual(t, b.Phase, 1.0)
		}
	}
}

func TestPlan_IncludesSkyBodies(t *testing.T) {
	// The plan carries the sky-body landmarks (whatever is up at the fixture instant is fine; the field
	// must simply be populated by Plan, not left to the caller).
	res := Plan(planParams(), Lookup("eq-generic"), 3, nil, nil)
	assert.Equal(t, skyBodies(planParams()), res.SkyBodies)
}
