package calib

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/stackalg"
)

// TestMasterName_RecipeFingerprint is the safety property behind per-frame-type stacking recipes.
// Masters live in a SHARED library keyed by camera settings, so a master built with a non-default
// algorithm must land in its own file: otherwise it would overwrite — or be silently reused in place
// of — the default-options master every other run depends on.
func TestMasterName_RecipeFingerprint(t *testing.T) {
	key := inspect.SetKey{Type: inspect.Dark, ExposureMs: 300000, Gain: 200, Offset: 10, Bin: 1, TempBucket: -10}
	def := defaultStack(MasterDark)

	base := masterName(MasterDark, key, def)
	assert.Equal(t, "master_DARK_300000ms_g200o10_b1_-10C", base,
		"the DEFAULT recipe must keep the historical name, so existing library masters are reused unchanged")

	gesd := def
	gesd.Reject = stackalg.RejectGESD
	gesdName := masterName(MasterDark, key, gesd)
	assert.NotEqual(t, base, gesdName, "a different algorithm must not overwrite the shared master")
	assert.True(t, strings.HasPrefix(gesdName, base+"_s"), "the variant keeps the readable stem: %s", gesdName)

	// Two different recipes must not collide with each other either.
	median := def
	median.Combine = stackalg.CombineMedian
	assert.NotEqual(t, gesdName, masterName(MasterDark, key, median))

	// And the SAME recipe must produce the same name every run, or nothing would ever be reused.
	assert.Equal(t, gesdName, masterName(MasterDark, key, gesd))
}

// TestMasterStackOptions_PerFrameType: each frame type reads its OWN recipe, and the normalization
// stays fixed by physics — bias/dark keep their pedestal, flats are levelled multiplicatively.
func TestMasterStackOptions_PerFrameType(t *testing.T) {
	m := stackalg.DefaultMasters()
	m.Bias.Reject = stackalg.RejectGESD
	m.Flat.Reject = stackalg.RejectPercentile

	assert.Equal(t, stackalg.RejectGESD, MasterStackOptions(MasterBias, m).Reject)
	assert.Equal(t, stackalg.RejectPercentile, MasterStackOptions(MasterFlat, m).Reject)
	assert.Equal(t, stackalg.RejectAuto, MasterStackOptions(MasterDark, m).Reject, "dark is untouched")

	assert.Equal(t, stackalg.NormNone, MasterStackOptions(MasterBias, m).Norm)
	assert.Equal(t, stackalg.NormNone, MasterStackOptions(MasterDark, m).Norm)
	assert.Equal(t, stackalg.NormNone, MasterStackOptions(MasterDarkFlat, m).Norm, "a flat's dark stacks like a dark")
	assert.Equal(t, stackalg.NormMul, MasterStackOptions(MasterFlat, m).Norm, "flats are multiplicative")
}
