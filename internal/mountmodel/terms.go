package mountmodel

import (
	"fmt"
	"math"
)

// The corrections that describe how a real mount differs from a perfect one.
//
// This is the classical equatorial pointing model — the same six terms every serious mount-analysis
// program fits, in the same form — and the reason to use it rather than something invented here is
// that each term is a physical thing that can be measured with a spanner. A fit that comes back
// saying ME is a quarter of a degree is telling the observer their polar alignment is a quarter of a
// degree out in elevation, which is actionable in a way that an opaque set of coefficients is not.
//
// All six are angles in degrees, and all six are SMALL — a mount needing a whole degree of collimation
// correction has something loose. The model is linear in them, which is what makes the fit a plain
// least-squares solve with no iteration and no starting guess.

// Terms are the six correction angles, in degrees.
type Terms struct {
	// IH is the hour-angle index error: the RA shaft's zero being in the wrong place. Pure offset,
	// affects every star equally, and is the one term a single centred star can determine.
	IH float64 `json:"ih"`
	// ID is the declination index error, the same idea on the other shaft.
	ID float64 `json:"id"`
	// CH is collimation, or cone error: the optical axis not square to the declination axis. It
	// pushes stars along hour angle by an amount that grows towards the pole, which is why it cannot
	// be separated from IH using stars that all sit at one declination.
	CH float64 `json:"ch"`
	// NP is the declination axis not being square to the polar axis.
	NP float64 `json:"np"`
	// MA is polar misalignment in azimuth — the polar axis pointing east or west of the true pole.
	MA float64 `json:"ma"`
	// ME is polar misalignment in elevation — the polar axis too high or too low. On a mount that has
	// been carefully polar aligned both of these should come back near zero, and a large one is the
	// fit telling you the mise en station is worth redoing.
	ME float64 `json:"me"`
}

// String renders the terms in arcminutes, which is the unit they are actually judged in.
func (t Terms) String() string {
	return fmt.Sprintf("IH %+.1f′ ID %+.1f′ CH %+.1f′ NP %+.1f′ MA %+.1f′ ME %+.1f′",
		t.IH*60, t.ID*60, t.CH*60, t.NP*60, t.MA*60, t.ME*60)
}

// PolarErrorArcmin is how far the polar axis misses the pole, in arcminutes — the number that says
// whether the mise en station needs redoing.
func (t Terms) PolarErrorArcmin() float64 {
	return math.Hypot(t.MA, t.ME) * 60
}

// maxPoleFactor caps the sec/tan blow-up near the pole.
//
// Three of the terms carry a sec(dec) or tan(dec) factor, and both run to infinity at the pole. This
// is not academic: the alignment planner cheerfully recommends Polaris as its first star, and at
// declination 89.4° tan(dec) is about 90. Left unbounded, one circumpolar star would contribute a
// row a hundred times heavier than every other star combined, and the solve would fit that star
// perfectly and the rest not at all. Capping at 10 corresponds to about 84° — beyond that the
// geometry genuinely cannot separate these terms, and pretending otherwise produces a confident
// wrong answer rather than a cautious right one.
const maxPoleFactor = 10.0

// poleFactors returns the bounded sec(dec) and tan(dec) used by the model.
func poleFactors(decDeg float64) (sec, tan float64) {
	c := math.Cos(rad(decDeg))
	if math.Abs(c) < 1/maxPoleFactor {
		c = math.Copysign(1/maxPoleFactor, c)
	}
	sec = 1 / c
	tan = math.Sin(rad(decDeg)) / c
	return sec, tan
}

// correction is how far the true sky position sits from where the ideal geometry says the shafts are
// pointing.
func (t Terms) correction(haDeg, decDeg float64) (dHADeg, dDecDeg float64) {
	sec, tan := poleFactors(decDeg)
	sinH, cosH := math.Sin(rad(haDeg)), math.Cos(rad(haDeg))
	dHADeg = t.IH + t.CH*sec + t.NP*tan - t.MA*cosH*tan + t.ME*sinH*tan
	dDecDeg = t.ID + t.MA*sinH + t.ME*cosH
	return dHADeg, dDecDeg
}

// termBasis is the correction above written as the coefficient of each term, which is what the fit
// needs: the model is linear, so each equation is just these numbers dotted with the six unknowns.
// Keeping it beside correction is deliberate — the two must agree exactly, and separating them is
// how a pointing model comes to be fitted for one thing and evaluated as another.
func termBasis(haDeg, decDeg float64) (haRow, decRow [6]float64) {
	sec, tan := poleFactors(decDeg)
	sinH, cosH := math.Sin(rad(haDeg)), math.Cos(rad(haDeg))
	//        IH   ID  CH    NP    MA            ME
	haRow = [6]float64{1, 0, sec, tan, -cosH * tan, sinH * tan}
	decRow = [6]float64{0, 1, 0, 0, sinH, cosH}
	return haRow, decRow
}

// termsFromVector rebuilds Terms from the solved parameter vector.
func termsFromVector(v [6]float64) Terms {
	return Terms{IH: v[0], ID: v[1], CH: v[2], NP: v[3], MA: v[4], ME: v[5]}
}
