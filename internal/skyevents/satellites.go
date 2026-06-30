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

	sgp4 "github.com/joshuaferrara/go-satellite"
	"github.com/soniakeys/meeus/v3/moonposition"
	"github.com/verove-jordan/astronomy/internal/astro"
)

const (
	deg2rad = math.Pi / 180
	rad2deg = 180 / math.Pi

	tleCacheTTL = 24 * time.Hour
	// Transit predictions need fresh TLEs and decay quickly, so cap the search horizon.
	transitHorizonDays = 10
	// Effective satellite radius (km) for the disk-crossing test (ISS spans ~100 m).
	satRadiusKm    = 0.05
	sunSemiDiamDeg = 0.2666
)

// curatedSats are the bright satellites worth chasing across the Sun/Moon (ISS, Tiangong/CSS, HST).
var curatedSats = map[int]bool{25544: true, 48274: true, 20580: true}

type namedTLE struct {
	name    string
	noradID int
	l1, l2  string
}

// satelliteEvents finds transits of the Sun and Moon by bright satellites across the exact site, to
// sub-second precision. Needs fresh TLEs (online, short-cached); offline it soft-fails with a warning.
func (e *Engine) satelliteEvents(ctx context.Context, prm Params) ([]Event, string) {
	data, err := e.tleData(ctx)
	if err != nil {
		return nil, "satellite TLE data unavailable (offline?) — Sun/Moon transits skipped this run"
	}
	sats := parseTLEs(data)
	if len(sats) == 0 {
		return nil, "no usable satellite elements — transits skipped"
	}

	horizon := prm.From.AddDate(0, 0, transitHorizonDays)
	if horizon.After(prm.To) {
		horizon = prm.To
	}
	rhoSin, rhoCos := observerParallax(prm.Lat, prm.ElevationM)

	var out []Event
	for _, nt := range sats {
		if !curatedSats[nt.noradID] {
			continue
		}
		sat := sgp4.TLEToSat(nt.l1, nt.l2, sgp4.GravityWGS84)
		out = append(out, e.transitsForSat(sat, nt.name, prm, rhoSin, rhoCos, horizon)...)
	}
	return out, ""
}

// transitsForSat brackets the satellite's above-horizon passes (coarse), then within each pass searches
// finely for a crossing of the Sun's or Moon's disk.
func (e *Engine) transitsForSat(sat sgp4.Satellite, name string, prm Params, rhoSin, rhoCos float64, horizon time.Time) []Event {
	moonBody := func(t time.Time) (az, el, sd float64, up bool) {
		az, el, sd = moonAzElSd(t, prm, rhoSin, rhoCos)
		return az, el, sd, el > 0
	}
	sunBody := func(t time.Time) (az, el, sd float64, up bool) {
		az, el = sunAzEl(t, prm)
		return az, el, sunSemiDiamDeg, el > 0
	}

	var out []Event
	inPass := false
	var passStart time.Time
	scan := func(a, b time.Time) {
		if ev, ok := transitInPass(sat, name, "moon", moonBody, a, b, prm); ok {
			out = append(out, ev)
		}
		if ev, ok := transitInPass(sat, name, "sun", sunBody, a, b, prm); ok {
			out = append(out, ev)
		}
	}
	for t := prm.From; !t.After(horizon); t = t.Add(20 * time.Second) {
		_, el, _ := satState(sat, t, prm)
		if el > 0 && !inPass {
			inPass, passStart = true, t.Add(-20*time.Second)
		} else if el <= 0 && inPass {
			inPass = false
			scan(passStart, t)
		}
	}
	if inPass {
		scan(passStart, horizon)
	}
	return out
}

