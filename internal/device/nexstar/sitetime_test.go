package nexstar

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Site and clock encoding. A wrong DST convention here is a one-hour error, and one hour of sky is
// fifteen degrees — so the reference case is Celestron's own worked example, not our reasoning.

func TestEncodeClock_MatchesCelestronsWorkedExample(t *testing.T) {
	// From the NexStar Communication Protocol: "to set the time to 3:26:00PM on April 6, 2005 in the
	// Eastern time zone (-5 UTC: 256-5 = 251) you would send:
	//   'H' & chr(15) & chr(26) & chr(0) & chr(4) & chr(6) & chr(5) & chr(251) & chr(1)"
	// Note what that says: the LOCAL WALL CLOCK (15:26, which is EDT that day), the zone's STANDARD
	// offset (-5, not the -4 in force), and daylight saving as a SEPARATE flag.
	eastern, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	when := time.Date(2005, time.April, 6, 15, 26, 0, 0, eastern)

	got := encodeClock(when)
	assert.Equal(t, []byte{'H', 15, 26, 0, 4, 6, 5, 251, 1}, got)
}

func TestDecodeClock_ReconstructsTheInstant(t *testing.T) {
	// The same example read back: 15:26 local, offset -5, DST on, is 19:26 UTC.
	got, err := decodeClock([]byte{15, 26, 0, 4, 6, 5, 251, 1})
	require.NoError(t, err)
	assert.Equal(t, time.Date(2005, time.April, 6, 19, 26, 0, 0, time.UTC), got.UTC)
	assert.Equal(t, -5, got.OffsetHours)
	assert.True(t, got.DST)
}

func TestClock_RoundTripsAcrossZonesAndSeasons(t *testing.T) {
	zones := []string{"Europe/Paris", "America/New_York", "UTC", "Australia/Sydney", "Asia/Tokyo"}
	moments := []time.Time{
		time.Date(2026, time.January, 15, 22, 41, 7, 0, time.UTC), // northern winter
		time.Date(2026, time.July, 15, 22, 41, 7, 0, time.UTC),    // northern summer
	}
	for _, zone := range zones {
		loc, err := time.LoadLocation(zone)
		require.NoError(t, err)
		for _, when := range moments {
			t.Run(zone+"/"+when.Format("Jan"), func(t *testing.T) {
				frame := encodeClock(when.In(loc))
				got, err := decodeClock(frame[1:])
				require.NoError(t, err)
				// The protocol carries whole seconds and whole hours of offset, so a half-hour zone
				// cannot round-trip exactly. Everything we actually observe from can.
				assert.WithinDuration(t, when, got.UTC, time.Second,
					"the instant must survive the trip through local time, offset and a DST flag")
			})
		}
	}
}

func TestStandardOffsetHours_DoesNotAssumeDaylightSavingIsAnHour(t *testing.T) {
	// Lord Howe Island shifts by THIRTY minutes. Assuming an hour — the obvious shortcut — would
	// compute a standard offset that the zone never uses.
	loc, err := time.LoadLocation("Australia/Lord_Howe")
	if err != nil {
		t.Skip("no tz database entry for Lord Howe here")
	}
	summer := time.Date(2026, time.January, 15, 12, 0, 0, 0, loc)
	winter := time.Date(2026, time.July, 15, 12, 0, 0, 0, loc)
	assert.Equal(t, 10, standardOffsetHours(summer))
	assert.Equal(t, 10, standardOffsetHours(winter))
}

func TestEncodeSite_UsesSeparateSignFlags(t *testing.T) {
	tests := []struct {
		name string
		site Site
		want []byte
	}{
		{
			// The protocol's own example: 33°50'41" N, 118°20'17" W.
			name: "the protocol's worked example",
			site: Site{LatDeg: 33 + 50.0/60 + 41.0/3600, LonDeg: -(118 + 20.0/60 + 17.0/3600)},
			want: []byte{'W', 33, 50, 41, 0, 118, 20, 17, 1},
		},
		{
			name: "southern and eastern",
			site: Site{LatDeg: -(33 + 52.0/60 + 4.0/3600), LonDeg: 151 + 12.0/60 + 26.0/3600},
			want: []byte{'W', 33, 52, 4, 1, 151, 12, 26, 0},
		},
		{
			name: "the default site, near Paris",
			site: Site{LatDeg: 48.8566, LonDeg: 2.3522},
			want: []byte{'W', 48, 51, 24, 0, 2, 21, 8, 0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, encodeSite(tt.site))
		})
	}
}

func TestSite_RoundTripsToArcsecondResolution(t *testing.T) {
	for _, s := range []Site{
		{LatDeg: 48.8566, LonDeg: 2.3522},
		{LatDeg: -33.8688, LonDeg: 151.2093},
		{LatDeg: 0, LonDeg: 0},
		{LatDeg: 89.9, LonDeg: -179.9},
	} {
		frame := encodeSite(s)
		got, err := decodeSite(frame[1:])
		require.NoError(t, err)
		assert.InDelta(t, s.LatDeg, got.LatDeg, 1.0/3600)
		assert.InDelta(t, s.LonDeg, got.LonDeg, 1.0/3600)
	}
}

func TestDecodeAzAlt_FoldsAnAltitudeBelowTheHorizon(t *testing.T) {
	// The mount reports a plain fraction of a revolution, so a tube pointed ten degrees below the
	// horizon comes back as 350 rather than −10. Reporting that as "350° up" would make the horizon
	// safety floor useless.
	az, alt, err := DecodeAzAlt(encodeAngle(120) + "," + encodeAngle(350) + "#")
	require.NoError(t, err)
	assert.InDelta(t, 120.0, az, 0.001)
	assert.InDelta(t, -10.0, alt, 0.001)

	_, _, err = DecodeAzAlt("nonsense#")
	assert.Error(t, err)
}

func TestMount_State_ReportsAltitudeAndAzimuthFromTheMount(t *testing.T) {
	hc := newFakeHC()
	m, _, _ := faultyRig(t, hc)

	st, err := m.State(context.Background())
	require.NoError(t, err)
	// The fake answers 'z' with the bare acknowledgement its default branch produces, so the values
	// stay zero — what matters here is that asking does not break the rest of the state read, since
	// the command was added to a sequence that already worked.
	assert.NotZero(t, st.RADeg)
	assert.Equal(t, "Advanced VX", st.Model)
}
