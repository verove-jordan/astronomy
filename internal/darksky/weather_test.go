package darksky

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/elevation"
	"github.com/verove-jordan/astronomy/internal/lightpollution"
	"github.com/verove-jordan/astronomy/internal/weather"
)

// fakeWeather forecasts by latitude: everything at or north of clearAbove has a good night, everything
// south of it is overcast. A threshold rather than exact coordinates, because the area pass samples a
// lattice of its own choosing and must be able to land on the clear half without matching a candidate
// exactly. unknown makes every scan come back with no data, standing in for a dead upstream.
type fakeWeather struct {
	clearAbove float64
	unknown    bool
	warn       string
	scans      int
	points     []int // how many points each scan asked for, in order
	detail     []bool
}

func (f *fakeWeather) NightScan(_ context.Context, pts []weather.Point, startMs, endMs int64, o weather.NightOpts) ([]weather.NightOutlook, string) {
	f.scans++
	f.points = append(f.points, len(pts))
	f.detail = append(f.detail, o.Detailed)

	out := make([]weather.NightOutlook, len(pts))
	for i, pt := range pts {
		out[i] = weather.NightOutlook{StartMs: startMs, EndMs: endMs}
		if f.unknown {
			out[i].Flags = []string{weather.FlagBeyondHorizon}
			continue
		}
		out[i].SampleHours = 8
		if pt.Lat >= f.clearAbove {
			out[i].Score, out[i].CloudPct, out[i].ClearHours = 92, 5, 8
		} else {
			out[i].Score, out[i].CloudPct, out[i].ClearHours = 8, 95, 0
		}
	}
	return out, f.warn
}

func (f *fakeWeather) NightConfidence(_ context.Context, _, _ float64, _, _ int64) *weather.Confidence {
	return &weather.Confidence{Model: "fake", Members: 10, ClearMembers: 7, Agreement: 0.7}
}

func weatherQuery(weight float64) Query {
	return Query{Bbox: area(), MaxBortle: 5, Limit: 10, Weather: true, WeatherWeight: weight}
}

// The point of the whole feature: a slightly brighter spot with a clear night must be able to beat a
// darker one under cloud.
func TestFind_WeatherLiftsTheClearSiteOverTheDarkerOne(t *testing.T) {
	cells := []lightpollution.Cell{
		{Lat: 1, Lon: 1, SQM: 21.9, Bortle: 1}, // darkest, but clouded over
		{Lat: 4, Lon: 4, SQM: 21.3, Bortle: 3}, // brighter, and clear
	}
	wx := &fakeWeather{clearAbove: 3}
	f := New(fakeScanner{cells}, nil, 4000, 10, WithWeather(wx, 16))

	res := f.Find(context.Background(), weatherQuery(0.3))

	require.Equal(t, 2, res.Count)
	assert.Equal(t, 21.3, res.Candidates[0].SQM, "the clear site must win")
	assert.True(t, res.Candidates[0].Sub.WeatherKnown)
	assert.Greater(t, res.Candidates[0].Sub.Weather, res.Candidates[1].Sub.Weather)
}

// Same data, weather switched off: the ranking must be the historical darkness-first order.
func TestFind_WeatherOffKeepsTheTerrainOrder(t *testing.T) {
	cells := []lightpollution.Cell{
		{Lat: 1, Lon: 1, SQM: 21.9, Bortle: 1},
		{Lat: 4, Lon: 4, SQM: 21.3, Bortle: 3},
	}
	wx := &fakeWeather{clearAbove: 3}
	f := New(fakeScanner{cells}, nil, 4000, 10, WithWeather(wx, 16))

	res := f.Find(context.Background(), Query{Bbox: area(), MaxBortle: 5, Limit: 10})

	require.Equal(t, 2, res.Count)
	assert.Equal(t, 21.9, res.Candidates[0].SQM)
	assert.Zero(t, wx.scans, "weather off must not spend a forecast call")
	assert.Nil(t, res.Night)
	assert.Zero(t, res.WeatherWeight)
	for _, c := range res.Candidates {
		assert.False(t, c.Sub.WeatherKnown)
		assert.Nil(t, c.Weather)
	}
}