// transitInPass finds the closest approach of the satellite to a body within [a,b]; if it crosses the
// disk it returns a transit event with sub-second centre time and duration.
func transitInPass(sat sgp4.Satellite, name, body string, bodyAt func(time.Time) (az, el, sd float64, up bool), a, b time.Time, prm Params) (Event, bool) {
	minSep, tmin := 1e9, time.Time{}
	for t := a; !t.After(b); t = t.Add(time.Second) {
		baz, bel, _, up := bodyAt(t)
		if !up {
			continue
		}
		saz, sel, _ := satState(sat, t, prm)
		if sel <= 0 {
			continue
		}
		if s := angularSepAzEl(saz, sel, baz, bel); s < minSep {
			minSep, tmin = s, t
		}
	}
	if tmin.IsZero() || minSep > 1.0 {
		return Event{}, false
	}

	// Sub-second refinement of the closest-approach (transit centre), seeded from the 1 s minimum.
	best, bt := minSep, tmin
	bAz, bEl, bSd, _ := bodyAt(tmin)
	_, _, bRange := satState(sat, tmin, prm)
	for t := tmin.Add(-3 * time.Second); !t.After(tmin.Add(3 * time.Second)); t = t.Add(20 * time.Millisecond) {
		baz, bel, sd, up := bodyAt(t)
		if !up {
			continue
		}
		saz, sel, rng := satState(sat, t, prm)
		if sel <= 0 {
			continue
		}
		if s := angularSepAzEl(saz, sel, baz, bel); s < best {
			best, bt, bAz, bEl, bSd, bRange = s, t, baz, bel, sd, rng
		}
	}
	satRad := math.Atan2(satRadiusKm, bRange) * rad2deg
	if best > bSd+satRad { // closest approach misses the disk — not a transit
		return Event{}, false
	}

	// Duration the satellite spends silhouetted on the disk (sep < body radius).
	start, end := bt, bt
	for t := bt.Add(-4 * time.Second); !t.After(bt.Add(4 * time.Second)); t = t.Add(20 * time.Millisecond) {
		baz, bel, sd, up := bodyAt(t)
		if !up {
			continue
		}
		saz, sel, _ := satState(sat, t, prm)
		if sel <= 0 {
			continue
		}
		if angularSepAzEl(saz, sel, baz, bel) <= sd {
			if t.Before(start) {
				start = t
			}
			if t.After(end) {
				end = t
			}
		}
	}

	inPath := best < bSd
	ev := Event{
		Kind: "satellite_transit", Subtype: body,
		PeakUTCMs:  bt.UnixMilli(),
		StartUTCMs: start.UnixMilli(), EndUTCMs: end.UnixMilli(),
		DurationMs:   end.Sub(start).Milliseconds(),
		Bodies:       []string{satKey(name), body},
		AltAtBestDeg: bEl, AzAtBestDeg: bAz,
		BestUTCMs: bt.UnixMilli(),
		Title:     name + " transits the " + titleCase(body),
		InPath:    &inPath,
		Notable:   true,
	}
	if body == "moon" {
		ev.MoonIllum = astro.MoonIllumination(bt)
	}
	ev.ExtraText = transitNote(ev.DurationMs, bEl)
	return ev, true
}

func transitNote(durMs int64, altDeg float64) string {
	return strings.TrimSpace(
		formatDur(durMs) + " · altitude " + strconv.Itoa(int(math.Round(altDeg))) + "°")
}

func formatDur(ms int64) string {
	return strconv.FormatFloat(float64(ms)/1000.0, 'f', 1, 64) + " s"
}

// satState returns the satellite's topocentric azimuth, elevation (deg) and slant range (km) at t,
// interpolating SGP4 between integer seconds for sub-second precision.
func satState(sat sgp4.Satellite, t time.Time, prm Params) (azDeg, elDeg, rangeKm float64) {
	eci := satECI(sat, t)
	look := sgp4.ECIToLookAngles(eci,
		sgp4.LatLong{Latitude: prm.Lat * deg2rad, Longitude: prm.Lon * deg2rad},
		prm.ElevationM/1000.0, jdayFrac(t))
	return norm360(look.Az * rad2deg), look.El * rad2deg, look.Rg
}

func satECI(sat sgp4.Satellite, t time.Time) sgp4.Vector3 {
	t = t.UTC()
	y, mo, d := t.Date()
	h, mi, s := t.Clock()
	ns := t.Nanosecond()
	p0, _ := sgp4.Propagate(sat, y, int(mo), d, h, mi, s)
	if ns == 0 {
		return p0
	}
	t1 := t.Add(time.Second)
	y1, mo1, d1 := t1.Date()
	h1, mi1, s1 := t1.Clock()
	p1, _ := sgp4.Propagate(sat, y1, int(mo1), d1, h1, mi1, s1)
	f := float64(ns) / 1e9
	return sgp4.Vector3{X: p0.X + (p1.X-p0.X)*f, Y: p0.Y + (p1.Y-p0.Y)*f, Z: p0.Z + (p1.Z-p0.Z)*f}
}

func jdayFrac(t time.Time) float64 {
	t = t.UTC()
	y, mo, d := t.Date()
	h, mi, s := t.Clock()
	return sgp4.JDay(y, int(mo), d, h, mi, s) + float64(t.Nanosecond())/1e9/86400.0
}

func sunAzEl(t time.Time, prm Params) (az, el float64) {
	ra, dec := astro.SunPosition(t)
	alt, azm := astro.Horizontal(ra, dec, prm.Lat, prm.Lon, t)
	return azm, alt
}

// moonAzElSd returns the topocentric (parallax-corrected) Moon azimuth, elevation and semi-diameter
// (deg) — necessary because lunar transits are exquisitely location-dependent.
func moonAzElSd(t time.Time, prm Params, rhoSin, rhoCos float64) (az, el, sd float64) {
	ra, dec, semi := moonTopoRADec(t, prm.Lon, rhoSin, rhoCos)
	alt, azm := astro.Horizontal(ra, dec, prm.Lat, prm.Lon, t)
	return azm, alt, semi
}

