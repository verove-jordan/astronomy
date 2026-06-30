package skyevents

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/soniakeys/meeus/v3/julian"
	"github.com/verove-jordan/astronomy/internal/astro"
)

// gaussK is the Gaussian gravitational constant (AU^1.5 / day); gaussK² = GM_sun in these units.
const gaussK = 0.01720209895

// cometCacheTTL is how long a fetched MPC element file is reused before refetching.
const cometCacheTTL = 7 * 24 * time.Hour

// cometElem is one comet's orbital elements + magnitude law parameters (from MPC CometEls.txt).
type cometElem struct {
	Name    string
	TpJDE   float64 // perihelion time (Julian Ephemeris Day)
	Q       float64 // perihelion distance (AU)
	E       float64 // eccentricity
	PeriDeg float64
	NodeDeg float64
	IncDeg  float64
	H       float64 // absolute (total) magnitude M1
	Slope   float64 // activity exponent n (m = H + 5·logΔ + 2.5n·log r)
}

// cometEvents lists comets that grow bright enough to see from the site during the window. The MPC
// element file is fetched and cached on disk; offline with no cache it soft-fails with a warning.
func (e *Engine) cometEvents(ctx context.Context, prm Params) ([]Event, string) {
	data, err := e.cometData(ctx)
	if err != nil {
		return nil, "comet data unavailable (offline?) — comets skipped this run"
	}
	comets := parseComets(data)
	limit := telescopeLimit(prm.Optics.ApertureMM) + 0.5

	var out []Event
	for _, c := range comets {
		bestMag, bestT := 99.0, time.Time{}
		for t := prm.From; !t.After(prm.To); t = t.Add(48 * time.Hour) {
			_, _, r, d := cometGeo(c, t)
			if r <= 0 || d <= 0 {
				continue
			}
			if m := cometMag(c, r, d); m < bestMag {
				bestMag, bestT = m, t
			}
		}
		if bestT.IsZero() || bestMag > limit {
			continue
		}
		mid := astro.SolarMidnight(bestT, prm.Lat, prm.Lon)
		ra, dec, r, d := cometGeo(c, mid)
		ev := Event{
			Kind:      "comet",
			PeakUTCMs: mid.UnixMilli(),
			Magnitude: cometMag(c, r, d), HasMag: true,
			RADeg: ra, DecDeg: dec, HasPosition: true,
			Title:     c.Name,
			ExtraText: "comet",
			Notable:   true,
		}
		ev.applyObs(observeNight(ra, dec, mid, prm))
		out = append(out, ev)
	}
	return out, ""
}

// cometData returns the MPC element file, using a fresh disk cache when available, fetching otherwise,
// and falling back to a stale cache when the network is down.
func (e *Engine) cometData(ctx context.Context) ([]byte, error) {
	path := filepath.Join(e.cacheDir, "CometEls.txt")
	if fi, err := os.Stat(path); err == nil && time.Since(fi.ModTime()) < cometCacheTTL {
		if b, err := os.ReadFile(path); err == nil {
			return b, nil
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.cometURL, nil)
	if err == nil {
		req.Header.Set("User-Agent", "AstroStack/1.0 (sky events calendar)")
		resp, derr := e.http.Do(req)
		if derr == nil {
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode == http.StatusOK {
				if b, rerr := io.ReadAll(resp.Body); rerr == nil && len(b) > 0 {
					_ = os.WriteFile(path, b, 0o644)
					return b, nil
				}
			}
		}
	}
	if b, rerr := os.ReadFile(path); rerr == nil { // stale fallback
		return b, nil
	}
	return nil, os.ErrNotExist
}

// parseComets parses MPC CometEls.txt fixed-column records into orbital elements.
func parseComets(data []byte) []cometElem {
	var out []cometElem
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1024), 1<<16)
	for sc.Scan() {
		line := sc.Text()
		if len(line) < 100 {
			continue
		}
		y, ok1 := colInt(line, 14, 18)
		mo, ok2 := colInt(line, 19, 21)
		day, ok3 := colF(line, 22, 29)
		q, ok4 := colF(line, 30, 39)
		ecc, ok5 := colF(line, 40, 49)
		peri, ok6 := colF(line, 50, 59)
		node, ok7 := colF(line, 60, 69)
		inc, ok8 := colF(line, 70, 79)
		ok := ok1 && ok2 && ok3 && ok4 && ok5 && ok6 && ok7 && ok8
		if !ok || q <= 0 {
			continue
		}
		h, okH := colF(line, 91, 95)
		if !okH {
			h = 12 // unknown brightness → treat as moderately faint
		}
		slope, okS := colF(line, 96, 100)
		if !okS || slope <= 0 {
			slope = 4 // standard comet activity exponent
		}
		name := strings.TrimSpace(line[102:])
		if i := strings.Index(name, "  "); i > 0 { // cut the trailing reference column
			name = strings.TrimSpace(name[:i])
		}
		out = append(out, cometElem{
			Name:  name,
			TpJDE: julian.CalendarGregorianToJD(y, mo, day),
			Q:     q, E: ecc, PeriDeg: peri, NodeDeg: node, IncDeg: inc,
			H: h, Slope: slope,
		})
	}
	return out
}

