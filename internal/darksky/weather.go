package darksky

import (
	"context"
	"math"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/lightpollution"
	"github.com/verove-jordan/astronomy/internal/weather"
)

// The darkest spot in a region is a fixed fact; whether it is worth driving to on Saturday is not. This
// file adds the forecast half of that question, in two passes chosen to keep the cost at two upstream
// calls per search:
//
//  1. an area pass over a coarse lattice covering the whole drawn box, so a clearer-but-slightly-
//     brighter spot can climb into the shortlist — something a shortlist-first design could never do;
//  2. a precise pass at the finalists' exact coordinates and elevations, which is what the table shows.
//
// Both soft-fail. With no forecast the ranking is exactly the historical darkness + horizon blend.

// NightScanner forecasts many locations for one night and rates how much an ensemble agrees about it
// (implemented by *weather.Provider).
type NightScanner interface {
	NightScan(ctx context.Context, pts []weather.Point, startMs, endMs int64, o weather.NightOpts) ([]weather.NightOutlook, string)
	NightConfidence(ctx context.Context, lat, lon float64, startMs, endMs int64) *weather.Confidence
}

// Night describes the night a search was run for.
type Night struct {
	Index       int                 `json:"index"` // 0 = tonight
	StartMs     int64               `json:"start_ms"`
	EndMs       int64               `json:"end_ms"`
	Kind        string              `json:"kind"` // astronomical | nautical | civil | best_effort
	DarkHours   float64             `json:"dark_hours"`
	MoonIllum   float64             `json:"moon_illum"`    // 0..1
	MoonUpHours float64             `json:"moon_up_hours"` // hours of the dark window the Moon spoils
	Confidence  *weather.Confidence `json:"confidence,omitempty"`
}

// planNight resolves which night index N means, at the centre of the search area — twilight and
// moonrise belong to the ground being searched, not to wherever the observer happens to be sitting.
func planNight(idx int, b lightpollution.Bbox) Night {
	lat, lon := boxCenter(b)
	w := nightWindow(idx, lat, lon)
	mid := w.Start.Add(w.End.Sub(w.Start) / 2)
	return Night{
		Index:       idx,
		StartMs:     w.Start.UnixMilli(),
		EndMs:       w.End.UnixMilli(),
		Kind:        w.Kind,
		DarkHours:   round1(w.Hours()),
		MoonIllum:   round2(astro.MoonIllumination(mid)),
		MoonUpHours: round1(astro.MoonUpHours(w, lat, lon)),
	}
}

// nightWindow returns the dark window idx nights from now. Night 0 is whatever night we are in or
// heading into, so a 3 a.m. search still means the session in progress; later nights step from that
// one rather than from the clock, which keeps them one-per-night at every longitude.
func nightWindow(idx int, lat, lon float64) astro.DarkWindow {
	base := astro.NightWindow(time.Now().UTC(), lat, lon, -18)
	if idx <= 0 {
		return base
	}
	return astro.NightWindow(base.Start.AddDate(0, 0, idx).Add(time.Hour), lat, lon, -18)
}

// probeSet is the lattice of forecast sample points laid over the search area, plus the outlook found
// at each. Its resolution is a budget decision, not a cartographic one: Open-Meteo weights a request
// by its location count, so this lattice IS what one search costs.
type probeSet struct {
	minLat, minLon   float64
	stepLat, stepLon float64
	nx, ny           int
	outlooks         []weather.NightOutlook
	deckTopM         float64
}

// probePoints lays out at most maxProbes sample points over the box, keeping the lattice roughly
// square on the ground so a wide, shallow area is not sampled as if it were tall.
func probePoints(b lightpollution.Bbox, maxProbes int) ([]weather.Point, probeSet) {
	if maxProbes < 4 {
		maxProbes = 4
	}
	midLat := (b.MinLat + b.MaxLat) / 2
	spanLat := math.Max(b.MaxLat-b.MinLat, 1e-6)
	spanLon := math.Max((b.MaxLon-b.MinLon)*math.Cos(midLat*math.Pi/180), 1e-6)

	ny := int(math.Round(math.Sqrt(float64(maxProbes) * spanLat / spanLon)))
	ny = clampInt(ny, 2, maxProbes/2)
	nx := clampInt(maxProbes/ny, 2, maxProbes/2)

	set := probeSet{
		minLat: b.MinLat, minLon: b.MinLon,
		stepLat: (b.MaxLat - b.MinLat) / float64(ny-1),
		stepLon: (b.MaxLon - b.MinLon) / float64(nx-1),
		nx:      nx, ny: ny,
	}
	pts := make([]weather.Point, 0, nx*ny)
	for iy := 0; iy < ny; iy++ {
		for ix := 0; ix < nx; ix++ {
			pts = append(pts, weather.Point{
				Lat: set.minLat + set.stepLat*float64(iy),
				Lon: set.minLon + set.stepLon*float64(ix),
			})
		}
	}
	return pts, set
}

