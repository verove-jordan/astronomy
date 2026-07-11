package calib

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

func lightSet(filter string, gain, offset, expMs int64, tempC, bin int) inspect.Set {
	return inspect.Set{Key: inspect.SetKey{
		Type: inspect.Light, Filter: filter, ExposureMs: expMs,
		Gain: gain, Offset: offset, TempBucket: tempC, Bin: bin,
	}, Count: 10}
}

func sampleMasters() []Master {
	return []Master{
		{Type: MasterDark, Gain: 200, Offset: 50, Bin: 1, ExposureMs: 30000, TempMilliC: -10000, FrameCount: 18},
		{Type: MasterFlat, Filter: "L", Gain: 200, Offset: 50, Bin: 1, FrameCount: 20},
	}
}

func TestSuggestForInventory(t *testing.T) {
	inv := &inspect.Inventory{Sets: []inspect.Set{
		lightSet("L", 200, 50, 30000, -10, 1),
		lightSet("R", 200, 50, 30000, -10, 1),
	}}
	pv := SuggestForInventory(inv, sampleMasters())
	require.Len(t, pv.Channels, 2)

	// L: matching dark + exact-filter flat; no fallback note.
	l := pv.Channels[0]
	assert.Equal(t, "L", l.Filter)
	roles := map[string]Master{}
	for _, s := range l.Suggestions {
		roles[s.Role] = s.Master
	}
	require.Contains(t, roles, RoleDark)
	require.Contains(t, roles, RoleFlat)
	assert.Equal(t, 18, roles[RoleDark].FrameCount)
	assert.Empty(t, l.Notes)

	// R: same dark, but only an L flat exists → cross-filter fallback is explained.
	r := pv.Channels[1]
	assert.Equal(t, "R", r.Filter)
	hasFlat := false
	for _, s := range r.Suggestions {
		if s.Role == RoleFlat {
			hasFlat = true
		}
	}
	assert.True(t, hasFlat)
	assert.NotEmpty(t, r.Notes)
}

func TestMatchForLightExcluding(t *testing.T) {
	light := inspect.SetKey{Type: inspect.Light, Filter: "L", ExposureMs: 30000, Gain: 200, Offset: 50, TempBucket: -10, Bin: 1}
	masters := sampleMasters()

	base := MatchForLightExcluding(light, masters, nil)
	require.NotNil(t, base.Dark)
	require.NotNil(t, base.Flat)

	sel := MatchForLightExcluding(light, masters, []string{SuggestID(light, RoleDark)})
	assert.Nil(t, sel.Dark, "excluded dark is dropped")
	assert.NotNil(t, sel.Flat, "flat untouched")
}

func TestSuggestID_Stable(t *testing.T) {
	light := inspect.SetKey{Filter: "Ha", ExposureMs: 30000, Gain: 200, Offset: 50, TempBucket: -10, Bin: 1}
	assert.Equal(t, SuggestID(light, RoleDark), SuggestID(light, RoleDark))
	assert.NotEqual(t, SuggestID(light, RoleDark), SuggestID(light, RoleFlat))
}