// A dead forecast must cost the user their weather column, not their results.
func TestFind_WeatherUnavailableFallsBackToTerrain(t *testing.T) {
	cells := []lightpollution.Cell{
		{Lat: 1, Lon: 1, SQM: 21.9, Bortle: 1},
		{Lat: 4, Lon: 4, SQM: 21.3, Bortle: 3},
	}
	wx := &fakeWeather{unknown: true, warn: "weather forecast unavailable — spots are ranked on darkness and horizon only"}
	f := New(fakeScanner{cells}, nil, 4000, 10, WithWeather(wx, 16))

	res := f.Find(context.Background(), weatherQuery(0.3))

	require.Equal(t, 2, res.Count)
	assert.Equal(t, 21.9, res.Candidates[0].SQM, "no forecast → the darkest site is still the answer")
	assert.Contains(t, res.Warnings, wx.warn)
	for _, c := range res.Candidates {
		assert.False(t, c.Sub.WeatherKnown)
		assert.InDelta(t, c.Sub.Darkness*0.6+c.Sub.Openness*0.4, c.Score, 0.005)
	}
}

// A failed DETAIL pass after a successful area pass must not claim the ranking ignored the weather —
// the spots on screen are ranked on the area forecast, and saying otherwise misreads the whole list.
func TestFind_DetailFailureKeepsTheAreaForecastAndSaysSo(t *testing.T) {
	cells := []lightpollution.Cell{
		{Lat: 1, Lon: 1, SQM: 21.9, Bortle: 1},
		{Lat: 4, Lon: 4, SQM: 21.3, Bortle: 3},
	}
	wx := &detailFailingWeather{fakeWeather: fakeWeather{clearAbove: 3}}
	f := New(fakeScanner{cells}, nil, 4000, 10, WithWeather(wx, 16))

	res := f.Find(context.Background(), weatherQuery(0.3))

	require.Equal(t, 2, res.Count)
	assert.Equal(t, 21.3, res.Candidates[0].SQM, "the area forecast still decides the order")
	assert.True(t, res.Candidates[0].Sub.WeatherKnown)
	assert.Contains(t, res.Warnings, "detailed per-spot forecast unavailable — showing the area forecast instead")
	assert.NotContains(t, res.Warnings, "ranked on darkness and horizon only")
}

// When BOTH passes fail, only the scan's own warning should appear — a second message about missing
// detail adds nothing to "there is no forecast".
func TestFind_TotalWeatherFailureWarnsOnce(t *testing.T) {
	cells := []lightpollution.Cell{{Lat: 1, Lon: 1, SQM: 21.9, Bortle: 1}}
	wx := &fakeWeather{unknown: true, warn: "weather forecast unavailable — spots are ranked on darkness and horizon only"}
	f := New(fakeScanner{cells}, nil, 4000, 10, WithWeather(wx, 16))

	res := f.Find(context.Background(), weatherQuery(0.3))

	assert.Equal(t, []string{wx.warn}, res.Warnings)
}

type detailFailingWeather struct{ fakeWeather }

func (f *detailFailingWeather) NightScan(ctx context.Context, pts []weather.Point, startMs, endMs int64, o weather.NightOpts) ([]weather.NightOutlook, string) {
	if o.Detailed {
		unknown := make([]weather.NightOutlook, len(pts))
		for i := range unknown {
			unknown[i] = weather.NightOutlook{Flags: []string{weather.FlagBeyondHorizon}}
		}
		return unknown, "weather forecast unavailable — spots are ranked on darkness and horizon only"
	}
	return f.fakeWeather.NightScan(ctx, pts, startMs, endMs, o)
}

// No finder at all is still a finder: nil weather is the pre-feature configuration.
func TestFind_NoWeatherProviderIsTheHistoricalFinder(t *testing.T) {
	cells := []lightpollution.Cell{{Lat: 1, Lon: 1, SQM: 21.5, Bortle: 2}}
	f := New(fakeScanner{cells}, nil, 4000, 10)

	res := f.Find(context.Background(), weatherQuery(0.5))

	require.Equal(t, 1, res.Count)
	assert.Nil(t, res.Night)
	assert.Nil(t, res.Candidates[0].Weather)
}

// Two calls per search, whatever the area: a coarse pass over the box and a detailed pass over the
// finalists. This is the guarantee that makes the feature affordable on a free quota.
func TestFind_SpendsExactlyTwoForecastCalls(t *testing.T) {
	cells := make([]lightpollution.Cell, 0, 60)
	for i := 0; i < 60; i++ {
		cells = append(cells, lightpollution.Cell{
			Lat: 1 + float64(i)*0.05, Lon: 1 + float64(i)*0.05,
			SQM: 21.0 + float64(i%7)*0.1, Bortle: 3,
		})
	}
	wx := &fakeWeather{clearAbove: 2}
	f := New(fakeScanner{cells}, nil, 4000, 10, WithWeather(wx, 36))

	res := f.Find(context.Background(), weatherQuery(0.3))

	require.Equal(t, 10, res.Count)
	assert.Equal(t, 2, wx.scans)
	assert.Equal(t, 36, wx.points[0], "the area pass samples the probe budget, not the candidate count")
	assert.Equal(t, 10, wx.points[1], "the precise pass covers exactly the returned spots")
	assert.Equal(t, []bool{false, true}, wx.detail, "only the finalists pay for the wind profile")
}