// at returns the outlook of the probe nearest a location. Nearest-neighbour, not interpolation: at
// this spacing an interpolated outlook would blend one place's cloud with another's dew point and read
// as a forecast for somewhere that does not exist. Every field here came from one real sample.
func (s probeSet) at(lat, lon float64) *weather.NightOutlook {
	if len(s.outlooks) == 0 {
		return nil
	}
	ix, iy := 0, 0
	if s.stepLon > 0 {
		ix = clampInt(int(math.Round((lon-s.minLon)/s.stepLon)), 0, s.nx-1)
	}
	if s.stepLat > 0 {
		iy = clampInt(int(math.Round((lat-s.minLat)/s.stepLat)), 0, s.ny-1)
	}
	i := iy*s.nx + ix
	if i < 0 || i >= len(s.outlooks) {
		return nil
	}
	return &s.outlooks[i]
}

// scanArea runs the coarse pass and attaches each candidate its nearest probe's outlook.
func (f *Finder) scanArea(ctx context.Context, q Query, night Night, cand []Candidate, res *Result) probeSet {
	pts, set := probePoints(q.Bbox, f.probes)
	outlooks, warn := f.wx.NightScan(ctx, pts, night.StartMs, night.EndMs, weather.NightOpts{
		Moon:     moonFactorAt(q.Bbox),
		AutoDeck: true,
	})
	if warn != "" {
		res.Warnings = append(res.Warnings, warn)
	}
	if len(outlooks) == 0 {
		return probeSet{}
	}
	set.outlooks = outlooks
	set.deckTopM = outlooks[0].DeckTopM

	for i := range cand {
		if o := set.at(cand[i].Lat, cand[i].Lon); o != nil && o.Known() {
			outlook := *o
			cand[i].Weather = &outlook
		}
	}
	return set
}

// refineFinalists re-forecasts the shortlist at its exact coordinates, with the elevations the horizon
// pass resolved and the wind profile the seeing index needs. It is a dozen locations, so it costs a
// fraction of a call — and it is the only weather the user actually reads.
func (f *Finder) refineFinalists(ctx context.Context, cand []Candidate, night Night, set probeSet, res *Result) {
	pts := make([]weather.Point, len(cand))
	for i := range cand {
		pts[i] = weather.Point{Lat: cand[i].Lat, Lon: cand[i].Lon}
		if cand[i].ElevationM != nil {
			pts[i].ElevationM = *cand[i].ElevationM
		}
	}
	outlooks, warn := f.wx.NightScan(ctx, pts, night.StartMs, night.EndMs, weather.NightOpts{
		Moon:     moonFactorAtPoint(cand),
		DeckTopM: set.deckTopM,
		Detailed: true,
	})
	if warn != "" {
		// Do NOT pass this warning through. When the coarse pass succeeded, the spots on screen ARE
		// ranked on a forecast, and the scan's own "ranked on darkness and horizon only" message would
		// be a plain lie about what the user is looking at. Say what is actually missing instead, and
		// stay quiet when the area pass failed too — its warning already covers it.
		if anyForecast(cand) {
			res.Warnings = appendUnique(res.Warnings, "detailed per-spot forecast unavailable — showing the area forecast instead")
		}
		return
	}
	for i := range cand {
		if i < len(outlooks) && outlooks[i].Known() {
			outlook := outlooks[i]
			cand[i].Weather = &outlook
		}
	}
}

// moonFactorAt weights an hour by how much moonlight spoils it, at the centre of the search area. The
// Moon is effectively the same across a box this size, so one curve serves every candidate.
func moonFactorAt(b lightpollution.Bbox) func(int64) float64 {
	lat, lon := boxCenter(b)
	return func(ms int64) float64 { return astro.MoonGlowFactor(time.UnixMilli(ms), lat, lon) }
}

func moonFactorAtPoint(cand []Candidate) func(int64) float64 {
	if len(cand) == 0 {
		return nil
	}
	lat, lon := cand[0].Lat, cand[0].Lon
	return func(ms int64) float64 { return astro.MoonGlowFactor(time.UnixMilli(ms), lat, lon) }
}

func boxCenter(b lightpollution.Bbox) (lat, lon float64) {
	return (b.MinLat + b.MaxLat) / 2, (b.MinLon + b.MaxLon) / 2
}

// anyForecast reports whether the ranking on screen actually rests on a forecast.
func anyForecast(cand []Candidate) bool {
	for _, c := range cand {
		if c.Weather != nil && c.Weather.Known() {
			return true
		}
	}
	return false
}

func appendUnique(list []string, s string) []string {
	for _, existing := range list {
		if existing == s {
			return list
		}
	}
	return append(list, s)
}

func clampInt(v, lo, hi int) int {
	if hi < lo {
		hi = lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
