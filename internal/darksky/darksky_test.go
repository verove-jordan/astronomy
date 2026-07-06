package darksky

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/elevation"
	"github.com/verove-jordan/astronomy/internal/lightpollution"
	"github.com/verove-jordan/astronomy/internal/routing"
)

type fakeScanner struct{ cells []lightpollution.Cell }

func (f fakeScanner) ScanArea(_ context.Context, _ lightpollution.Bbox, _, _ int) []lightpollution.Cell {
	return f.cells
}

type fakeHorizon struct{ open map[[2]float64]float64 }

func (f fakeHorizon) Horizon(_ context.Context, lat, lon float64) (elevation.Horizon, string) {
	return elevation.Horizon{ElevationM: 500, OpennessPct: f.open[[2]float64{lat, lon}]}, ""
}

func area() lightpollution.Bbox {
	return lightpollution.Bbox{MinLat: 0, MinLon: 0, MaxLat: 5, MaxLon: 5}
}

func TestFind_FiltersByBortleAndRanksDarkest(t *testing.T) {
	cells := []lightpollution.Cell{
		{Lat: 1, Lon: 1, SQM: 21.8, Bortle: 2},
		{Lat: 2, Lon: 2, SQM: 20.0, Bortle: 5}, // above the threshold → dropped
		{Lat: 3, Lon: 3, SQM: 21.2, Bortle: 4},
		{Lat: 4, Lon: 4, SQM: 21.95, Bortle: 1},
	}
	f := New(fakeScanner{cells}, nil, 4000, 10)
	res := f.Find(context.Background(), Query{Bbox: area(), MaxBortle: 4, Limit: 10})

	require.Equal(t, 3, res.Count)
	assert.Equal(t, 21.95, res.Candidates[0].SQM) // darkest first
	assert.Equal(t, 21.2, res.Candidates[2].SQM)
}

func TestFind_HorizonReordersToFavorOpenSites(t *testing.T) {
	cells := []lightpollution.Cell{
		{Lat: 1, Lon: 1, SQM: 21.9, Bortle: 1}, // darkest, but boxed-in
		{Lat: 2, Lon: 2, SQM: 21.5, Bortle: 3}, // slightly brighter, wide open
	}
	fh := fakeHorizon{open: map[[2]float64]float64{{1, 1}: 10, {2, 2}: 100}}
	f := New(fakeScanner{cells}, fh, 4000, 10)
	res := f.Find(context.Background(), Query{Bbox: area(), MaxBortle: 5, Limit: 10, Horizon: true})

	require.Equal(t, 2, res.Count)
	require.NotNil(t, res.Candidates[0].Horizon)
	// Blended score (0.6·dark + 0.4·open) lifts the open site above the marginally darker one.
	assert.Equal(t, 21.5, res.Candidates[0].SQM)
	assert.Equal(t, 100.0, res.Candidates[0].Horizon.OpennessPct)
}

func TestFind_EmptyWhenNoneBelowThreshold(t *testing.T) {
	f := New(fakeScanner{[]lightpollution.Cell{{Lat: 1, Lon: 1, SQM: 19, Bortle: 6}}}, nil, 4000, 10)
	res := f.Find(context.Background(), Query{Bbox: area(), MaxBortle: 4, Limit: 10})
	assert.Equal(t, 0, res.Count)
	assert.NotEmpty(t, res.Warnings)
}

type fakeRouter struct{}

func (fakeRouter) DriveMatrix(_ context.Context, _, _ float64, dstLats, _ []float64) ([]routing.Drive, string) {
	out := make([]routing.Drive, len(dstLats))
	for i := range out {
		out[i] = routing.Drive{DistanceKm: 42, DurationMin: 55, OK: true}
	}
	return out, ""
}

func TestFind_DriveDistanceFilled(t *testing.T) {
	cells := []lightpollution.Cell{{Lat: 1, Lon: 1, SQM: 21.5, Bortle: 2}}
	f := New(fakeScanner{cells}, nil, 4000, 10, WithRouter(fakeRouter{}))
	res := f.Find(context.Background(), Query{Bbox: area(), MaxBortle: 4, Limit: 10, ObsLat: 0.5, ObsLon: 0.5, ObsSet: true})
	require.Equal(t, 1, res.Count)
	assert.Equal(t, 42.0, res.Candidates[0].DriveKm)
	assert.Equal(t, 55.0, res.Candidates[0].DriveMin)
}

func TestScoreCandidate_SouthWeightPenalisesBlockedSouth(t *testing.T) {
	// Great overall openness but a poor southern horizon: south weighting must lower the score.
	c := Candidate{SQM: 21.0, Horizon: &elevation.Horizon{OpennessPct: 90, SouthOpennessPct: 10}}
	base := scoreCandidate(c, ScoreConfig{DarkWeight: 0.6})                  // openness ignores south
	south := scoreCandidate(c, ScoreConfig{DarkWeight: 0.6, SouthWeight: 1}) // openness = south only
	assert.Greater(t, base, south)
}

func TestScoreCandidate_SouthGate(t *testing.T) {
	c := Candidate{SQM: 21.5, Horizon: &elevation.Horizon{OpennessPct: 100, SouthOpennessPct: 100, SouthObstructionDeg: 25}}
	open := scoreCandidate(c, ScoreConfig{DarkWeight: 0.6})                        // no gate
	gated := scoreCandidate(c, ScoreConfig{DarkWeight: 0.6, MaxSouthBlockDeg: 20}) // blocked past 20°
	assert.Greater(t, open, gated)
}

func TestScoreCandidate_DefaultIsHistoricalBlend(t *testing.T) {
	// Default config must reproduce the old 0.6·dark + 0.4·open score exactly.
	c := Candidate{SQM: 21.0, Horizon: &elevation.Horizon{OpennessPct: 50}}
	got := scoreCandidate(c, defaultScoreConfig())
	darkNorm := (21.0 - 18.0) / (22.0 - 18.0)
	assert.InDelta(t, 0.6*darkNorm+0.4*0.5, got, 1e-9)
}
