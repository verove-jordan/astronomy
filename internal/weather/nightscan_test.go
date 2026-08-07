package weather

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nightUpstream is a fake Open-Meteo that answers a night-window multi-point request. It echoes the
// requested hour range so the assembled hours always fall inside the window under test, and makes each
// coordinate progressively cloudier so ordering is observable.
type nightUpstream struct {
	srv     *httptest.Server
	calls   atomic.Int32
	lastURL atomic.Value // string
	mode    atomic.Value // "ok" | "429"
}

func newNightUpstream(t *testing.T) *nightUpstream {
	t.Helper()
	u := &nightUpstream{}
	u.mode.Store("ok")
	u.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.calls.Add(1)
		u.lastURL.Store(r.URL.String())
		if u.mode.Load().(string) == "429" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":true,"reason":"Minutely API request limit exceeded."}`)
			return
		}
		q := r.URL.Query()
		times := hourRange(q.Get("start_hour"), q.Get("end_hour"))
		lats := strings.Split(q.Get("latitude"), ",")
		elevations := strings.Split(q.Get("elevation"), ",")

		objs := make([]string, len(lats))
		for i := range objs {
			elevation := "200"
			if q.Get("elevation") != "" && i < len(elevations) {
				elevation = elevations[i]
			}
			objs[i] = fmt.Sprintf(`{"latitude":0,"longitude":0,"elevation":%s,"hourly":{%s}}`,
				elevation, nightHourlyBody(times, float64(i*20)))
		}
		_, _ = io.WriteString(w, "["+strings.Join(objs, ",")+"]")
	}))
	t.Cleanup(u.srv.Close)
	return u
}

func (u *nightUpstream) query(t *testing.T) url.Values {
	t.Helper()
	raw, _ := u.lastURL.Load().(string)
	require.NotEmpty(t, raw, "no request was made")
	parsed, err := url.Parse(raw)
	require.NoError(t, err)
	return parsed.Query()
}

// hourRange lists the hourly timestamps Open-Meteo would return for a start_hour/end_hour pair.
func hourRange(start, end string) []string {
	from, err1 := time.Parse("2006-01-02T15:04", start)
	to, err2 := time.Parse("2006-01-02T15:04", end)
	if err1 != nil || err2 != nil || !to.After(from) {
		return nil
	}
	var out []string
	for t := from; !t.After(to); t = t.Add(time.Hour) {
		out = append(out, t.Format("2006-01-02T15:04"))
	}
	return out
}

// nightHourlyBody renders an hourly block whose cloud cover is `cloud` percent throughout.
func nightHourlyBody(times []string, cloud float64) string {
	series := func(name string, v float64) string {
		vals := make([]string, len(times))
		for i := range vals {
			vals[i] = fmt.Sprintf("%g", v)
		}
		return fmt.Sprintf(`"%s":[%s]`, name, strings.Join(vals, ","))
	}
	parts := []string{`"time":["` + strings.Join(times, `","`) + `"]`,
		series("cloud_cover", cloud), series("cloud_cover_low", cloud),
		series("cloud_cover_mid", 0), series("cloud_cover_high", 0),
		series("relative_humidity_2m", 60), series("dew_point_2m", 4),
		series("temperature_2m", 12), series("wind_speed_10m", 6),
		series("boundary_layer_height", 400),
	}
	return strings.Join(parts, ",")
}

// testNight returns a window in the near future, inside any sane forecast horizon.
func testNight() (int64, int64) {
	start := time.Now().UTC().Add(6 * time.Hour).Truncate(time.Hour)
	return start.UnixMilli(), start.Add(8 * time.Hour).UnixMilli()
}

func testPoints(n int) []Point {
	pts := make([]Point, n)
	for i := range pts {
		pts[i] = Point{Lat: 44 + float64(i)*0.1, Lon: 5 + float64(i)*0.1}
	}
	return pts
}

// The whole quota argument rests on asking for the night's hours only, so the wire format is a
// contract, not an implementation detail.
func TestNightScan_RequestsOnlyTheNightsHours(t *testing.T) {
	up := newNightUpstream(t)
	p := testProvider(t, up.srv.URL, "", "", "")
	startMs, endMs := testNight()

	out, warn := p.NightScan(context.Background(), testPoints(3), startMs, endMs, NightOpts{})
	require.Empty(t, warn)
	require.Len(t, out, 3)

	q := up.query(t)
	assert.Equal(t, omHourParam(startMs), q.Get("start_hour"))
	assert.Equal(t, omHourParam(endMs), q.Get("end_hour"))
	assert.Empty(t, q.Get("forecast_days"), "a day span would defeat the point of the hour window")
	assert.Equal(t, "44.000,44.100,44.200", q.Get("latitude"))
	for _, o := range out {
		assert.True(t, o.Known())
		assert.Equal(t, startMs, o.StartMs)
	}
}

func TestNightScan_DetailedAddsThePressureLevelWinds(t *testing.T) {
	tests := []struct {
		name     string
		detailed bool
		want     bool
	}{
		{name: "area probe stays cheap", detailed: false, want: false},
		{name: "finalists get the wind profile", detailed: true, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := newNightUpstream(t)
			p := testProvider(t, up.srv.URL, "", "", "")
			startMs, endMs := testNight()

			_, warn := p.NightScan(context.Background(), testPoints(2), startMs, endMs, NightOpts{Detailed: tt.detailed})
			require.Empty(t, warn)

			hourly := up.query(t).Get("hourly")
			assert.Contains(t, hourly, "cloud_cover_low")
			assert.Equal(t, tt.want, strings.Contains(hourly, "wind_speed_500hPa"))
			assert.Equal(t, tt.want, strings.Contains(hourly, "wind_speed_850hPa"))
		})
	}
}