// The precise pass must hand Open-Meteo the elevation the horizon step resolved, so temperature and
// dew point are downscaled to the real spot rather than to the model's smoothed terrain.
func TestFind_PrecisePassCarriesTheResolvedElevation(t *testing.T) {
	cells := []lightpollution.Cell{{Lat: 1, Lon: 1, SQM: 21.5, Bortle: 2}}
	wx := &elevationRecordingWeather{fakeWeather: fakeWeather{clearAbove: 0}}
	fh := fakeHorizon{open: map[[2]float64]float64{{1, 1}: 80}}
	f := New(fakeScanner{cells}, fh, 4000, 10, WithWeather(wx, 16))

	res := f.Find(context.Background(), Query{Bbox: area(), MaxBortle: 5, Limit: 10, Horizon: true, Weather: true})

	require.Equal(t, 1, res.Count)
	require.Len(t, wx.elevations, 1)
	assert.Equal(t, []float64{500}, wx.elevations[0], "fakeHorizon reports 500 m")
}

type elevationRecordingWeather struct {
	fakeWeather
	elevations [][]float64
}

func (f *elevationRecordingWeather) NightScan(ctx context.Context, pts []weather.Point, startMs, endMs int64, o weather.NightOpts) ([]weather.NightOutlook, string) {
	if o.Detailed {
		got := make([]float64, len(pts))
		for i, pt := range pts {
			got[i] = pt.ElevationM
		}
		f.elevations = append(f.elevations, got)
	}
	return f.fakeWeather.NightScan(ctx, pts, startMs, endMs, o)
}

func TestFind_NightAndConfidenceAreReported(t *testing.T) {
	cells := []lightpollution.Cell{{Lat: 1, Lon: 1, SQM: 21.5, Bortle: 2}}
	wx := &fakeWeather{clearAbove: 0}
	f := New(fakeScanner{cells}, nil, 4000, 10, WithWeather(wx, 16))

	res := f.Find(context.Background(), Query{Bbox: area(), MaxBortle: 5, Limit: 5, Weather: true, NightIndex: 2})

	require.NotNil(t, res.Night)
	assert.Equal(t, 2, res.Night.Index)
	assert.Greater(t, res.Night.EndMs, res.Night.StartMs)
	assert.Greater(t, res.Night.DarkHours, 0.0)
	assert.GreaterOrEqual(t, res.Night.MoonIllum, 0.0)
	assert.LessOrEqual(t, res.Night.MoonIllum, 1.0)
	assert.LessOrEqual(t, res.Night.MoonUpHours, res.Night.DarkHours+1e-6)
	require.NotNil(t, res.Night.Confidence)
	assert.Equal(t, 0.7, res.Night.Confidence.Agreement)
}

func TestNightWindow_LaterNightsDoNotRepeat(t *testing.T) {
	var prevStart int64
	for i := 0; i < 7; i++ {
		w := nightWindow(i, 43.6, 5.1)

		assert.True(t, w.End.After(w.Start))
		if i > 0 {
			gap := w.Start.UnixMilli() - prevStart
			assert.Greater(t, gap, int64(20*3600*1000), "night %d must be a different night", i)
			assert.Less(t, gap, int64(28*3600*1000), "night %d must be the very next one", i)
		}
		prevStart = w.Start.UnixMilli()
	}
}

func TestProbePoints_FitsTheBudgetAndCoversTheBox(t *testing.T) {
	tests := []struct {
		name string
		bbox lightpollution.Bbox
		max  int
	}{
		{name: "square", bbox: lightpollution.Bbox{MinLat: 44, MinLon: 5, MaxLat: 45, MaxLon: 6}, max: 160},
		{name: "wide", bbox: lightpollution.Bbox{MinLat: 44, MinLon: 0, MaxLat: 44.2, MaxLon: 10}, max: 160},
		{name: "tall", bbox: lightpollution.Bbox{MinLat: 40, MinLon: 5, MaxLat: 50, MaxLon: 5.2}, max: 160},
		{name: "tiny budget", bbox: lightpollution.Bbox{MinLat: 44, MinLon: 5, MaxLat: 45, MaxLon: 6}, max: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pts, set := probePoints(tt.bbox, tt.max)

			assert.LessOrEqual(t, len(pts), tt.max, "the budget is the quota — it must not be exceeded")
			assert.Equal(t, set.nx*set.ny, len(pts))
			for _, p := range pts {
				assert.GreaterOrEqual(t, p.Lat, tt.bbox.MinLat-1e-9)
				assert.LessOrEqual(t, p.Lat, tt.bbox.MaxLat+1e-9)
				assert.GreaterOrEqual(t, p.Lon, tt.bbox.MinLon-1e-9)
				assert.LessOrEqual(t, p.Lon, tt.bbox.MaxLon+1e-9)
			}
			// The corners must be sampled, or the edges of a drawn box would carry no forecast.
			assert.Equal(t, tt.bbox.MinLat, pts[0].Lat)
			assert.Equal(t, tt.bbox.MinLon, pts[0].Lon)
			assert.InDelta(t, tt.bbox.MaxLat, pts[len(pts)-1].Lat, 1e-9)
			assert.InDelta(t, tt.bbox.MaxLon, pts[len(pts)-1].Lon, 1e-9)
		})
	}
}

