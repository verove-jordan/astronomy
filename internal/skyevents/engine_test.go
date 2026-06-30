package skyevents

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/skyplan"
)

// TestComputeAugust2026 exercises the offline generators end-to-end for Paris and checks the two
// headline August-2026 events: the 12 August total solar eclipse and the Perseids peak.
func TestComputeAugust2026(t *testing.T) {
	eng := &Engine{} // offline categories only — no network feeds used
	prm := Params{
		From:     time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		To:       time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC),
		Lat:      48.8566,
		Lon:      2.3522,
		Optics:   skyplan.Optics{ApertureMM: 100, FocalMM: 740, PixelUm: 3.8, SensorWpx: 4656, SensorHpx: 3520},
		Twilight: "astro",
		Location: time.UTC,
		Categories: map[Category]bool{
			CatEclipse: true, CatMeteor: true, CatPlanet: true, CatMoon: true, CatSeason: true,
		},
	}
	res, err := eng.Compute(context.Background(), prm)
	require.NoError(t, err)
	require.NotEmpty(t, res.Events)

	var solar, perseids *Event
	for i := range res.Events {
		e := &res.Events[i]
		switch {
		case e.Kind == "solar_eclipse":
			solar = e
		case e.Kind == "meteor_shower" && e.Subtype == "PER":
			perseids = e
		}
		// events are time-sorted and within the window
		assert.GreaterOrEqual(t, e.PeakUTCMs, prm.From.UnixMilli())
		assert.LessOrEqual(t, e.PeakUTCMs, prm.To.UnixMilli())
	}

	require.NotNil(t, solar, "the 12 Aug 2026 solar eclipse should be present")
	got := time.UnixMilli(solar.PeakUTCMs).UTC()
	assert.Equal(t, 2026, got.Year())
	assert.Equal(t, time.August, got.Month())
	assert.Equal(t, 12, got.Day())
	assert.Equal(t, "total", solar.Subtype)
	assert.Greater(t, solar.Score, 0, "the eclipse is visible (partial) from Paris → non-zero score")

	require.NotNil(t, perseids, "the Perseids peak should be present in August")
	assert.Greater(t, perseids.ZHR, 50.0)
	assert.Greater(t, perseids.Visibility.NakedEye, 0)
}
