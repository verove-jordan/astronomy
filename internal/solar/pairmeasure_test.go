package solar

import (
	"math"
	"strings"
	"testing"
)

// pairmeasure_test.go pins the per-frame measurements against the occulter.
//
// Each of these reads "the disc", and on a crescent most of the disc is Moon. The failures are not
// degradations — they are the measurement quietly answering a different question — so each test
// states the number rather than asserting a direction.

// TestFrameSharpness_KeepsTheOcculterAsAFocusProbe pins a conclusion that reversed under measurement.
//
// Masking the Moon out of the sharpness metric looks obviously right: most of what lies inside 0.85 R
// on a crescent is a flat, signal-free disc. It is wrong, and the fixtures say so plainly. The
// occulter's limb is an opaque body against the Sun — a true knife edge, with no limb darkening, no
// chromospheric skirt and no prominences — which makes it the cleanest probe of the system's blur
// anywhere in the frame. Keeping it separates a frame drawn at sigma 1.0 from one at 2.2 by a factor
// of about two at every obscuration. Masking it drops that to 1.35, and once the crescent thins past
// the point where it carries detail of its own, to 0.92 — an INVERSION, ranking the blurred frame
// first, which would have selection keep the worst frames in the clip.
func TestFrameSharpness_KeepsTheOcculterAsAFocusProbe(t *testing.T) {
	for _, sep := range []float64{0.9, 0.5, 0.2} {
		base := eclipsedSun(sep, 1.02)
		base.features = 40

		score := func(sigma float64) (float64, Pair) {
			s := base
			s.psfSigma = sigma
			im := drawSun(s)
			g, ok := FitPair(im)
			if !ok {
				t.Fatalf("sep %.2fR: the two-body fit refused the frame", sep)
			}
			return FrameSharpnessPair(im, g), g
		}
		sharp, g := score(1.0)
		soft, _ := score(2.2)
		ratio := sharp / soft
		t.Logf("obscuration %4.1f%%: sharp %.5f, soft %.5f, ratio %.2f", 100*g.Obscuration, sharp, soft, ratio)
		if ratio < 1.5 {
			t.Errorf("at %.0f%% obscuration a frame drawn at 1.0 px scores only %.2f times one at 2.2 px; "+
				"selection cannot separate them", 100*g.Obscuration, ratio)
		}
	}
}

