package solar

import (
	"math"
	"testing"
)

// pair_test.go pins the two-body fit against fixtures whose geometry is known exactly.
//
// The measurement that matters most here is not accuracy but STABILITY. A single-circle fit handed
// a crescent still returns a confident-looking answer; what makes it useless is that the answer
// jumps between the two bodies as the occultation deepens. So the headline test marches an occulter
// across the disc and asserts the solar circle never moves — a test that fails loudly for the exact
// defect this code exists to remove, and which a per-frame accuracy check would pass right through.

// eclipsedSun places an occulter of the given radius ratio at the given centre separation, along an
// axis deliberately off the pixel grid so no result can come out right by symmetry.
func eclipsedSun(sepFrac, radiusRatio float64) sunSpec {
	s := defaultSun()
	const axisDeg = 23.0
	a := axisDeg * math.Pi / 180
	d := sepFrac * s.r
	s.moonR = radiusRatio * s.r
	s.moonCX = s.cx + d*math.Cos(a)
	s.moonCY = s.cy + d*math.Sin(a)
	return s
}

// trueMoon is the occulter a spec describes, as a Limb.
func trueMoon(s sunSpec) Limb { return Limb{CX: s.moonCX, CY: s.moonCY, R: s.moonR} }

// trueSun is the disc a spec describes, as a Limb.
func trueSun(s sunSpec) Limb { return Limb{CX: s.cx, CY: s.cy, R: s.r} }

func TestFitPair_RecoversBothCirclesAcrossObscuration(t *testing.T) {
	// The separations span a shallow bite through to the thin crescent of the real 12 Aug clips,
	// where the two centres sit only a few percent of a radius apart.
	for _, sep := range []float64{1.0, 0.5, 0.2, 0.07} {
		s := eclipsedSun(sep, 1.02)
		im := drawSun(s)
		p, ok := FitPair(im)
		if !ok {
			t.Fatalf("sep %.2fR: FitPair refused a frame with a perfectly good limb", sep)
		}
		if !p.Eclipsed() {
			t.Fatalf("sep %.2fR: no occulter found, but one was drawn at (%.1f,%.1f) r=%.1f",
				sep, s.moonCX, s.moonCY, s.moonR)
		}
		const tol = 0.3
		checkCircle(t, "sun", sep, p.Sun, trueSun(s), tol)
		checkCircle(t, "moon", sep, p.Moon, trueMoon(s), tol)

		want := OverlapFraction(trueSun(s), trueMoon(s))
		if math.Abs(p.Obscuration-want) > 0.02 {
			t.Errorf("sep %.2fR: obscuration %.3f, want %.3f", sep, p.Obscuration, want)
		}
	}
}

func checkCircle(t *testing.T, name string, sep float64, got, want Limb, tol float64) {
	t.Helper()
	if d := math.Hypot(got.CX-want.CX, got.CY-want.CY); d > tol {
		t.Errorf("sep %.2fR: %s centre off by %.2f px (got %.2f,%.2f want %.2f,%.2f)",
			sep, name, d, got.CX, got.CY, want.CX, want.CY)
	}
	if d := math.Abs(got.R - want.R); d > tol {
		t.Errorf("sep %.2fR: %s radius off by %.2f px (got %.2f want %.2f)", sep, name, d, got.R, want.R)
	}
}

// TestFitPair_FullDiscFindsNoOcculter is the graceful-degradation contract: an ordinary solar frame
// must come out of the two-body fit exactly as it would have come out of FitLimb, so a mode that
// runs this on every frame can be pointed at a session that never had an eclipse in it.
func TestFitPair_FullDiscFindsNoOcculter(t *testing.T) {
	im := drawSun(defaultSun())
	p, ok := FitPair(im)
	if !ok {
		t.Fatal("FitPair refused a plain full disc")
	}
	if p.Eclipsed() {
		t.Errorf("invented an occulter on an unoccluded Sun: %+v", p.Moon)
	}
	if p.Obscuration != 0 {
		t.Errorf("obscuration %.4f on an unoccluded Sun, want 0", p.Obscuration)
	}
	l, ok := FitLimb(im)
	if !ok {
		t.Fatal("FitLimb refused the same frame")
	}
	if d := math.Hypot(p.Sun.CX-l.CX, p.Sun.CY-l.CY); d > 0.1 {
		t.Errorf("two-body fit disagrees with FitLimb by %.3f px on a full disc", d)
	}
	if d := math.Abs(p.Sun.R - l.R); d > 0.1 {
		t.Errorf("two-body radius disagrees with FitLimb by %.3f px on a full disc", d)
	}
}

