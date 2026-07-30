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
	pv := SuggestForInventory(inv, sampleMasters(), false)
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

	base := MatchForLightExcluding(light, masters, nil, false)
	require.NotNil(t, base.Dark)
	require.NotNil(t, base.Flat)

	sel := MatchForLightExcluding(light, masters, []string{SuggestID(light, RoleDark)}, false)
	assert.Nil(t, sel.Dark, "excluded dark is dropped")
	assert.NotNil(t, sel.Flat, "flat untouched")
}

// A forced preview surfaces mismatched masters as included suggestions (not gap notes): a gain-200 dark
// is applied to gain-139 lights of a different exposure and temperature, and the panel says why.
func TestSuggestForInventory_Forced(t *testing.T) {
	inv := &inspect.Inventory{Sets: []inspect.Set{
		lightSet("L", 139, 21, 60000, -20, 1), // gain 139, 60s, -20°C — nothing in sampleMasters matches
	}}
	strict := SuggestForInventory(inv, sampleMasters(), false)
	require.Len(t, strict.Channels, 1)
	for _, s := range strict.Channels[0].Suggestions {
		assert.NotEqual(t, RoleDark, s.Role, "no dark should match strictly")
	}

	forced := SuggestForInventory(inv, sampleMasters(), true)
	require.Len(t, forced.Channels, 1)
	hasDark := false
	for _, s := range forced.Channels[0].Suggestions {
		if s.Role == RoleDark {
			hasDark = true
			assert.Equal(t, int64(200), s.Master.Gain, "the mismatched gain-200 dark is force-applied")
		}
	}
	assert.True(t, hasDark, "force must surface the mismatched dark as a suggestion")
	assert.True(t, anyContains(forced.Channels[0].Notes, "forced dark"),
		"expected a forced-dark note, got %v", forced.Channels[0].Notes)
}

// The real-world bug this guards: lights + their own bias/dark/flat inspected together, while the
// library only holds a stale wrong-camera bias. The preview must match the capture's frames (via
// PreviewCandidates synthetics), not report gaps or surface the stale master.
func TestSuggestForInventory_CaptureOwnCalFrames(t *testing.T) {
	inv := &inspect.Inventory{Sets: []inspect.Set{
		lightSet("L", 0, 10, 10, -15, 1), // gain 0 / offset 10 / 10ms — the moon capture
		previewCalSet(inspect.Bias, "", 0, 10, 0, 0, 1, 400),
		previewCalSet(inspect.Dark, "", 0, 10, 10, -15, 1, 64),
		previewCalSet(inspect.Flat, "L", 0, 10, 10, -15, 1, 239),
	}}
	staleLib := []Master{{Type: MasterBias, Gain: 300, Offset: 50, Bin: 1, FrameCount: 400, Path: "/lib/stale_bias.fits"}}

	pv := SuggestForInventory(inv, PreviewCandidates(inv, staleLib), false)
	require.Len(t, pv.Channels, 1)
	ch := pv.Channels[0]

	roles := map[string]CalibSuggestion{}
	for _, s := range ch.Suggestions {
		roles[s.Role] = s
	}
	require.Contains(t, roles, RoleDark)
	require.Contains(t, roles, RoleFlat)
	require.Contains(t, roles, RoleBias)
	for role, s := range roles {
		assert.True(t, s.FromCapture, "%s must come from the capture", role)
	}
	assert.Equal(t, int64(0), roles[RoleBias].Master.Gain, "the stale gain-300 library bias must lose to the capture's")
	assert.Empty(t, ch.Notes, "no gap notes when the capture calibrates itself")
}

// M92-like: 20 s lights with only a 10 ms capture dark + bias → the dark still applies via the
// bias-scaled -opt path, sourced from the capture, with the explanatory note.
func TestSuggestForInventory_CaptureScalableDark(t *testing.T) {
	inv := &inspect.Inventory{Sets: []inspect.Set{
		lightSet("L", 0, 10, 20000, -15, 1),
		previewCalSet(inspect.Bias, "", 0, 10, 0, 0, 1, 400),
		previewCalSet(inspect.Dark, "", 0, 10, 10, -15, 1, 64),
	}}
	pv := SuggestForInventory(inv, PreviewCandidates(inv, nil), false)
	require.Len(t, pv.Channels, 1)
	ch := pv.Channels[0]

	var dark *CalibSuggestion
	for i := range ch.Suggestions {
		if ch.Suggestions[i].Role == RoleDark {
			dark = &ch.Suggestions[i]
		}
	}
	require.NotNil(t, dark, "the 10ms capture dark must be offered for 20s lights via -opt scaling")
	assert.True(t, dark.FromCapture)
	assert.Equal(t, int64(10), dark.Master.ExposureMs)
	assert.True(t, anyContains(ch.Notes, "dark-optimized"), "expected the -opt note, got %v", ch.Notes)
}

func TestSuggestID_Stable(t *testing.T) {
	light := inspect.SetKey{Filter: "Ha", ExposureMs: 30000, Gain: 200, Offset: 50, TempBucket: -10, Bin: 1}
	assert.Equal(t, SuggestID(light, RoleDark), SuggestID(light, RoleDark))
	assert.NotEqual(t, SuggestID(light, RoleDark), SuggestID(light, RoleFlat))
}
