package skyplan

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
)

// The chart window and the dark window must always describe THE SAME night.
//
// They did not. computeNight recomputed its own anchor with astro.SolarMidnight(prm.At), which
// returns the anti-transit nearest the instant it is given — so from dawn until mid-afternoon it
// answers last night's. astro.NightWindow, which produces the dark window handed to computeNight,
// rolls forward past a night that has already ended. Between those two behaviours the page framed
// its chart, its hour filter and its "night of" badge on the night that ended that morning while
// the best-clear-window described the night ahead.
//
// Paris in early September is the case that was reported: at noon on the 3rd the panel offered the
// night of the 2nd.
func TestComputeNight_FramesTheSameNightAsTheDarkWindow(t *testing.T) {
	const (
		lat = 48.85 // Paris
		lon = 2.35
	)
	paris, err := time.LoadLocation("Europe/Paris")
	require.NoError(t, err)

	tests := []struct {
		name string
		at   time.Time
		// wantNightOf is the local calendar day the night STARTS on — the evening you go out.
		wantNightOf string
	}{
		{
			// The reported bug: UTC+2 midday. Solar midnight nearest this instant is 00:00 on the
			// 3rd, i.e. the night of the 2nd, which finished hours ago.
			name:        "midday plans the night that is coming, not the one that ended this morning",
			at:          time.Date(2026, 9, 3, 12, 0, 0, 0, paris),
			wantNightOf: "2026-09-03",
		},
		{
			name:        "late afternoon plans tonight",
			at:          time.Date(2026, 9, 3, 17, 30, 0, 0, paris),
			wantNightOf: "2026-09-03",
		},
		{
			name:        "evening, already dark, plans tonight",
			at:          time.Date(2026, 9, 3, 22, 0, 0, 0, paris),
			wantNightOf: "2026-09-03",
		},
		{
			// After midnight you are INSIDE the night that began yesterday evening; planning must
			// stay on it rather than skip to the next one.
			name:        "after midnight stays on the night in progress",
			at:          time.Date(2026, 9, 4, 2, 0, 0, 0, paris),
			wantNightOf: "2026-09-03",
		},
		{
			// Just past dawn the night is genuinely over: roll forward.
			name:        "just after dawn moves to the night ahead",
			at:          time.Date(2026, 9, 4, 8, 0, 0, 0, paris),
			wantNightOf: "2026-09-04",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prm := Params{At: tt.at, Lat: lat, Lon: lon, Location: paris}
			dark := astro.NightWindow(tt.at, lat, lon, -18)
			nc := computeNight(prm, dark)

			require.True(t, nc.hasSunSet, "a September night at this latitude has a sunset")
			require.True(t, nc.hasSunRise, "a September night at this latitude has a sunrise")

			assert.Equal(t, tt.wantNightOf, nc.start.In(paris).Format("2006-01-02"),
				"the chart window must open on the evening of the planned night")

			// The two halves of the answer have to agree: dusk/dawn come from NightWindow, the chart
			// window from computeNight, and the panel filters its hours with the latter while the
			// best-clear-window is measured over the former.
			assert.True(t, !nc.start.After(dark.Start),
				"chart window opens at or before dusk (chart %s, dusk %s)", nc.start, dark.Start)
			assert.True(t, !nc.end.Before(dark.End),
				"chart window closes at or after dawn (chart %s, dawn %s)", nc.end, dark.End)
		})
	}
}
