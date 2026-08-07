package annotate

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/deepstars"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/skycat"
	"github.com/verove-jordan/astronomy/internal/skyplan"
)

const (
	maxStarLabels = 80 // proper-named stars are always kept; the rest fill up to this
	maxDSOLabels  = 40
	labelMinSepPx = 24 // two star labels closer than this (final px) would render on top of each other
	// maxFieldStars bounds the cone query behind both the labels and the per-star identification. A
	// normal telescope frame pulls a few hundred stars, so the cap only bites on a wide-angle
	// nightscape, where it keeps the brightest 50k — well past anything a camera lens resolves, and
	// past anything the overlay can plot (maxPlottedStars is 5000).
	maxFieldStars = 50_000
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
// starLabels also returns the magnitude zero-point samples it can measure along the way: every
// catalogue star it snaps onto a real peak pairs a KNOWN V magnitude with this frame's instrumental
// brightness, which is exactly what anchors the estimated magnitudes of the unnamed detections.
func starLabels(stars []deepstars.Star, wcs fits.WCS, m mapping, grid *peakGrid) ([]Label, []float64) {
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
	var zpSamples []float64
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
		if f := starFlux(peak); f > 0 {
			// Pairs this frame's instrumental flux with the star's KNOWN V magnitude; the median of
			// these is the zero point that anchors every other detection.
			zpSamples = append(zpSamples, s.Mag+2.5*math.Log10(f))
		}
		out = append(out, Label{
			X: x, Y: y,
			Name: s.Primary(), Secondary: s.Secondary(),
			Kind: "star", Mag: s.Mag,
			Star: starInfo(s),
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
	return out, zpSamples
}

// offsetSky returns the sky position d degrees from (ra,dec) along position angle pa (degrees, the
// catalogue convention: measured at the object from North through East). Standard spherical
// direct-geodesic formulae; all arguments and results in degrees.
func offsetSky(raDeg, decDeg, paDeg, dDeg float64) (float64, float64) {
	const rad = math.Pi / 180
	sinD, cosD := math.Sincos(dDeg * rad)
	sinDec, cosDec := math.Sincos(decDeg * rad)
	sinPA, cosPA := math.Sincos(paDeg * rad)
	dec2 := math.Asin(sinDec*cosD + cosDec*sinD*cosPA)
	dRA := math.Atan2(sinPA*sinD*cosDec, cosD-sinDec*math.Sin(dec2))
	return raDeg + dRA/rad, dec2 / rad
}

// extentOf projects a catalogued object's angular size into final-image pixels. Rather than convert
// arcminutes with the plate scale and then reason about the WCS rotation, it projects the two axis
// TIPS through the very same chain the label anchors use (SkyToPix → wcsToFile → toFinal) and
// measures the resulting vectors — so image rotation, the empirically-chosen WCS y flip, the
// ROWORDER flip and the finish crop are all accounted for by construction, with no second opinion
// about orientation that could disagree with where the labels land.
//
// Position angle is required to orient an ellipse: when the catalogue gives a minor axis but no PA
// the footprint is drawn as a CIRCLE on the major axis rather than an arbitrarily-rotated ellipse —
// overstating the area slightly is honest, pointing a galaxy the wrong way is not.
func extentOf(wcs fits.WCS, m mapping, rec skycat.Record) (*Extent, bool) {
	if !rec.HasDiameter || rec.DiameterArcmin <= 0 {
		return nil, false
	}
	project := func(ra, dec float64) (float64, float64, bool) {
		px, py, ok := wcs.SkyToPix(ra, dec)
		if !ok {
			return 0, 0, false
		}
		// The tips may fall outside the crop — that is fine and common for a large nebula, so the
		// inFrame flag is deliberately ignored here (the anchor itself is already frame-checked).
		x, y, _ := m.toFinal(m.wcsToFile(px, py))
		return x, y, true
	}
	cx, cy, ok := project(rec.RADeg, rec.DecDeg)
	if !ok {
		return nil, false
	}
	semiMajorDeg := rec.DiameterArcmin / 2 / 60
	pa := 0.0
	if rec.HasPositionAngle {
		pa = rec.PositionAngleDeg
	}
	tipRA, tipDec := offsetSky(rec.RADeg, rec.DecDeg, pa, semiMajorDeg)
	tx, ty, ok := project(tipRA, tipDec)
	if !ok {
		return nil, false
	}
	rx := math.Hypot(tx-cx, ty-cy)
	if rx <= 0 {
		return nil, false
	}
	ry := rx
	if rec.HasPositionAngle && rec.HasMinorAxis && rec.MinorAxisArcmin > 0 {
		mRA, mDec := offsetSky(rec.RADeg, rec.DecDeg, pa+90, rec.MinorAxisArcmin/2/60)
		if mx, my, ok := project(mRA, mDec); ok {
			ry = math.Hypot(mx-cx, my-cy)
		}
	}
	return &Extent{RXpx: rx, RYpx: ry, AngleRad: math.Atan2(ty-cy, tx-cx)}, true
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
		if rec.HasMinorAxis {
			l.MinorAxis = rec.MinorAxisArcmin
		}
		l.Morphology = rec.Morphology
		if e, ok := extentOf(wcs, m, rec); ok {
			l.Extent = e
		}
		out = append(out, l)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Mag < out[j].Mag })
	if len(out) > maxDSOLabels {
		out = out[:maxDSOLabels]
	}
	return out
}
