package annotate

import (
	"math"
	"sort"
	"time"

	"github.com/verove-jordan/astronomy/internal/deepstars"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/skycat"
	"github.com/verove-jordan/astronomy/internal/skyplan"
)

const (
	maxStarLabels = 80 // proper-named stars are always kept; the rest fill up to this
	maxDSOLabels  = 40
	labelMinSepPx = 24 // two star labels closer than this (final px) would render on top of each other
)

// fieldCenter returns the sky position of the master's center plus a radius covering the whole
// frame (with a 5% margin), in degrees.
func fieldCenter(wcs fits.WCS, m mapping) (ra, dec, radiusDeg float64) {
	ra, dec = wcs.PixToSky(float64(m.W)/2, float64(m.H)/2)
	radiusDeg = wcs.ScaleArcsecPerPix() * math.Hypot(float64(m.W), float64(m.H)) / 2 * 1.05 / 3600
	return ra, dec, radiusDeg
}

// starLabels projects the deep star catalogue into the final frame: every star must land on a
// detected peak (the label snaps to the real centroid), carry a designation, and keep a minimum
// separation so a dense field does not ship overlapping text. Proper-named stars are always kept;
// the rest fill brightest-first to maxStarLabels.
func starLabels(wcs fits.WCS, m mapping, grid *peakGrid, epoch time.Time) []Label {
	ra, dec, radius := fieldCenter(wcs, m)
	stars := deepstars.InField(ra, dec, radius, 0, epoch) // magnitude-ascending

	type placed struct{ x, y float64 }
	var accepted []placed
	farEnough := func(x, y float64) bool {
		for _, a := range accepted {
			if math.Hypot(a.x-x, a.y-y) < labelMinSepPx {
				return false
			}
		}
		return true
	}

	var out []Label
	add := func(s deepstars.Star) {
		px, py, ok := wcs.SkyToPix(s.RADeg, s.DecDeg)
		if !ok {
			return
		}
		fx, fy := m.wcsToFile(px, py)
		peak, found := grid.nearest(fx, fy, matchTolPx)
		if !found {
			return
		}
		x, y, inFrame := m.toFinal(float64(peak.X), float64(peak.Y))
		if !inFrame || !farEnough(x, y) {
			return
		}
		accepted = append(accepted, placed{x, y})
		out = append(out, Label{
			X: x, Y: y,
			Name: s.Primary(), Secondary: s.Secondary(),
			Kind: "star", Mag: s.Mag,
		})
	}

	for _, s := range stars { // named stars first — never displaced by anonymous ones
		if s.Proper != "" {
			add(s)
		}
	}
	for _, s := range stars {
		if len(out) >= maxStarLabels {
			break
		}
		if s.Proper == "" && s.Primary() != "" {
			add(s)
		}
	}
	return out
}

// dsoLabels projects the deep-sky catalogue (Messier/NGC/IC/Sh2/LDN + OpenNGC names) into the
// final frame. DSOs are extended objects — no detected-peak requirement; position is the
// projected catalogue center. Brightest first, objects without a magnitude last.
func dsoLabels(wcs fits.WCS, m mapping, catalogDir string) []Label {
	ra0, dec0, radius := fieldCenter(wcs, m)
	const degRad = math.Pi / 180
	sinD0, cosD0 := math.Sincos(dec0 * degRad)
	cosR := math.Cos(radius * degRad)

	var out []Label
	for _, rec := range skycat.Load(catalogDir).Records() {
		if math.Abs(rec.DecDeg-dec0) > radius {
			continue
		}
		sinD, cosD := math.Sincos(rec.DecDeg * degRad)
		if sinD*sinD0+cosD*cosD0*math.Cos((rec.RADeg-ra0)*degRad) < cosR {
			continue
		}
		px, py, ok := wcs.SkyToPix(rec.RADeg, rec.DecDeg)
		if !ok {
			continue
		}
		x, y, inFrame := m.toFinal(m.wcsToFile(px, py))
		if !inFrame {
			continue
		}
		l := Label{
			X: x, Y: y,
			Name: rec.Name, Kind: "dso",
			Type: skyplan.DeriveType(rec),
			Mag:  noMagSentinel,
		}
		if rec.HasMag {
			l.Mag = rec.MagV
		}
		if len(rec.CommonNames) > 0 {
			l.Secondary = rec.CommonNames[0]
		}
		if rec.HasDiameter {
			l.Diameter = rec.DiameterArcmin
		}
		out = append(out, l)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Mag < out[j].Mag })
	if len(out) > maxDSOLabels {
		out = out[:maxDSOLabels]
	}
	return out
}
