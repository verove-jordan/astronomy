package weather

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nightStart is a fixed instant so tests never depend on the wall clock.
var nightStart = time.Date(2026, 8, 4, 21, 0, 0, 0, time.UTC)

// hoursOf builds a run of consecutive hourly samples from a template, applying tweak per index.
func hoursOf(n int, tweak func(i int, h *Hour)) []Hour {
	out := make([]Hour, n)
	for i := range out {
		h := Hour{
			TMs:         nightStart.Add(time.Duration(i) * time.Hour).UnixMilli(),
			HumidityPct: 60,
			TempC:       12,
			DewPointC:   4,
			WindKmh:     6,
		}
		if tweak != nil {
			tweak(i, &h)
		}
		h.DewSpreadC = round1(h.TempC - h.DewPointC)
		h.DewRisk = dewRisk(h.DewSpreadC)
		h.Verdict = hourVerdict(h)
		out[i] = h
	}
	return out
}

func nightRange(hours []Hour) (int64, int64) {
	return hours[0].TMs, hours[len(hours)-1].TMs
}

func TestScoreNight_CloudDominatesTheScore(t *testing.T) {
	tests := []struct {
		name    string
		cloud   float64
		wantMin float64
		wantMax float64
	}{
		{name: "clear", cloud: 0, wantMin: 90, wantMax: 100},
		{name: "broken", cloud: 50, wantMin: 30, wantMax: 60},
		{name: "overcast", cloud: 100, wantMin: 0, wantMax: 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hours := hoursOf(8, func(_ int, h *Hour) { h.CloudPct = tt.cloud })
			start, end := nightRange(hours)

			got := ScoreNight(hours, NightInputs{StartMs: start, EndMs: end})

			assert.GreaterOrEqual(t, got.Score, tt.wantMin)
			assert.LessOrEqual(t, got.Score, tt.wantMax)
			assert.Equal(t, 8, got.SampleHours)
			assert.True(t, got.Known())
		})
	}
}

// Low cloud is a hard stop and high cloud is not, so identical total cover must not score identically.
func TestScoreNight_LowCloudCostsMoreThanHighCloud(t *testing.T) {
	low := hoursOf(8, func(_ int, h *Hour) { h.CloudPct, h.CloudLow = 60, 60 })
	high := hoursOf(8, func(_ int, h *Hour) { h.CloudPct, h.CloudHigh = 60, 60 })
	start, end := nightRange(low)

	lowScore := ScoreNight(low, NightInputs{StartMs: start, EndMs: end})
	highScore := ScoreNight(high, NightInputs{StartMs: start, EndMs: end})

	assert.Less(t, lowScore.Score, highScore.Score, "a stratus deck must rank below cirrus at the same cover")
	assert.Equal(t, 60.0, lowScore.CloudLowPct)
	assert.Equal(t, 60.0, highScore.CloudHighPct)
}

func TestScoreNight_MoonWeightingFavoursTheMoonlessHalf(t *testing.T) {
	// Clear early, cloudy late.
	hours := hoursOf(8, func(i int, h *Hour) {
		if i >= 4 {
			h.CloudPct = 90
		}
	})
	start, end := nightRange(hours)
	midMs := hours[4].TMs

	flat := ScoreNight(hours, NightInputs{StartMs: start, EndMs: end})
	// A Moon that spoils only the first (clear) half must drag the night's verdict down.
	moonEarly := ScoreNight(hours, NightInputs{StartMs: start, EndMs: end, Moon: func(ms int64) float64 {
		if ms < midMs {
			return 0.2
		}
		return 1
	}})
	// A Moon that spoils only the cloudy half changes almost nothing worth having anyway.
	moonLate := ScoreNight(hours, NightInputs{StartMs: start, EndMs: end, Moon: func(ms int64) float64 {
		if ms >= midMs {
			return 0.2
		}
		return 1
	}})

	assert.Less(t, moonEarly.Score, flat.Score)
	assert.Greater(t, moonLate.Score, flat.Score)
}

func TestScoreNight_UnknownMetricsInventNoPenalty(t *testing.T) {
	// A feed that supplied nothing but cloud must not be punished for the fields it left at zero.
	bare := make([]Hour, 6)
	for i := range bare {
		bare[i] = Hour{TMs: nightStart.Add(time.Duration(i) * time.Hour).UnixMilli()}
		bare[i].Verdict = hourVerdict(bare[i])
	}
	start, end := nightRange(bare)

	got := ScoreNight(bare, NightInputs{StartMs: start, EndMs: end})

	assert.Equal(t, 100.0, got.Score, "no cloud and no other data means nothing is known to be wrong")
	assert.Zero(t, got.SeeingArcsec)
	assert.Zero(t, got.Transparency)
}

