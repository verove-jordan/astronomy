package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/weather"
)

func TestNearestHour(t *testing.T) {
	const hour = int64(3_600_000)
	hours := []weather.Hour{
		{TMs: 100 * hour, CloudPct: 10},
		{TMs: 101 * hour, CloudPct: 20},
		{TMs: 102 * hour, CloudPct: 30},
	}
	tests := []struct {
		name      string
		hours     []weather.Hour
		nowMs     int64
		wantOK    bool
		wantCloud float64
		why       string
	}{
		{
			name: "picks the sample bracketing now", hours: hours,
			nowMs: 101*hour + hour/4, wantOK: true, wantCloud: 20,
		},
		{
			// A cached forecast can start in the past; the first hour would then be badly wrong.
			name: "a forecast that starts in the past still answers for now", hours: hours,
			nowMs: 102*hour - 60_000, wantOK: true, wantCloud: 30,
			why: "the tooltip describes the sky NOW, not the start of the timeline",
		},
		{
			name: "before the timeline clamps to its first hour", hours: hours,
			nowMs: 90 * hour, wantOK: true, wantCloud: 10,
		},
		{
			name: "after the timeline clamps to its last hour", hours: hours,
			nowMs: 200 * hour, wantOK: true, wantCloud: 30,
		},
		{name: "no hours means no weather", hours: nil, nowMs: 100 * hour, wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := nearestHour(tt.hours, tt.nowMs)
			require.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.wantCloud, got.CloudPct, tt.why)
			}
		})
	}
}

// A nil provider must simply mean "no weather", never a panic: the hover endpoint is called
// constantly and an engine started without a weather provider still has to answer.
func TestCachedPointWeather_NoProvider(t *testing.T) {
	s := &Server{}
	_, ok := s.cachedPointWeather(48.3, 2.7)
	assert.False(t, ok)
}