// TestFitPair_DoesNotFlipAsTheOccultationDeepens is the reason this package exists.
//
// The single-circle fit is handed points from two circles and keeps whichever population survives
// its robust trim; which one that is changes as the occultation deepens, so the disc's measured
// scale — the number every registration is built on — steps between the two bodies partway through
// a clip. Here the SAME Sun is drawn every time and only the occulter moves, so any movement in the
// recovered solar circle is error, and a flip is a large one.
func TestFitPair_DoesNotFlipAsTheOccultationDeepens(t *testing.T) {
	seps := []float64{1.3, 1.1, 0.9, 0.7, 0.5, 0.35, 0.2, 0.12, 0.07}
	want := trueSun(eclipsedSun(1, 1.02))

	var pairWorst, singleWorst float64
	for _, sep := range seps {
		im := drawSun(eclipsedSun(sep, 1.02))
		p, ok := FitPair(im)
		if !ok {
			t.Fatalf("sep %.2fR: FitPair refused the frame", sep)
		}
		pairWorst = math.Max(pairWorst, circleError(p.Sun, want))
		if l, ok := FitLimb(im); ok {
			singleWorst = math.Max(singleWorst, circleError(l, want))
		}
	}
	if pairWorst > 0.3 {
		t.Errorf("solar circle wandered %.2f px as the occulter crossed; it must not move at all", pairWorst)
	}
	// Not a requirement on FitLimb, which was never asked to do this — it is here so the fixture is
	// proved to contain the defect. If the single-circle fit ever coped, this test would be passing
	// for the wrong reason and would stop protecting anything.
	if singleWorst <= pairWorst {
		t.Fatalf("fixture proves nothing: the single-circle fit was off by only %.2f px "+
			"against the two-body fit's %.2f px", singleWorst, pairWorst)
	}
	t.Logf("worst solar-circle error across the sequence: two-body %.2f px, single-circle %.2f px",
		pairWorst, singleWorst)
}

// circleError is how far a fitted circle sits from the truth, centre and radius together.
func circleError(got, want Limb) float64 {
	return math.Max(math.Hypot(got.CX-want.CX, got.CY-want.CY), math.Abs(got.R-want.R))
}

// TestFitPair_RefusesAnImplausibleOcculter guards the direction every gate in fitMoon fails towards.
// A wrong occulter is worse than none: it masks real Sun and exposes real Moon, and every later
// stage trusts the mask completely.
func TestFitPair_RefusesAnImplausibleOcculter(t *testing.T) {
	// An occulter half the Sun's size sitting entirely inside the disc is not a Moon — it is a
	// sunspot group, a dust mote, or a bad threshold. It must be refused on the radius ratio.
	s := defaultSun()
	s.moonR = 0.4 * s.r
	s.moonCX, s.moonCY = s.cx+0.2*s.r, s.cy
	p, ok := FitPair(drawSun(s))
	if !ok {
		t.Fatal("FitPair refused a frame that still has a full solar limb")
	}
	if p.Eclipsed() {
		t.Errorf("accepted an occulter at %.2f of the solar radius; the band is %.2f..%.2f",
			p.Moon.R/p.Sun.R, pairMoonRadiusLo, pairMoonRadiusHi)
	}
}

func TestOverlapFraction_MatchesTheKnownGeometry(t *testing.T) {
	sun := Limb{CX: 100, CY: 100, R: 50}
	for _, tc := range []struct {
		name string
		moon Limb
		want float64
	}{
		{"disjoint", Limb{CX: 300, CY: 100, R: 50}, 0},
		{"touching", Limb{CX: 200, CY: 100, R: 50}, 0},
		{"concentric equal", Limb{CX: 100, CY: 100, R: 50}, 1},
		{"concentric larger", Limb{CX: 100, CY: 100, R: 60}, 1},
		// Two equal circles one radius apart share 2/3π − √3/2 of a disc: the standard lens area.
		{"one radius apart", Limb{CX: 150, CY: 100, R: 50}, (2*math.Pi/3 - math.Sqrt(3)/2) / math.Pi},
	} {
		if got := OverlapFraction(sun, tc.moon); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("%s: overlap %.6f, want %.6f", tc.name, got, tc.want)
		}
	}
}
