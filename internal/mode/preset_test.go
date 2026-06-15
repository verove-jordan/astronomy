package mode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMode(t *testing.T) {
	for _, s := range []string{"deepsky", "Nebula", "MILKYWAY", "planetary"} {
		_, err := ParseMode(s)
		assert.NoError(t, err, s)
	}
	_, err := ParseMode("bogus")
	assert.Error(t, err)
}

func TestParseFormat(t *testing.T) {
	for _, s := range []string{"image", "video", "both"} {
		f, err := ParseFormat(s)
		require.NoError(t, err)
		assert.NotEmpty(t, f)
	}
	_, err := ParseFormat("gif")
	assert.Error(t, err)
}

func TestFormatWants(t *testing.T) {
	assert.True(t, FormatImage.WantsImage())
	assert.False(t, FormatImage.WantsVideo())
	assert.True(t, FormatVideo.WantsVideo())
	assert.False(t, FormatVideo.WantsImage())
	assert.True(t, FormatBoth.WantsImage())
	assert.True(t, FormatBoth.WantsVideo())
}

func TestForPresetsDiffer(t *testing.T) {
	deep := For(Deepsky)
	neb := For(Nebula)
	mw := For(Milkyway)
	pl := For(Planetary)

	assert.Equal(t, Mono, deep.Color)
	assert.Equal(t, OSC, mw.Color)

	// Nebula keeps more frames and pushes Ha harder than deepsky.
	assert.Less(t, neb.Grade.RoundnessFloor, deep.Grade.RoundnessFloor)
	assert.Greater(t, neb.Grade.FWHMSigma, deep.Grade.FWHMSigma)
	assert.Greater(t, neb.HaScreen, deep.HaScreen)

	// Milkyway uses stronger gradient removal and no trail rejection (phone, wide field).
	assert.Greater(t, mw.BackgroundDegree, deep.BackgroundDegree)
	assert.False(t, mw.Grade.RejectTrails)

	// Planetary carries lucky-imaging settings.
	assert.Greater(t, pl.Planetary.BestPercent, 0)
	assert.True(t, pl.Planetary.Sharpen)
}
