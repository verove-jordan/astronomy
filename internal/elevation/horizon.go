package elevation

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"
)

const earthRadiusM = 6371000.0

// Horizon summarizes how open a site's horizon is, from a ring of terrain elevations around it.
type Horizon struct {
	ElevationM        float64 `json:"elevation_m"`         // ground elevation at the site
	OpennessPct       float64 `json:"openness_pct"`        // % of azimuths whose horizon is below the open threshold
	MaxObstructionDeg float64 `json:"max_obstruction_deg"` // worst (highest) horizon elevation angle around the site
	WorstAzimuthDeg   float64 `json:"worst_azimuth_deg"`   // azimuth (° from north, clockwise) of that worst obstruction
}

// Horizon evaluates the openness of the horizon at (lat, lon): it samples the site plus a ring of
// azimuths × radii, then scores the obstruction. Cached per ~1 km cell. On an elevation-data failure it
// returns the zero Horizon and a warning (the finder still ranks by darkness).
func (p *Provider) Horizon(ctx context.Context, lat, lon float64) (Horizon, string) {
	key := geoKey(lat, lon)
	if h, ok := p.cachedHorizon(key); ok {
		return h, ""
	}
	lats, lons := p.ringPoints(lat, lon)
	elevs, err := p.Elevations(ctx, lats, lons)
	if err != nil || len(elevs) != len(lats) {
		return Horizon{}, "elevation data unavailable — horizon openness not evaluated"
	}
	h := scoreHorizon(elevs[0], elevs[1:], p.radii, p.naz, p.openDeg)
	p.storeHorizon(key, h)
	return h, ""
}

// ringPoints returns the sample coordinates: index 0 is the site, then naz azimuths each at every
// radius (azimuth-major, so ring index = az*len(radii) + radiusIndex).
func (p *Provider) ringPoints(lat, lon float64) (lats, lons []float64) {
	n := 1 + p.naz*len(p.radii)
	lats = make([]float64, 0, n)
	lons = make([]float64, 0, n)
	lats = append(lats, lat)
	lons = append(lons, lon)
	for a := 0; a < p.naz; a++ {
		az := 2 * math.Pi * float64(a) / float64(p.naz)
		for _, d := range p.radii {
			la, lo := offset(lat, lon, az, d)
			lats = append(lats, la)
			lons = append(lons, lo)
		}
	}
	return lats, lons
}

// offset returns the point distM metres from (lat, lon) along azimuth az (radians from north,
// clockwise), using the small-distance equirectangular approximation (exact enough for a few km).
func offset(lat, lon, az, distM float64) (float64, float64) {
	latRad := lat * math.Pi / 180
	dLat := distM * math.Cos(az) / earthRadiusM
	dLon := distM * math.Sin(az) / (earthRadiusM * math.Cos(latRad))
	return lat + dLat*180/math.Pi, lon + dLon*180/math.Pi
}

// scoreHorizon is the pure scoring core (no I/O): given the site elevation and the ring elevations
// laid out azimuth-major (naz azimuths × len(radii)), the obstruction for each azimuth is the highest
// terrain elevation angle along it, and openness is the fraction of azimuths below openDeg.
func scoreHorizon(site float64, ring, radii []float64, naz int, openDeg float64) Horizon {
	nr := len(radii)
	open := 0
	maxObs, worstAz := 0.0, 0.0
	for a := 0; a < naz; a++ {
		obs := 0.0
		for ri := 0; ri < nr; ri++ {
			idx := a*nr + ri
			if idx >= len(ring) {
				continue
			}
			angle := math.Atan2(ring[idx]-site, radii[ri]) * 180 / math.Pi
			if angle > obs {
				obs = angle
			}
		}
		if obs < openDeg {
			open++
		}
		if obs > maxObs {
			maxObs = obs
			worstAz = 360 * float64(a) / float64(naz)
		}
	}
	pct := 0.0
	if naz > 0 {
		pct = 100 * float64(open) / float64(naz)
	}
	return Horizon{
		ElevationM:        round1(site),
		OpennessPct:       round1(pct),
		MaxObstructionDeg: round1(maxObs),
		WorstAzimuthDeg:   round1(worstAz),
	}
}

// --- cache (in-process memo + disk JSON, keyed by ~1 km cell) ---

type cachedHorizon struct {
	H  Horizon `json:"h"`
	At int64   `json:"at"`
}

func (p *Provider) cachedHorizon(key string) (Horizon, bool) {
	p.mu.Lock()
	c, ok := p.memo[key]
	p.mu.Unlock()
	if ok && p.fresh(c.At) {
		return c.H, true
	}
	if c, ok := p.readDisk(key); ok && p.fresh(c.At) {
		p.mu.Lock()
		p.memo[key] = c
		p.mu.Unlock()
		return c.H, true
	}
	return Horizon{}, false
}

func (p *Provider) storeHorizon(key string, h Horizon) {
	c := cachedHorizon{H: h, At: time.Now().UnixMilli()}
	p.mu.Lock()
	p.memo[key] = c
	p.mu.Unlock()
	if b, err := json.Marshal(c); err == nil {
		_ = os.WriteFile(p.horizonPath(key), b, 0o644)
	}
}

func (p *Provider) readDisk(key string) (cachedHorizon, bool) {
	b, err := os.ReadFile(p.horizonPath(key))
	if err != nil {
		return cachedHorizon{}, false
	}
	var c cachedHorizon
	if err := json.Unmarshal(b, &c); err != nil || c.At == 0 {
		return cachedHorizon{}, false
	}
	return c, true
}

func (p *Provider) fresh(atMs int64) bool {
	return atMs > 0 && time.Since(time.UnixMilli(atMs)) < p.ttl
}

func (p *Provider) horizonPath(key string) string {
	return filepath.Join(p.cacheDir, key+".json")
}

func geoKey(lat, lon float64) string { return fmt.Sprintf("%+.2f_%+.2f", lat, lon) }

func round1(x float64) float64 { return math.Round(x*10) / 10 }