// cometMag estimates the comet's total apparent magnitude: m = H + 5·log10(Δ) + 2.5·n·log10(r).
func cometMag(c cometElem, rHelio, delta float64) float64 {
	return c.H + 5*math.Log10(delta) + 2.5*c.Slope*math.Log10(rHelio)
}

// cometGeo returns the comet's geocentric apparent RA/Dec (degrees), heliocentric distance r and
// geocentric distance Δ (AU) at t, by universal-variable two-body propagation from perihelion.
func cometGeo(c cometElem, t time.Time) (raDeg, decDeg, rHelio, delta float64) {
	mu := gaussK * gaussK
	dt := timeToJDE(t) - c.TpJDE
	// Perifocal state at perihelion: on the +x axis, moving in +y.
	r0 := vec3{c.Q, 0, 0}
	v0 := vec3{0, math.Sqrt(mu * (1 + c.E) / c.Q), 0}
	p := keplerUniversal(r0, v0, dt, mu)

	// Rotate perifocal (P toward perihelion, Q 90° ahead) into heliocentric ecliptic coordinates.
	cO, sO := cosD(c.NodeDeg), sinD(c.NodeDeg)
	cw, sw := cosD(c.PeriDeg), sinD(c.PeriDeg)
	ci, si := cosD(c.IncDeg), sinD(c.IncDeg)
	px, py, pz := cw*cO-sw*sO*ci, cw*sO+sw*cO*ci, sw*si
	qx, qy, qz := -sw*cO-cw*sO*ci, -sw*sO+cw*cO*ci, cw*si
	xh := px*p.x + qx*p.y
	yh := py*p.x + qy*p.y
	zh := pz*p.x + qz*p.y
	rHelio = math.Sqrt(xh*xh + yh*yh + zh*zh)

	xs, ys, _ := astro.SunEclipticRect(t)
	raDeg, decDeg, delta = astro.EclRectToEqua(xh+xs, yh+ys, zh, t)
	return
}

type vec3 struct{ x, y, z float64 }

// keplerUniversal propagates a two-body state (r0,v0 in AU, AU/day) by dt days, for any conic, using
// the universal-variable (Stumpff) formulation. Returns the new position.
func keplerUniversal(r0v, v0v vec3, dt, mu float64) vec3 {
	r0 := math.Sqrt(r0v.x*r0v.x + r0v.y*r0v.y + r0v.z*r0v.z)
	v0 := math.Sqrt(v0v.x*v0v.x + v0v.y*v0v.y + v0v.z*v0v.z)
	vr0 := (r0v.x*v0v.x + r0v.y*v0v.y + r0v.z*v0v.z) / r0
	alpha := 2/r0 - v0*v0/mu // reciprocal semi-major axis (1/a); <0 hyperbolic
	smu := math.Sqrt(mu)

	chi := smu * math.Abs(alpha) * dt
	if chi == 0 {
		chi = smu * dt / r0
	}
	for i := 0; i < 80; i++ {
		z := alpha * chi * chi
		cz, sz := stumpffC(z), stumpffS(z)
		f := r0*vr0/smu*chi*chi*cz + (1-alpha*r0)*chi*chi*chi*sz + r0*chi - smu*dt
		df := r0*vr0/smu*chi*(1-z*sz) + (1-alpha*r0)*chi*chi*cz + r0
		if df == 0 {
			break
		}
		d := f / df
		chi -= d
		if math.Abs(d) < 1e-9 {
			break
		}
	}
	z := alpha * chi * chi
	fF := 1 - chi*chi/r0*stumpffC(z)
	gG := dt - chi*chi*chi/smu*stumpffS(z)
	return vec3{
		fF*r0v.x + gG*v0v.x,
		fF*r0v.y + gG*v0v.y,
		fF*r0v.z + gG*v0v.z,
	}
}

// stumpffC, stumpffS are the Stumpff functions C(z), S(z) used by the universal Kepler solver.
func stumpffC(z float64) float64 {
	switch {
	case z > 1e-8:
		return (1 - math.Cos(math.Sqrt(z))) / z
	case z < -1e-8:
		return (math.Cosh(math.Sqrt(-z)) - 1) / -z
	default:
		return 0.5 - z/24
	}
}

func stumpffS(z float64) float64 {
	switch {
	case z > 1e-8:
		s := math.Sqrt(z)
		return (s - math.Sin(s)) / (s * s * s)
	case z < -1e-8:
		s := math.Sqrt(-z)
		return (math.Sinh(s) - s) / (s * s * s)
	default:
		return 1.0/6 - z/120
	}
}

// --- fixed-column helpers (0-based [a:b], clamped) ---

func colStr(line string, a, b int) string {
	if a >= len(line) {
		return ""
	}
	if b > len(line) {
		b = len(line)
	}
	return strings.TrimSpace(line[a:b])
}

func colF(line string, a, b int) (float64, bool) {
	s := colStr(line, a, b)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	return v, err == nil
}

func colInt(line string, a, b int) (int, bool) {
	s := colStr(line, a, b)
	if s == "" {
		return 0, false
	}
	v, err := strconv.Atoi(s)
	return v, err == nil
}
