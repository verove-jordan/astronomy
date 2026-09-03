package pipeline

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The composite's input TIFF must carry the stretch's own numbers. Siril's savetif CONVERTS from the
// image's colour profile, and PCC attaches one (HDU 2, EXTNAME=ICCProfile) — so without an assign the
// sRGB transfer curve is applied to already-stretched data and the sky goes 0.061 -> 0.286, taking the
// saturation boost with it into a magenta cast. icc_assign reinterprets instead of converting.
func TestStretchScript_AssignsTheProfileBeforeSavingTheTif(t *testing.T) {
	s := stretchScript("rgb_base", "/work/base", true, true, 0.06)

	require.Contains(t, s, "savetif /work/base")
	assert.Contains(t, s, "icc_assign sRGB\nsavetif /work/base",
		"the assign must come immediately before the save, or savetif converts")
	assert.Less(t, strings.Index(s, "icc_assign"), strings.Index(s, "savetif"))
	// And it must not be a CONVERT — that is the operation this exists to prevent.
	assert.NotContains(t, s, "icc_convert_to")
}

// The detour that flattens sky chroma in Go saves its own TIFF; it needs the same treatment, and the
// stretch-only form must not save at all.
func TestStretchScript_NoSaveWhenTheCallerSavesItself(t *testing.T) {
	s := stretchScript("rgb_base", "", true, true, 0.06)

	assert.NotContains(t, s, "savetif")
	assert.NotContains(t, s, "icc_assign", "nothing to assign when nothing is saved here")
	assert.Equal(t, "icc_assign sRGB\nsavetif /work/base\n", saveDisplayTif("/work/base"),
		"the shared helper is what both save paths use")
}