// Open-Meteo takes ONE elevation list for all coordinates, so a partial list would silently attach the
// wrong altitude to the wrong spot. Sending none at all is the only safe fallback.
func TestNightScan_ElevationSentOnlyWhenEveryPointHasOne(t *testing.T) {
	tests := []struct {
		name   string
		points []Point
		want   string
	}{
		{
			name:   "all known",
			points: []Point{{Lat: 44, Lon: 5, ElevationM: 1180}, {Lat: 44.5, Lon: 5.5, ElevationM: 520}},
			want:   "1180.000,520.000",
		},
		{
			name:   "one missing",
			points: []Point{{Lat: 44, Lon: 5, ElevationM: 1180}, {Lat: 44.5, Lon: 5.5}},
			want:   "",
		},
		{name: "none known", points: testPoints(2), want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := newNightUpstream(t)
			p := testProvider(t, up.srv.URL, "", "", "")
			startMs, endMs := testNight()

			_, warn := p.NightScan(context.Background(), tt.points, startMs, endMs, NightOpts{})
			require.Empty(t, warn)

			assert.Equal(t, tt.want, up.query(t).Get("elevation"))
		})
	}
}

func TestNightScan_CloudierPointsScoreLower(t *testing.T) {
	up := newNightUpstream(t)
	p := testProvider(t, up.srv.URL, "", "", "")
	startMs, endMs := testNight()

	out, warn := p.NightScan(context.Background(), testPoints(4), startMs, endMs, NightOpts{})
	require.Empty(t, warn)
	require.Len(t, out, 4)

	for i := 1; i < len(out); i++ {
		assert.Less(t, out[i].Score, out[i-1].Score, "point %d is cloudier than %d", i, i-1)
	}
}

// The cache holds raw hours, not finished scores, so re-weighting for the Moon must be free.
func TestNightScan_RescoringWithADifferentMoonCostsNoFetch(t *testing.T) {
	up := newNightUpstream(t)
	p := testProvider(t, up.srv.URL, "", "", "")
	startMs, endMs := testNight()
	pts := testPoints(3)

	plain, warn := p.NightScan(context.Background(), pts, startMs, endMs, NightOpts{})
	require.Empty(t, warn)
	require.Equal(t, int32(1), up.calls.Load())

	moonlit, warn := p.NightScan(context.Background(), pts, startMs, endMs, NightOpts{
		Moon: func(int64) float64 { return 0.3 },
	})
	require.Empty(t, warn)

	assert.Equal(t, int32(1), up.calls.Load(), "the second scan must come from cache")
	assert.Len(t, moonlit, len(plain))
}

func TestNightScan_BeyondHorizonMakesNoUpstreamCall(t *testing.T) {
	up := newNightUpstream(t)
	p := testProvider(t, up.srv.URL, "", "", "")
	start := time.Now().UTC().AddDate(0, 0, 30)

	out, warn := p.NightScan(context.Background(), testPoints(2), start.UnixMilli(), start.Add(8*time.Hour).UnixMilli(), NightOpts{})

	assert.Zero(t, up.calls.Load(), "Open-Meteo would reject the range — do not spend the quota finding out")
	assert.Contains(t, warn, "forecast horizon")
	require.Len(t, out, 2)
	for _, o := range out {
		assert.False(t, o.Known())
		assert.Contains(t, o.Flags, FlagBeyondHorizon)
	}
}

// A rate-limited scan must degrade to "no weather", never to "every spot is terrible".
func TestNightScan_RateLimitedDegradesToUnknown(t *testing.T) {
	up := newNightUpstream(t)
	p := testProvider(t, up.srv.URL, "", "", "")
	up.mode.Store("429")
	startMs, endMs := testNight()

	out, warn := p.NightScan(context.Background(), testPoints(3), startMs, endMs, NightOpts{})

	assert.Contains(t, warn, "darkness and horizon only")
	require.Len(t, out, 3)
	for _, o := range out {
		assert.False(t, o.Known())
		assert.Zero(t, o.Score)
	}

	before := up.calls.Load()
	_, warn2 := p.NightScan(context.Background(), testPoints(5), startMs, endMs, NightOpts{})
	assert.Equal(t, before, up.calls.Load(), "the breaker must stop a second attempt inside the cooldown")
	assert.NotEmpty(t, warn2)
}

func TestNightScan_EmptyInputIsANoop(t *testing.T) {
	up := newNightUpstream(t)
	p := testProvider(t, up.srv.URL, "", "", "")
	startMs, endMs := testNight()

	out, warn := p.NightScan(context.Background(), nil, startMs, endMs, NightOpts{})
	assert.Nil(t, out)
	assert.Empty(t, warn)

	out, warn = p.NightScan(context.Background(), testPoints(2), endMs, startMs, NightOpts{})
	assert.Nil(t, out)
	assert.Empty(t, warn)
	assert.Zero(t, up.calls.Load())
}

func TestNightKey_SeparatesPointsWindowAndDepth(t *testing.T) {
	startMs, endMs := testNight()
	base := nightKey(testPoints(3), startMs, endMs, NightOpts{})

	assert.Equal(t, base, nightKey(testPoints(3), startMs, endMs, NightOpts{Moon: func(int64) float64 { return 0.5 }}),
		"scoring options must not fragment the cache — the cached hours are raw")
	assert.NotEqual(t, base, nightKey(testPoints(4), startMs, endMs, NightOpts{}))
	assert.NotEqual(t, base, nightKey(testPoints(3), startMs+3600_000, endMs, NightOpts{}))
	assert.NotEqual(t, base, nightKey(testPoints(3), startMs, endMs, NightOpts{Detailed: true}))
	assert.NotContains(t, base, "/", "the key becomes a filename")
}