func TestScoreNight_OutsideForecastHorizonIsUnknownNotZero(t *testing.T) {
	hours := hoursOf(4, nil)

	got := ScoreNight(hours, NightInputs{
		StartMs: nightStart.AddDate(0, 0, 9).UnixMilli(),
		EndMs:   nightStart.AddDate(0, 0, 9).Add(6 * time.Hour).UnixMilli(),
	})

	assert.False(t, got.Known(), "a night with no samples must not masquerade as a bad night")
	assert.Contains(t, got.Flags, FlagBeyondHorizon)
	assert.Zero(t, got.SampleHours)
}

func TestScoreNight_ClearHoursMatchTheBestWindow(t *testing.T) {
	hours := hoursOf(8, func(i int, h *Hour) {
		if i < 3 {
			h.CloudPct = 95
		}
	})
	start, end := nightRange(hours)

	got := ScoreNight(hours, NightInputs{StartMs: start, EndMs: end})

	assert.Equal(t, 5.0, got.ClearHours)
	require.NotNil(t, got.Best)
	assert.Equal(t, hours[3].TMs, got.Best.StartMs)
	assert.Equal(t, hours[7].TMs, got.Best.EndMs)
}

func TestScoreNight_AboveInversionForgivesTheDeckBelow(t *testing.T) {
	// A stratus deck: total cover is all low cloud, capped by a 700 m boundary layer over a 200 m plain.
	hours := hoursOf(8, func(_ int, h *Hour) {
		h.CloudPct, h.CloudLow, h.CloudMid, h.CloudHigh = 95, 95, 0, 10
		h.BLHeightM = 700
	})
	start, end := nightRange(hours)
	deck := DeckTop(200, hours)
	require.Equal(t, 900.0, deck)

	valley := ScoreNight(hours, NightInputs{StartMs: start, EndMs: end, SiteElevationM: 300, DeckTopM: deck})
	summit := ScoreNight(hours, NightInputs{StartMs: start, EndMs: end, SiteElevationM: 1600, DeckTopM: deck})

	assert.NotContains(t, valley.Flags, FlagAboveInversion)
	assert.Contains(t, summit.Flags, FlagAboveInversion)
	assert.Greater(t, summit.Score, valley.Score+50, "the summit is above the cloud, the valley is inside it")
}

func TestScoreNight_InversionBonusNeedsEvidence(t *testing.T) {
	tests := []struct {
		name      string
		elevation float64
		deckTop   float64
		cloudLow  float64
		blHeight  float64
	}{
		{name: "no elevation known", elevation: 0, deckTop: 900, cloudLow: 95, blHeight: 700},
		{name: "no deck top known", elevation: 1600, deckTop: 0, cloudLow: 95, blHeight: 0},
		{name: "not high enough", elevation: 950, deckTop: 900, cloudLow: 95, blHeight: 700},
		{name: "no deck to rise above", elevation: 1600, deckTop: 900, cloudLow: 10, blHeight: 700},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hours := hoursOf(6, func(_ int, h *Hour) {
				h.CloudPct, h.CloudLow, h.BLHeightM = tt.cloudLow, tt.cloudLow, tt.blHeight
			})
			start, end := nightRange(hours)

			got := ScoreNight(hours, NightInputs{
				StartMs: start, EndMs: end,
				SiteElevationM: tt.elevation, DeckTopM: tt.deckTop,
			})

			assert.NotContains(t, got.Flags, FlagAboveInversion)
		})
	}
}

func TestScoreNight_FlagsFogAndFrost(t *testing.T) {
	hours := hoursOf(6, func(_ int, h *Hour) {
		h.TempC, h.DewPointC, h.HumidityPct, h.WindKmh = 0, -0.5, 97, 2
	})
	start, end := nightRange(hours)

	got := ScoreNight(hours, NightInputs{StartMs: start, EndMs: end})

	assert.Contains(t, got.Flags, FlagFogRisk)
	assert.Contains(t, got.Flags, FlagFrost)
	assert.Equal(t, "high", got.DewRisk)
}

// The deck top is averaged over whatever hours report a boundary-layer depth. A minority of gaps is
// survivable — the scan pools hundreds of points — but once most of them are missing the mean says
// nothing, and inventing a deck would hand out an inversion bonus nobody earned.
func TestDeckTop_NeedsMostHoursToReportABoundaryLayer(t *testing.T) {
	blAt := func(n int, known func(i int) bool) []Hour {
		return hoursOf(n, func(i int, h *Hour) {
			if known(i) {
				h.BLHeightM = 500
			}
		})
	}

	tests := []struct {
		name  string
		floor float64
		hours []Hour
		want  float64
	}{
		{name: "all known", floor: 300, hours: blAt(4, func(int) bool { return true }), want: 800},
		{name: "one gap", floor: 300, hours: blAt(4, func(i int) bool { return i != 2 }), want: 800},
		{name: "mostly missing", floor: 300, hours: blAt(4, func(i int) bool { return i == 0 }), want: 0},
		{name: "none known", floor: 300, hours: blAt(4, func(int) bool { return false }), want: 0},
		{name: "no lowland floor", floor: 0, hours: blAt(4, func(int) bool { return true }), want: 0},
		{name: "no hours", floor: 300, hours: nil, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, DeckTop(tt.floor, tt.hours))
		})
	}
}
