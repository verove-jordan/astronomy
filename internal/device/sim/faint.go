package sim

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/deepstars"
)

// A synthetic faint-star population, so simulated frames look like real ones.
//
// The bundled catalogue stops around magnitude 9, which puts 6–20 stars in a 1° field. A real
// 100 mm scope reaches magnitude 15–16 in a couple of minutes and records THOUSANDS. That gap is
// not cosmetic: Siril refuses to plate-solve a frame with fewer than six detected stars, so without
// this the simulator cannot exercise plate-solve centring, tracking measurement or star matching —
// exactly the paths that most need testing without hardware.
//
// The population must be a property of the SKY, not of the frame: two exposures of the same field,
// or two overlapping mosaic panels, have to contain the same stars in the same places or nothing
// downstream means anything. So stars are generated deterministically from the sky cell they fall
// in — the same cell always yields the same stars, from any pointing, in any process, forever.

// faintCellDeg is the generating grid's cell size in declination. Small enough that a field only
// touches a few dozen cells, large enough that the per-cell work stays trivial.
const faintCellDeg = 0.25

// faintMagMin/Max bound the synthetic population: it starts just below where the real catalogue
// runs out, and stops at a realistic limiting magnitude for a 100 mm scope on a few-minute sub.
//
// faintMagMin sits well BELOW the real catalogue's limit, and that margin is load-bearing. Siril
// plate-solves by matching the BRIGHTEST detected stars against its reference catalogue; synthetic
// stars are not real, so any that outshine the genuine ones poison the match. Starting the
// population two magnitudes fainter keeps every real star brighter than every fabricated one, so
// the solver's brightest-N selection still sees the true pattern.
const (
	faintMagMin = 11.5
	faintMagMax = 16.0
)

// defaultFaintPerDeg2 is the synthetic surface density. Real counts to magnitude 16 run from about
// 1000 per square degree near the galactic poles to tens of thousands in the plane; a mid value
// keeps frames realistic without making every render pay for 20000 Gaussians.
const defaultFaintPerDeg2 = 2500.0

// faintStars returns the synthetic stars within radiusDeg of a pointing. perDeg2 <= 0 disables the
// population entirely (the negative-means-none convention this package uses elsewhere).
func faintStars(centerRADeg, centerDecDeg, radiusDeg, perDeg2 float64) []deepstars.Star {
	if perDeg2 <= 0 || radiusDeg <= 0 {
		return nil
	}
	const degRad = math.Pi / 180
	sinD0, cosD0 := math.Sincos(centerDecDeg * degRad)
	cosR := math.Cos(radiusDeg * degRad)

	decLo := math.Max(-90, centerDecDeg-radiusDeg)
	decHi := math.Min(90, centerDecDeg+radiusDeg)

	var out []deepstars.Star
	for band := int(math.Floor(decLo / faintCellDeg)); band <= int(math.Floor(decHi/faintCellDeg)); band++ {
		decCell := float64(band) * faintCellDeg
		// Cells are widened in RA by 1/cos(dec) so every cell covers roughly the same area — without
		// it the density would climb towards the poles, where lines of RA converge.
		widthDeg := faintCellDeg / math.Max(0.02, math.Cos((decCell+faintCellDeg/2)*degRad))
		if widthDeg > 180 {
			widthDeg = 180
		}
		// How far in RA the search must reach at this declination.
		raSpan := raSpanDeg(centerDecDeg, decCell, radiusDeg)
		if raSpan <= 0 {
			continue
		}
		// One cell of margin each side: a cell whose ORIGIN sits outside the span can still hold a
		// star inside it, and a cell visited unnecessarily costs nothing but a discarded draw.
		loCol := int(math.Floor((centerRADeg - raSpan - widthDeg) / widthDeg))
		hiCol := int(math.Floor((centerRADeg + raSpan + widthDeg) / widthDeg))
		for col := loCol; col <= hiCol; col++ {
			out = appendCellStars(out, band, col, decCell, widthDeg, perDeg2,
				centerRADeg, sinD0, cosD0, cosR)
		}
	}
	return out
}