// TestFrameSharpness_ScoreDoesNotDriftWithObscuration guards the normalisation.
//
// The score divides band-pass energy by the region's own median brightness, and if that median is
// taken across the whole disc it falls as the occultation deepens — inflating every frame's score for
// a reason that has nothing to do with focus. It matters because selection ranks a WHOLE CLIP at
// once: on the seventeen-minute clip that spans maximum, the drift would quietly prefer the deepest
// frames over the sharpest ones.
func TestFrameSharpness_ScoreDoesNotDriftWithObscuration(t *testing.T) {
	var scores []float64
	for _, sep := range []float64{1.2, 0.7, 0.3} {
		s := eclipsedSun(sep, 1.02)
		s.features = 40
		s.psfSigma = 1.4 // the same optics every time: any change in the score is drift
		im := drawSun(s)
		g, ok := FitPair(im)
		if !ok {
			t.Fatalf("sep %.2fR: the two-body fit refused the frame", sep)
		}
		v := FrameSharpnessPair(im, g)
		scores = append(scores, v)
		t.Logf("obscuration %4.1f%%: score %.5f", 100*g.Obscuration, v)
	}
	lo, hi := scores[0], scores[0]
	for _, v := range scores {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	if hi/lo > 1.6 {
		t.Errorf("the same optics scored %.5f..%.5f across obscurations (%.2fx); "+
			"the score is tracking the eclipse, not the focus", lo, hi, hi/lo)
	}
}

// TestDiscLevel_TheEclipseIsNotCloud is the measurement that would have cost most of the run.
//
// The transparency gate drops any frame whose level fell below 95% of the clip's clearest, on the
// reasoning that the Sun's surface brightness does not change so a drop means something got in the
// way. The Moon is something in the way, but it does not dim the Sun — it covers it. Measured across
// the whole disc the level tracks obscuration almost exactly, collapsing to the sky level.
func TestDiscLevel_TheEclipseIsNotCloud(t *testing.T) {
	clear := drawSun(defaultSun())
	clearPair, ok := FitPair(clear)
	if !ok {
		t.Fatal("the two-body fit refused an unoccluded frame")
	}
	reference := discLevelPair(clear, clearPair)

	for _, sep := range []float64{1.0, 0.5, 0.2, 0.07} {
		im := drawSun(eclipsedSun(sep, 1.02))
		g, ok := FitPair(im)
		if !ok {
			t.Fatalf("sep %.2fR: the two-body fit refused the frame", sep)
		}
		masked := discLevelPair(im, g) / reference
		whole := discLevel(im, g.Sun) / reference
		t.Logf("obscuration %4.1f%%: crescent level %.3f of clear | whole-disc level %.3f of clear",
			100*g.Obscuration, masked, whole)

		// The crescent's level does fall, and the reason is real: the surviving Sun lies closer and
		// closer to the limb, so LIMB DARKENING takes it down. What must not happen is the collapse
		// the whole-disc measurement shows, which is the occulter averaged in as though it were Sun.
		if masked < 0.5 {
			t.Errorf("sep %.2fR: crescent level fell to %.2f of clear — that is more than limb darkening",
				sep, masked)
		}
		if g.Obscuration > 0.6 && whole > 0.3 {
			t.Errorf("sep %.2fR: whole-disc level is %.2f of clear at %.0f%% obscuration; "+
				"the fixture is not reproducing the defect", sep, whole, 100*g.Obscuration)
		}
	}
}

// TestGateTransparency_LocalReferenceSurvivesTheEclipse is the fix for what the test above measures.
//
// Even read on the crescent alone, the level falls through an eclipse — by limb darkening, honestly
// and irreversibly. Judged against the whole clip's clearest minute, the deepest frames therefore
// fail a gate that exists to catch cloud. The two causes separate cleanly in TIME: cloud arrives and
// leaves in seconds, obscuration drifts over minutes and never reverses. So the reference is taken
// locally, and the gate then removes the cloud and nothing else.
func TestGateTransparency_LocalReferenceSurvivesTheEclipse(t *testing.T) {
	const (
		n         = 9000 // five minutes at 30 fps
		cloudFrom = 4000
		cloudTo   = 4600
		cloud     = cloudTo - cloudFrom
	)
	frames := make([]frameScan, n)
	for i := range frames {
		level := 1.0 - 0.35*float64(i)/float64(n) // limb darkening through the eclipse
		if i >= cloudFrom && i < cloudTo {
			level *= 0.88 // a cloud: fast, deep, and over
		}
		frames[i] = frameScan{index: i, ok: true, score: 1, level: level}
	}

	clipWide, noteWide := gateTransparency(frames, defaultTransparencyFloor, false)
	local, noteLocal := gateTransparency(frames, defaultTransparencyFloor, true)
	t.Logf("clip-wide reference kept %d of %d — %s", len(clipWide), n, strings.TrimSpace(noteWide))
	t.Logf("local reference     kept %d of %d — %s", len(local), n, strings.TrimSpace(noteLocal))

	if dropped := n - len(local); dropped < cloud/2 || dropped > cloud*2 {
		t.Errorf("local gate dropped %d frames for a %d-frame cloud", dropped, cloud)
	}
	for _, f := range local {
		if f.index >= cloudFrom && f.index < cloudTo {
			t.Errorf("local gate kept frame %d, which was behind the cloud", f.index)
			break
		}
	}
	// And the clip-wide gate must fail on this clip, or the local one is solving nothing.
	if n-len(clipWide) <= cloud*2 {
		t.Fatalf("fixture proves nothing: the clip-wide gate dropped only %d frames", n-len(clipWide))
	}
}

// TestVisibleSunMask_ExcludesBothBodiesEdges checks the masks agree with the geometry they came from.
func TestVisibleSunMask_ExcludesBothBodiesEdges(t *testing.T) {
	g, ok := FitPair(drawSun(eclipsedSun(0.3, 1.02)))
	if !ok {
		t.Fatal("the two-body fit refused the frame")
	}
	const w, h = 1400, 1200
	vis := g.VisibleSunMask(w, h, 0)
	occ := g.OccludedMask(w, h, 0)
	sky := g.SkyMask(w, h, 0)

	at := func(m []float32, x, y float64) float32 { return m[int(y)*w+int(x)] }
	if v := at(occ, g.Moon.CX, g.Moon.CY); v < 0.99 {
		t.Errorf("the occulter's centre reads %.2f in the occluded mask, want 1", v)
	}
	if v := at(vis, g.Moon.CX, g.Moon.CY); v > 0.01 {
		t.Errorf("the occulter's centre reads %.2f in the visible-Sun mask, want 0", v)
	}
	// The Moon is not sky, and the distinction is load-bearing: near maximum the occulter overhangs
	// the solar limb, and a background or a noise floor read there would be reading the Moon.
	if v := at(sky, g.Moon.CX, g.Moon.CY); v > 0.01 {
		t.Errorf("the occulter's centre reads %.2f in the sky mask, want 0", v)
	}
	far := math.Max(g.Sun.R, g.Moon.R) * 1.6
	if v := at(sky, g.Sun.CX-far, g.Sun.CY); v < 0.99 {
		t.Errorf("a point well outside both bodies reads %.2f in the sky mask, want 1", v)
	}
	dx, dy := g.Moon.CX-g.Sun.CX, g.Moon.CY-g.Sun.CY
	if d := math.Hypot(dx, dy); d > 1 {
		cx, cy := g.Sun.CX-0.75*g.Sun.R*dx/d, g.Sun.CY-0.75*g.Sun.R*dy/d
		if v := at(vis, cx, cy); v < 0.99 {
			t.Errorf("a point on the crescent reads %.2f in the visible-Sun mask, want 1", v)
		}
	}
}