func TestProbeSet_NearestProbeWins(t *testing.T) {
	box := lightpollution.Bbox{MinLat: 44, MinLon: 5, MaxLat: 45, MaxLon: 6}
	pts, set := probePoints(box, 16)
	set.outlooks = make([]weather.NightOutlook, len(pts))
	for i := range set.outlooks {
		set.outlooks[i] = weather.NightOutlook{SampleHours: 8, Score: float64(i)}
	}

	for i, p := range pts {
		got := set.at(p.Lat, p.Lon)

		require.NotNil(t, got, "probe %d", i)
		assert.Equal(t, float64(i), got.Score, "a probe's own coordinates must return that probe")
	}
	// Outside the box the nearest edge probe is the honest answer, not nil.
	assert.NotNil(t, set.at(40, 0))
	assert.NotNil(t, set.at(89, 179))
}

func TestProbeSet_EmptyScanReturnsNothing(t *testing.T) {
	_, set := probePoints(lightpollution.Bbox{MinLat: 44, MinLon: 5, MaxLat: 45, MaxLon: 6}, 16)

	assert.Nil(t, set.at(44.5, 5.5))
}

func TestBlend_WeatherTakesItsShareOffTheTop(t *testing.T) {
	sub := SubScores{Darkness: 1, Openness: 0.5, Weather: 0, WeatherKnown: true}
	sc := ScoreConfig{DarkWeight: 0.6}

	terrain := blend(SubScores{Darkness: 1, Openness: 0.5}, sc)
	half := blend(sub, ScoreConfig{DarkWeight: 0.6, WeatherWeight: 0.5})

	assert.InDelta(t, 0.6*1+0.4*0.5, terrain, 1e-9)
	assert.InDelta(t, 0.5*terrain, half, 1e-9, "a hopeless sky halves the score at weight 0.5")
}

// The historical blend must survive untouched whenever weather is absent, whatever the weight says.
func TestScoreCandidate_WeatherOffIsHistoricalBlend(t *testing.T) {
	c := Candidate{SQM: 21.0, Horizon: &elevation.Horizon{OpennessPct: 50}}
	darkNorm := (21.0 - 18.0) / (22.0 - 18.0)
	want := 0.6*darkNorm + 0.4*0.5

	assert.InDelta(t, want, scoreCandidate(c, defaultScoreConfig()), 1e-9)
	assert.InDelta(t, want, scoreCandidate(c, ScoreConfig{DarkWeight: 0.6, WeatherWeight: 0.5}), 1e-9,
		"a weight with no forecast behind it must not move the score")

	unknown := c
	unknown.Weather = &weather.NightOutlook{} // SampleHours 0 → no data
	assert.InDelta(t, want, scoreCandidate(unknown, ScoreConfig{DarkWeight: 0.6, WeatherWeight: 0.5}), 1e-9)
}

func TestScoreFor_RequestWeightIsClamped(t *testing.T) {
	f := New(fakeScanner{}, nil, 4000, 10, WithScore(ScoreConfig{DarkWeight: 0.6, WeatherWeight: 0.3}))

	tests := []struct {
		name string
		q    Query
		want float64
	}{
		{name: "weather off", q: Query{}, want: 0},
		{name: "configured default", q: Query{Weather: true}, want: 0.3},
		{name: "request override", q: Query{Weather: true, WeatherWeight: 0.55}, want: 0.55},
		{name: "over the ceiling", q: Query{Weather: true, WeatherWeight: 3}, want: maxWeatherWeight},
		{name: "negative ignored", q: Query{Weather: true, WeatherWeight: -1}, want: 0.3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, f.scoreFor(tt.q).WeatherWeight)
		})
	}
}
