package weather

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDewRisk(t *testing.T) {
	tests := []struct {
		name   string
		spread float64
		want   string
	}{
		{"dry", 8, "low"},
		{"boundary low", 5, "low"},
		{"marginal", 4, "moderate"},
		{"boundary moderate", 3, "moderate"},
		{"fogging", 1, "high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, dewRisk(tt.spread))
		})
	}
}

func TestTransparencyScore_BestToWorst(t *testing.T) {
	assert.Greater(t, transparencyScore(1), transparencyScore(8), "index 1 is the clearest sky")
	assert.InDelta(t, 1.0, transparencyScore(1), 0.13)
	assert.Equal(t, 0.0, transparencyScore(0), "out of range = unknown")
}

func TestSeeingArcsec(t *testing.T) {
	assert.Less(t, seeingArcsec(1), seeingArcsec(8), "higher index = worse (larger) seeing")
	assert.Equal(t, 0.0, seeingArcsec(99), "out of range = unknown")
}

func TestHourVerdict_CloudDominates(t *testing.T) {
	clear := hourVerdict(Hour{CloudPct: 0, Transparency: 1, SeeingArcsec: 0.6, HumidityPct: 50})
	cloudy := hourVerdict(Hour{CloudPct: 90, Transparency: 1, SeeingArcsec: 0.6, HumidityPct: 50})
	assert.Greater(t, clear, 80.0)
	assert.Less(t, cloudy, 20.0, "thick cloud crushes the verdict regardless of seeing/transparency")
}

func TestBestWindow_PicksGoodRun(t *testing.T) {
	hr := int64(3600 * 1000)
	hours := []Hour{
		{TMs: 0 * hr, Verdict: 20}, // cloudy
		{TMs: 1 * hr, Verdict: 75}, // ┐ good run
		{TMs: 2 * hr, Verdict: 85}, // │
		{TMs: 3 * hr, Verdict: 70}, // ┘
		{TMs: 4 * hr, Verdict: 30}, // cloudy again
	}
	w := BestWindow(hours, 0, 4*hr)
	require.NotNil(t, w)
	assert.Equal(t, 1*hr, w.StartMs)
	assert.Equal(t, 3*hr, w.EndMs)
	assert.Greater(t, w.Verdict, 60.0)
}

func TestBestWindow_NoHoursInRange(t *testing.T) {
	assert.Nil(t, BestWindow([]Hour{{TMs: 100, Verdict: 90}}, 1000, 2000))
}

func TestAuroraChance_ByLatitude(t *testing.T) {
	assert.Equal(t, "unlikely", auroraChance(2, 45), "mid-latitude, quiet")
	assert.Equal(t, "likely", auroraChance(5, 75), "high latitude, active")
}

func TestStTimeMs(t *testing.T) {
	base, ok := stTimeMs("2026063018", 0)
	require.True(t, ok)
	plus3, ok := stTimeMs("2026063018", 3)
	require.True(t, ok)
	assert.Equal(t, int64(3*3600*1000), plus3-base, "timepoint is an hour offset from the init run")
	assert.Equal(t, "2026-06-30T18:00:00Z", time.UnixMilli(base).UTC().Format(time.RFC3339), "init parses as UTC")
}
