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

// Horizon summarizes how open a site's horizon is, from a ring of terrain (and, when available, treetop)
// elevations around it.
type Horizon struct {
	ElevationM        float64 `json:"elevation_m"`         // ground elevation at the site
	OpennessPct       float64 `json:"openness_pct"`        // % of azimuths whose horizon is below the open threshold
	MaxObstructionDeg float64 `json:"max_obstruction_deg"` // worst (highest) horizon elevation angle around the site
	WorstAzimuthDeg   float64 `json:"worst_azimuth_deg"`   // azimuth (° from north, clockwise) of that worst obstruction

	// Precision metrics. South is the arc that matters most for N-hemisphere deep-sky (the celestial equator
	// and most targets transit low in the south); these are 0 in a degenerate ring but always computed.
	MeanObstructionDeg   float64    `json:"mean_obstruction_deg"`    // average horizon elevation angle over all azimuths
	SouthObstructionDeg  float64    `json:"south_obstruction_deg"`   // worst obstruction within ±90° of due south
	SouthWorstAzimuthDeg float64    `json:"south_worst_azimuth_deg"` // azimuth of that worst southern obstruction
	SouthOpennessPct     float64    `json:"south_openness_pct"`      // cosine-of-south-weighted openness %
	CanopyM              float64    `json:"canopy_m"`                // canopy height (m) at the site itself (0 = clearing / no data)
	Octants              [8]float64 `json:"octants"`                 // max obstruction angle per 45° sector, N,NE,E…NW (for a UI rose)
}

// Horizon evaluates the openness of the horizon at (lat, lon): it samples the site plus a ring of
// azimuths × radii, then scores the obstruction. Cached per ~1 km cell. On an elevation-data failure it
// returns the zero Horizon and a warning (the finder still ranks by darkness).
func (p *Provider) Horizon(ctx context.Context, lat, lon float64) (Horizon, string) {
	key := geoKey(lat, lon)
	if h, ok := p.cachedHorizon(key); ok {
		return h, ""
	}
	naz, radii, eye := p.naz, p.radii, 0.0
	canopyOn := p.canopy != nil && p.canopy.Active()
	if canopyOn {
		naz, radii, eye = p.canopyNaz, p.canopyRadii, p.eyeHeightM
	}
	lats, lons := ringPoints(lat, lon, naz, radii)
	elevs, err := p.Elevations(ctx, lats, lons)
	if err != nil || len(elevs) != len(lats) {
		return Horizon{}, "elevation data unavailable — horizon openness not evaluated"
	}
	in := horizonScore{
		site:      elevs[0],
		ring:      elevs[1:],
		radii:     radii,
		naz:       naz,
		openDeg:   p.openDeg,
		eyeHeight: eye,
		southAz:   southAzFor(lat),
	}
	var siteCanopy float64
	if canopyOn {
		if ch := p.canopy.CanopyHeights(ctx, lats, lons); len(ch) == len(lats) {
			siteCanopy = ch[0]
			in.canopy = ch[1:]
		}
	}
	h := scoreHorizonCanopy(in)
	h.CanopyM = round1(siteCanopy)
	p.storeHorizon(key, h)
	return h, ""
}