// raSpanDeg is the half-width in RA the field spans at a given declination band, from the spherical
// law of cosines rather than the small-angle shortcut. Getting this even slightly short means whole
// cells are never visited, and the same patch of sky then yields DIFFERENT stars depending on where
// the telescope was pointed — which defeats the entire purpose of generating from fixed cells.
func raSpanDeg(centerDec, decCell, radiusDeg float64) float64 {
	const degRad = math.Pi / 180
	// The declination within this cell closest to the field centre gives the widest RA reach.
	dec := math.Min(math.Max(centerDec, decCell), decCell+faintCellDeg)

	sinD0, cosD0 := math.Sincos(centerDec * degRad)
	sinD, cosD := math.Sincos(dec * degRad)
	den := cosD0 * cosD
	if den < 1e-9 {
		return 180 // at a pole, every line of RA is nearby
	}
	cosDeltaRA := (math.Cos(radiusDeg*degRad) - sinD0*sinD) / den
	if cosDeltaRA <= -1 {
		return 180
	}
	if cosDeltaRA >= 1 {
		return 0
	}
	return math.Acos(cosDeltaRA) / degRad
}

// appendCellStars generates one cell's stars and keeps those actually inside the field.
func appendCellStars(out []deepstars.Star, band, col int, decCell, widthDeg, perDeg2,
	centerRADeg, sinD0, cosD0, cosR float64) []deepstars.Star {
	const degRad = math.Pi / 180

	// Area of this cell in square degrees: width × height × cos(dec), the spherical correction.
	areaDeg2 := widthDeg * faintCellDeg * math.Cos((decCell+faintCellDeg/2)*degRad)
	expected := areaDeg2 * perDeg2
	if expected <= 0 {
		return out
	}

	rng := newCellRNG(band, col)
	// Round stochastically rather than truncating, so a cell expecting 0.4 stars produces one 40 %
	// of the time instead of never — otherwise sparse settings would yield an empty sky.
	n := int(expected)
	if rng.float64() < expected-float64(n) {
		n++
	}

	raBase := float64(col) * widthDeg
	for i := 0; i < n; i++ {
		ra := raBase + rng.float64()*widthDeg
		// Uniform in sin(dec) would be correct for a full sphere, but within a 0.25° cell the
		// difference is far below the noise, and uniform keeps the cell boundaries exact.
		dec := decCell + rng.float64()*faintCellDeg
		if dec > 90 || dec < -90 {
			continue
		}
		ra = math.Mod(ra, 360)
		if ra < 0 {
			ra += 360
		}
		// The magnitude is drawn BEFORE the in-field test, not after. Skipping the draw for a star
		// that falls outside would advance the stream by a different amount depending on where the
		// telescope happens to point — so the very next star in the cell would land somewhere else,
		// and the same patch of sky would silently render differently from a different pointing.
		mag := faintMagnitude(rng.float64())

		sinD, cosD := math.Sincos(dec * degRad)
		cosSep := sinD0*sinD + cosD0*cosD*math.Cos((ra-centerRADeg)*degRad)
		if cosSep < cosR {
			continue
		}
		out = append(out, deepstars.Star{RADeg: ra, DecDeg: dec, Mag: mag})
	}
	return out
}

// faintMagnitude draws a magnitude from a realistic luminosity function. Star counts grow roughly as
// 10^(k·m), so inverting that gives many more faint stars than bright ones — which is what makes a
// rendered field look right rather than like a uniform sprinkle.
func faintMagnitude(u float64) float64 {
	const k = 0.35 // counts multiply by about 2.2 per magnitude at these depths
	span := math.Pow(10, k*(faintMagMax-faintMagMin))
	return faintMagMin + math.Log10(1+u*(span-1))/k
}

// cellRNG is a small deterministic generator seeded from a cell's grid coordinates. splitmix64 is
// used because it needs no state beyond a single word, passes the usual statistical tests, and —
// the point here — gives identical output on every platform and every run.
type cellRNG struct{ state uint64 }

func newCellRNG(band, col int) *cellRNG {
	// Mix the two coordinates into one seed. The odd constants are splitmix64's, chosen so that
	// neighbouring cells produce completely unrelated streams rather than visibly similar fields.
	h := uint64(band)*0x9E3779B97F4A7C15 ^ uint64(col)*0xBF58476D1CE4E5B9
	return &cellRNG{state: h ^ 0x94D049BB133111EB}
}

func (r *cellRNG) next() uint64 {
	r.state += 0x9E3779B97F4A7C15
	z := r.state
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

func (r *cellRNG) float64() float64 {
	return float64(r.next()>>11) / float64(1<<53)
}
