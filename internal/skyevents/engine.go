package skyevents

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/verove-jordan/astronomy/internal/config"
)

// Engine computes the events calendar. It is safe for concurrent use. The embedded meteor table is
// compiled in; the comet/TLE feeds are fetched on demand and cached on disk (soft-fail offline).
type Engine struct {
	cacheDir string
	http     *http.Client
	cometURL string
	tleURLs  []string
}

// New builds an Engine, placing its on-disk caches under the work dir (falling back to the user cache).
func New(cfg *config.Config) *Engine {
	cache := filepath.Join(cfg.WorkDir, "cache", "skyevents")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		if ucd, e2 := os.UserCacheDir(); e2 == nil {
			cache = filepath.Join(ucd, "astrostack", "skyevents")
			_ = os.MkdirAll(cache, 0o755)
		}
	}
	return &Engine{
		cacheDir: cache,
		http:     &http.Client{Timeout: 20 * time.Second},
		cometURL: "https://www.minorplanetcenter.net/iau/MPCORB/CometEls.txt",
		// CelesTrak TLE groups: the crewed stations (ISS, CSS/Tiangong) plus the "visual" brightest sats.
		tleURLs: []string{
			"https://celestrak.org/NORAD/elements/gp.php?GROUP=stations&FORMAT=tle",
			"https://celestrak.org/NORAD/elements/gp.php?GROUP=visual&FORMAT=tle",
		},
	}
}

// Compute generates, scores and time-sorts the events in the requested window for the site/gear.
func (e *Engine) Compute(ctx context.Context, prm Params) (*Result, error) {
	res := &Result{}
	if prm.enabled(CatEclipse) {
		res.Events = append(res.Events, eclipseEvents(prm)...)
	}
	if prm.enabled(CatPlanet) {
		res.Events = append(res.Events, planetEvents(prm)...)
	}
	if prm.enabled(CatMoon) {
		res.Events = append(res.Events, moonEvents(prm)...)
	}
	if prm.enabled(CatSeason) {
		res.Events = append(res.Events, seasonEvents(prm)...)
	}
	if prm.enabled(CatMeteor) {
		res.Events = append(res.Events, meteorEvents(prm)...)
	}
	if prm.enabled(CatComet) {
		evs, warn := e.cometEvents(ctx, prm)
		res.Events = append(res.Events, evs...)
		if warn != "" {
			res.Warnings = append(res.Warnings, warn)
		}
	}
	if prm.enabled(CatSatellite) {
		evs, warn := e.satelliteEvents(ctx, prm)
		res.Events = append(res.Events, evs...)
		if warn != "" {
			res.Warnings = append(res.Warnings, warn)
		}
	}

	for i := range res.Events {
		finalizeEvent(&res.Events[i], prm)
	}
	sort.SliceStable(res.Events, func(i, j int) bool {
		if res.Events[i].PeakUTCMs != res.Events[j].PeakUTCMs {
			return res.Events[i].PeakUTCMs < res.Events[j].PeakUTCMs
		}
		return res.Events[i].Score > res.Events[j].Score
	})
	res.Count = len(res.Events)
	return res, nil
}
