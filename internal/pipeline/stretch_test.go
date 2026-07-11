package pipeline

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestStretchScript(t *testing.T) {
	// SPCC path: SCNR green removal runs, before the autostretch/save.
	withGreen := stretchScript("rgb_base", "/o/base", true, true, 0.06)
	assert.Contains(t, withGreen, "load rgb_base\n")
	assert.Contains(t, withGreen, "rmgreen 0\n")
	assert.Contains(t, withGreen, "savetif /o/base\n")
	assert.Less(t, strings.Index(withGreen, "rmgreen 0"), strings.Index(withGreen, "savetif"),
		"green removal must precede the stretch/save")

	// Star-field / neutralized path: NO SCNR (the median star is already neutral → subtracting green
	// tips it magenta), just the stretch.
	noGreen := stretchScript("rgb_base", "/o/base", false, false, 0.06)
	assert.NotContains(t, noGreen, "rmgreen", "non-SPCC calibration must not SCNR (magenta tip)")
	assert.Contains(t, noGreen, "load rgb_base\n")
	assert.Contains(t, noGreen, "savetif /o/base\n")
}