// ringPoints returns the sample coordinates: index 0 is the site, then naz azimuths each at every
// radius (azimuth-major, so ring index = az*len(radii) + radiusIndex).
func ringPoints(lat, lon float64, naz int, radii []float64) (lats, lons []float64) {
	n := 1 + naz*len(radii)
	lats = make([]float64, 0, n)
	lons = make([]float64, 0, n)
	lats = append(lats, lat)
	lons = append(lons, lon)
	for a := 0; a < naz; a++ {
		az := 2 * math.Pi * float64(a) / float64(naz)
		for _, d := range radii {
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

// horizonScore is the input to scoreHorizonCanopy: the site elevation, the ring elevations laid out
// azimuth-major (naz azimuths × len(radii)), optional canopy heights aligned to the same ring (nil = bare
// terrain), and the tuning. southAz is the azimuth (° from north) of due south for the site's hemisphere.
type horizonScore struct {
	site      float64
	ring      []float64
	canopy    []float64 // aligned to ring, or nil for bare terrain
	radii     []float64
	naz       int
	openDeg   float64
	eyeHeight float64 // observer eye height (m) above the site ground
	southAz   float64
}

// scoreHorizon scores a bare-terrain horizon (no canopy, observer at ground level). Retained as the
// terrain-only entry point (and its regression test); it delegates to scoreHorizonCanopy, so their shared
// OpennessPct/MaxObstructionDeg/WorstAzimuthDeg are byte-identical.
func scoreHorizon(site float64, ring, radii []float64, naz int, openDeg float64) Horizon {
	return scoreHorizonCanopy(horizonScore{
		site: site, ring: ring, radii: radii, naz: naz, openDeg: openDeg, southAz: 180,
	})
}

// scoreHorizonCanopy is the pure scoring core (no I/O). The obstruction for each azimuth is the highest
// treetop elevation angle along it — atan2((terrain+canopy) − (site+eye), radius) — and openness is the
// fraction of azimuths below openDeg. With canopy=nil and eyeHeight=0 the treetop reference collapses to
// the bare terrain (treetop−eye ≡ terrain−site), so the shared metrics match the old terrain-only result.
func scoreHorizonCanopy(in horizonScore) Horizon {
	nr := len(in.radii)
	eye := in.site + in.eyeHeight
	obs := make([]float64, in.naz)
	for a := 0; a < in.naz; a++ {
		worst := 0.0
		for ri := 0; ri < nr; ri++ {
			idx := a*nr + ri
			if idx >= len(in.ring) {
				continue
			}
			top := in.ring[idx]
			if in.canopy != nil && idx < len(in.canopy) {
				top += in.canopy[idx]
			}
			angle := math.Atan2(top-eye, in.radii[ri]) * 180 / math.Pi
			if angle > worst {
				worst = angle
			}
		}
		obs[a] = worst
	}
	return summarizeObstruction(in.site, obs, in.naz, in.openDeg, in.southAz)
}

// summarizeObstruction turns per-azimuth obstruction angles into the Horizon metrics: overall openness +
// worst/mean, the south-weighted openness/worst (cosine weight over ±90° of due south), and an 8-way rose.
func summarizeObstruction(site float64, obs []float64, naz int, openDeg, southAz float64) Horizon {
	open, sum := 0, 0.0
	maxObs, worstAz := 0.0, 0.0
	southMax, southWorstAz := 0.0, 0.0
	var southOpenW, southW float64
	var octants [8]float64
	for a := 0; a < naz; a++ {
		o := obs[a]
		sum += o
		if o < openDeg {
			open++
		}
		if o > maxObs {
			maxObs, worstAz = o, 360*float64(a)/float64(naz)
		}
		azDeg := 360 * float64(a) / float64(naz)
		if oct := ((int(math.Round(azDeg/45)) % 8) + 8) % 8; o > octants[oct] {
			octants[oct] = o
		}
		if w := math.Cos((azDeg - southAz) * math.Pi / 180); w > 0 {
			southW += w
			if o < openDeg {
				southOpenW += w
			}
			if o > southMax {
				southMax, southWorstAz = o, azDeg
			}
		}
	}
	pct, meanObs := 0.0, 0.0
	if naz > 0 {
		pct = 100 * float64(open) / float64(naz)
		meanObs = sum / float64(naz)
	}
	southPct := pct
	if southW > 0 {
		southPct = 100 * southOpenW / southW
	}
	for i := range octants {
		octants[i] = round1(octants[i])
	}
	return Horizon{
		ElevationM:           round1(site),
		OpennessPct:          round1(pct),
		MaxObstructionDeg:    round1(maxObs),
		WorstAzimuthDeg:      round1(worstAz),
		MeanObstructionDeg:   round1(meanObs),
		SouthObstructionDeg:  round1(southMax),
		SouthWorstAzimuthDeg: round1(southWorstAz),
		SouthOpennessPct:     round1(southPct),
		Octants:              octants,
	}
}

// southAzFor returns the azimuth (° from north) of due south for the site's hemisphere: 180° in the
// northern hemisphere (where the low southern sky holds the celestial equator and most deep-sky targets),
// 0° in the southern hemisphere.
func southAzFor(lat float64) float64 {
	if lat < 0 {
		return 0
	}
	return 180
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