func moonTopoRADec(t time.Time, lonDeg, rhoSin, rhoCos float64) (raDeg, decDeg, sdDeg float64) {
	jde := timeToJDE(t)
	lam, bet, dkm := moonposition.Position(jde)
	tc := (jde - 2451545.0) / 36525.0
	eps := (23.439291 - 0.0130042*tc) * deg2rad
	l, b := lam.Rad(), bet.Rad()
	dec := math.Asin(math.Sin(b)*math.Cos(eps) + math.Cos(b)*math.Sin(eps)*math.Sin(l))
	ra := math.Atan2(math.Sin(l)*math.Cos(eps)-math.Tan(b)*math.Sin(eps), math.Cos(l))
	raDeg = norm360(ra * rad2deg)

	sinPi := 6378.14 / dkm
	h := (astro.LST(t, lonDeg) - raDeg) * deg2rad
	dRA := math.Atan2(-rhoCos*sinPi*math.Sin(h), math.Cos(dec)-rhoCos*sinPi*math.Cos(h))
	raTopo := raDeg + dRA*rad2deg
	decTopo := math.Atan2((math.Sin(dec)-rhoSin*sinPi)*math.Cos(dRA), math.Cos(dec)-rhoCos*sinPi*math.Cos(h))
	return norm360(raTopo), decTopo * rad2deg, math.Asin(1737.4/dkm) * rad2deg
}

// observerParallax returns ρ·sinφ′ and ρ·cosφ′ for the site (Meeus ch. 11), used by lunar parallax.
func observerParallax(latDeg, hM float64) (rhoSin, rhoCos float64) {
	phi := latDeg * deg2rad
	u := math.Atan(0.99664719 * math.Tan(phi))
	rhoSin = 0.99664719*math.Sin(u) + hM/6378140*math.Sin(phi)
	rhoCos = math.Cos(u) + hM/6378140*math.Cos(phi)
	return
}

// tleData fetches the configured TLE groups (cached on disk, short TTL; stale fallback offline).
func (e *Engine) tleData(ctx context.Context) ([]byte, error) {
	path := filepath.Join(e.cacheDir, "tle.txt")
	if fi, err := os.Stat(path); err == nil && time.Since(fi.ModTime()) < tleCacheTTL {
		if b, err := os.ReadFile(path); err == nil {
			return b, nil
		}
	}
	var buf bytes.Buffer
	ok := false
	for _, u := range e.tleURLs {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "AstroStack/1.0 (sky events calendar)")
		resp, derr := e.http.Do(req)
		if derr != nil {
			continue
		}
		if resp.StatusCode == http.StatusOK {
			if b, rerr := io.ReadAll(resp.Body); rerr == nil {
				buf.Write(b)
				buf.WriteByte('\n')
				ok = true
			}
		}
		_ = resp.Body.Close()
	}
	if ok && buf.Len() > 0 {
		_ = os.WriteFile(path, buf.Bytes(), 0o644)
		return buf.Bytes(), nil
	}
	if b, rerr := os.ReadFile(path); rerr == nil { // stale fallback
		return b, nil
	}
	return nil, os.ErrNotExist
}

// parseTLEs parses 2- or 3-line TLE sets, capturing the satellite name and NORAD id.
func parseTLEs(data []byte) []namedTLE {
	var out []namedTLE
	sc := bufio.NewScanner(bytes.NewReader(data))
	name, l1 := "", ""
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		switch {
		case strings.HasPrefix(line, "1 ") && len(line) >= 69:
			l1 = line
		case strings.HasPrefix(line, "2 ") && len(line) >= 69 && l1 != "":
			id, _ := strconv.Atoi(strings.TrimSpace(l1[2:7]))
			nm := name
			if nm == "" {
				nm = "NORAD " + strconv.Itoa(id)
			}
			out = append(out, namedTLE{name: nm, noradID: id, l1: l1, l2: line})
			name, l1 = "", ""
		default:
			name = strings.TrimSpace(strings.TrimPrefix(line, "0 "))
			l1 = ""
		}
	}
	return out
}

// satKey turns a TLE name into a canonical lowercase body key the UI can localize (iss, tiangong, hst).
func satKey(name string) string {
	u := strings.ToUpper(name)
	switch {
	case strings.Contains(u, "ISS"):
		return "iss"
	case strings.Contains(u, "CSS") || strings.Contains(u, "TIANHE") || strings.Contains(u, "TIANGONG"):
		return "tiangong"
	case strings.Contains(u, "HST") || strings.Contains(u, "HUBBLE"):
		return "hst"
	}
	return strings.ToLower(strings.ReplaceAll(name, " ", "_"))
}
