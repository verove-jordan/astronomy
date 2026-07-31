package mode

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseMode(t *testing.T) {
	for _, s := range []string{"deepsky", "Nebula", "MILKYWAY", "planetary", "comet", "mosaic", "SUN"} {
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

	// Comet reuses the deepsky tuning but is tagged Comet and keeps StarNet on (for the star layer).
	com := For(Comet)
	assert.Equal(t, Comet, com.Mode)
	assert.Equal(t, Mono, com.Color)
	assert.Greater(t, com.StarReduce, 0.0)
	assert.False(t, com.Supervise)

	// Chroma NR defaults (anti-drift pins): the fine pass keeps its legacy radius, and the coarse
	// background-only pass ships on for the colour modes — the large sky-mottle fix depends on it.
	assert.Equal(t, 6, deep.ChromaSmoothPx)
	assert.Equal(t, 24, deep.ChromaBgSmoothPx)
	assert.Equal(t, 6, neb.ChromaSmoothPx)
	assert.Equal(t, 24, neb.ChromaBgSmoothPx)

	// Photometric cross-session normalization ships ON for the deep-sky modes (anti-drift pin): the
	// flat-curve meta seed + widened clamp fixed the mixed-gain mis-measure that had it disabled, and
	// it only ever engages on multi-group channels.
	assert.True(t, deep.PhotomNorm)
	assert.True(t, neb.PhotomNorm)
}
