package solar

import (
	"fmt"
	"math"
)

// panel.go brings the phases of an eclipse sequence into ONE frame.
//
// The panels come from different clips of a hand-held afocal rig, so each arrives at its own scale,
// its own roll, and — if the train has a diagonal — possibly its own handedness. Left alone they
// make a row of crescents facing arbitrary directions, which is not a sequence, it is a contact
// sheet. What ties them together is that the Moon's true position angle at each instant is KNOWN:
// comparing it with the direction measured in the picture gives the camera's roll for free, without
// plate solving anything, and the sign of the disagreement gives the handedness.
//
// The rotations involved are large — across this session the Moon swings 175° around the Sun — so
// this is not a refinement. Skipping it is the difference between an eclipse and a scatter of
// crescents.

// PanelFrame is one panel's measured geometry and what the sky says it should look like.
type PanelFrame struct {
	// Source names the clip, so panels that share a camera roll can be recognised.
	Source string
	// Sun and Moon are the circles fitted in this panel's own raster.
	Sun  Limb
	Moon Limb
	// SkyPADeg is the true position angle of the Moon's centre from the Sun's, North through East.
	SkyPADeg float64
	// SunRadiusArcsec and SepArcsec are what the sky says this instant looked like. They are what
	// lets a panel whose crescent is too thin to fit borrow its scale and its centre from the
	// geometry instead of guessing at them.
	SunRadiusArcsec float64
	SepArcsec       float64
	// ParallacticDeg locates the vertical at the Sun, which is the axis refraction squashes.
	ParallacticDeg float64
	// Flatten is the apparent vertical extent over the true one: 1 for a high Sun.
	Flatten float64
}

// measurable reports whether this panel carries the two circles the roll solve needs.
func (f PanelFrame) measurable() bool { return f.Sun.R > 0 && f.Moon.R > 0 }

// separation is how far apart the two centres are, in pixels. It is the precision of the direction
// this panel can report: the same centring error is a large angle when the bodies nearly coincide
// and a small one when they are a diameter apart.
func (f PanelFrame) separation() float64 {
	if !f.measurable() {
		return 0
	}
	return math.Hypot(f.Moon.CX-f.Sun.CX, f.Moon.CY-f.Sun.CY)
}

// ReconcileGeometry repairs the solar circle of the panels that could not measure their own.
//
// On a deep crescent the Sun is a sliver: its circle is fitted from a short arc, so both its radius
// and — worse — the position of its centre perpendicular to that arc are badly determined. Measured
// on the 12 Aug 2026 session, the panel at maximum reported 84% obscuration where the sky says 95%,
// and a scale taken from that fit blew the disc up until only a fragment of it landed on the sheet.
//
// Every ingredient of a better answer is already to hand, each from where it is strong:
//
//   - the plate scale is a property of the CLIP, so it is measured once per clip from the panel with
//     the longest solar arc and shared with the rest;
//   - the Moon's limb on a deep crescent is long and clean — an opaque body against the Sun — so its
//     centre is trustworthy exactly where the Sun's is not;
//   - the separation between the two centres is an ephemeris number, exact;
//   - the direction between them is the camera's roll, already smoothed across each clip.
//
// So a panel whose lunar arc beats its solar arc gets its centre placed from the Moon's, and every
// panel gets its radius from its clip. Panels that measured themselves well are left alone.
func ReconcileGeometry(frames []PanelFrame, orients []Orientation) []string {
	var notes []string
	scales := clipScales(frames)
	for i := range frames {
		f := &frames[i]
		if f.Sun.R <= 0 || f.SunRadiusArcsec <= 0 {
			continue
		}
		scale, ok := scales[f.Source]
		if !ok {
			continue
		}
		if want := f.SunRadiusArcsec / scale; want > 0 {
			if off := math.Abs(want-f.Sun.R) / f.Sun.R; off > scaleWarnFrac {
				notes = append(notes, fmt.Sprintf(
					"%s: a panel fitted its solar radius %.0f%% away from its clip's own plate scale, so the clip's was used",
					f.Source, off*100))
			}
			f.Sun.R = want
		}
		if !f.measurable() || f.Moon.ArcDeg <= f.Sun.ArcDeg || f.SepArcsec <= 0 {
			continue
		}
		theta := moonDirection(f.SkyPADeg, orients[i])
		sep := f.SepArcsec / scale
		f.Sun.CX = f.Moon.CX - sep*math.Cos(theta)
		f.Sun.CY = f.Moon.CY - sep*math.Sin(theta)
	}
	return notes
}

// scaleWarnFrac is how far a panel's own fitted radius may sit from its clip's before it is worth
// saying so. Below a tenth it is the ordinary spread of fitting a circle to an arc.
const scaleWarnFrac = 0.10

// clipScales measures arcseconds per pixel once per clip, from the panel whose solar limb was
// longest — the one whose circle the frame actually constrained.
func clipScales(frames []PanelFrame) map[string]float64 {
	bestArc := map[string]float64{}
	out := map[string]float64{}
	for _, f := range frames {
		if f.Sun.R <= 0 || f.SunRadiusArcsec <= 0 {
			continue
		}
		if arc, seen := bestArc[f.Source]; seen && arc >= f.Sun.ArcDeg {
			continue
		}
		bestArc[f.Source] = f.Sun.ArcDeg
		out[f.Source] = f.SunRadiusArcsec / f.Sun.R
	}
	return out
}

// moonDirection is the angle, in this panel's own raster, from the Sun's centre to the Moon's — the
// inverse of the roll solve, read back out once the roll is known.
func moonDirection(skyPADeg float64, o Orientation) float64 {
	theta := skyAngle(skyPADeg) - o.RollDeg*math.Pi/180
	if o.Mirrored {
		theta = math.Pi - theta
	}
	return theta
}

// moonAngle is the direction from the Sun's centre to the Moon's, in the panel's own raster:
// measured from +x toward +y, which is clockwise on screen because rows run downward.
func (f PanelFrame) moonAngle() float64 {
	return math.Atan2(f.Moon.CY-f.Sun.CY, f.Moon.CX-f.Sun.CX)
}

// Orientation is the solved placement of one panel in the common sky frame.
type Orientation struct {
	RollDeg  float64 `json:"roll_deg"`
	Mirrored bool    `json:"mirrored"`
	// Residual is how far this panel's roll sits from its clip's own mean, in degrees. A large
	// residual on a clip that should be steady means the geometry fit, not the camera, moved.
	Residual float64 `json:"residual_deg"`
	// Inherited marks a roll borrowed from a neighbour because this panel's occulter could not be
	// fitted — a shallow bite near contact, most often.
	Inherited bool `json:"inherited,omitempty"`
}

// SolveOrientation works out how each panel was rolled, and whether the optical train mirrors.
//
// Parity is solved ONCE for the whole set, not per panel, because it is a property of the telescope
// and cannot change between clips. It is recoverable at all only because the true position angle
// sweeps: a mirrored image runs the sweep backwards, so within a single clip that spans a large
// swing of the Moon's position angle the wrong handedness makes the "constant" camera roll rotate by
// twice that swing. Choosing the handedness whose rolls are steadiest within each clip therefore
// decides it outright — no knowledge of the optics required.
//
// Panels whose occulter could not be fitted carry no direction and are left at zero roll, flagged in
// the notes, since a shallow bite near contact is exactly where the two-circle fit gives up.
func SolveOrientation(frames []PanelFrame) ([]Orientation, []string) {
	out := make([]Orientation, len(frames))
	var notes []string

	mirrored, decided := solveParity(frames)
	if !decided {
		notes = append(notes, "no clip contributed two measurable panels, so the image handedness could not be measured; assuming the train does not mirror")
	}
	for i, f := range frames {
		if !f.measurable() {
			continue
		}
		out[i] = Orientation{RollDeg: rollFor(f, mirrored) * 180 / math.Pi, Mirrored: mirrored}
	}
	notes = append(notes, smoothRolls(frames, out)...)
	notes = append(notes, inheritRolls(frames, out, mirrored)...)
	return out, notes
}

// smoothRolls replaces each panel's roll with its clip's own, averaged over the panels that could
// measure it well.
//
// Roll belongs to the CAMERA, so within one clip it is one number that drifts slowly — but the
// per-panel estimate of it does not have one precision. It comes from the direction between the two
// centres, and near maximum those centres are barely thirty pixels apart, so a two-pixel error in a
// crescent's fitted centre is several degrees of roll. Measured on the first render of the 12 Aug
// 2026 session, three panels of the SAME clip reported 56.7°, 70.7° and 61.7°; the outlier was the
// one closest to maximum, exactly as the geometry predicts. Weighting by separation lets the panels
// that could see the angle decide it for the ones that could not.
func smoothRolls(frames []PanelFrame, out []Orientation) []string {
	byClip := map[string][]int{}
	for i, f := range frames {
		if f.measurable() {
			byClip[f.Source] = append(byClip[f.Source], i)
		}
	}
	var notes []string
	for clip, idx := range byClip {
		if len(idx) < 2 {
			continue
		}
		mean, ok := weightedMeanAngle(frames, out, idx)
		if !ok {
			continue
		}
		var worst float64
		for _, i := range idx {
			d := math.Abs(wrapPi(out[i].RollDeg*math.Pi/180-mean)) * 180 / math.Pi
			if d > worst {
				worst = d
			}
			out[i].RollDeg = mean * 180 / math.Pi
			out[i].Residual = d
		}
		if worst > rollScatterWarnDeg {
			notes = append(notes, fmt.Sprintf(
				"%s: its panels disagreed about the camera's roll by up to %.1f°, so they share the one the widest-separated of them measured", clip, worst))
		}
	}
	return notes
}

// rollScatterWarnDeg is how much per-panel disagreement is worth mentioning. Below a few degrees it
// is the ordinary noise of fitting a circle to a crescent.
const rollScatterWarnDeg = 4.0

// weightedMeanAngle averages the rolls of one clip's panels, weighting each by the separation that
// produced it.
func weightedMeanAngle(frames []PanelFrame, out []Orientation, idx []int) (float64, bool) {
	var sx, sy, total float64
	for _, i := range idx {
		w := frames[i].separation()
		if w <= 0 {
			continue
		}
		a := out[i].RollDeg * math.Pi / 180
		sx += w * math.Cos(a)
		sy += w * math.Sin(a)
		total += w
	}
	if total == 0 || (sx == 0 && sy == 0) {
		return 0, false
	}
	return math.Atan2(sy, sx), true
}

// inheritRolls gives a panel with no measurable occulter the roll of its nearest neighbour.
//
// Zero would be the easy answer and it is the worst one: an unrotated panel in a sequence of
// straightened ones is not a small error, it is a crescent facing an arbitrary direction, and it
// lands on the panels the sequence most wants — the first bite and the last, where the occulter is a
// shallow dent the two-circle fit is entitled to miss.
//
// The neighbour is the right estimate because roll belongs to the CAMERA, not to the phase: the same
// clip is the same seating of the phone on the eyepiece, so its roll drifts by degrees, not tens of
// them. A neighbour from another clip is a weaker guess and is reported as one, but it is still an
// observation of how this rig was held rather than an assumption that it was held level.
func inheritRolls(frames []PanelFrame, out []Orientation, mirrored bool) []string {
	var notes []string
	for i, f := range frames {
		if f.measurable() {
			continue
		}
		out[i] = Orientation{Mirrored: mirrored}
		j, sameClip := nearestMeasurable(frames, i)
		if j < 0 {
			notes = append(notes, fmt.Sprintf(
				"%s: no occulter fitted and no other panel to borrow an orientation from, so it keeps the camera's own", f.Source))
			continue
		}
		out[i].RollDeg = out[j].RollDeg
		out[i].Inherited = true
		where := "another clip"
		if sameClip {
			where = "the same clip"
		}
		notes = append(notes, fmt.Sprintf(
			"%s: no occulter could be fitted, so this panel takes its orientation from the nearest panel of %s", f.Source, where))
	}
	return notes
}

// nearestMeasurable walks outward from i, preferring a panel from the same clip.
func nearestMeasurable(frames []PanelFrame, i int) (int, bool) {
	best, bestSame := -1, false
	for d := 1; d < len(frames); d++ {
		for _, j := range [...]int{i - d, i + d} {
			if j < 0 || j >= len(frames) || !frames[j].measurable() {
				continue
			}
			if frames[j].Source == frames[i].Source {
				return j, true
			}
			if best < 0 {
				best = j
			}
		}
	}
	return best, bestSame
}

// rollFor returns the rotation, in radians, that takes this panel's measured Moon direction onto the
// one the sky says it should have, in the output convention: North up, East left.
func rollFor(f PanelFrame, mirrored bool) float64 {
	theta := f.moonAngle()
	if mirrored {
		theta = math.Pi - theta
	}
	return wrapPi(skyAngle(f.SkyPADeg) - theta)
}

// skyAngle maps a position angle (North through East) into the output raster's own angle
// convention. North is up, so it is -y; East is left, so it is -x.
func skyAngle(paDeg float64) float64 {
	pa := paDeg * math.Pi / 180
	return math.Atan2(-math.Cos(pa), -math.Sin(pa))
}

// solveParity picks the handedness whose per-clip rolls are the most consistent.
func solveParity(frames []PanelFrame) (mirrored, decided bool) {
	direct, direcOK := rollConsistency(frames, false)
	flipped, flipOK := rollConsistency(frames, true)
	if !direcOK || !flipOK {
		return false, false
	}
	return flipped > direct, true
}

// rollConsistency scores a handedness by how tightly each clip's rolls cluster, weighted by how many
// panels that clip contributed. Clips with a single panel say nothing about consistency and are
// skipped; ok=false means no clip could speak.
func rollConsistency(frames []PanelFrame, mirrored bool) (score float64, ok bool) {
	byClip := map[string][]float64{}
	for _, f := range frames {
		if !f.measurable() {
			continue
		}
		byClip[f.Source] = append(byClip[f.Source], rollFor(f, mirrored))
	}
	for _, rolls := range byClip {
		if len(rolls) < 2 {
			continue
		}
		score += float64(len(rolls)) * resultantLength(rolls)
		ok = true
	}
	return score, ok
}

// resultantLength is the mean resultant of a set of angles: 1 when they agree exactly, 0 when they
// are spread evenly around the circle. It is the right average for angles, where a plain standard
// deviation would call 359° and 1° almost a full turn apart.
func resultantLength(angles []float64) float64 {
	var sx, sy float64
	for _, a := range angles {
		sx += math.Cos(a)
		sy += math.Sin(a)
	}
	return math.Hypot(sx, sy) / float64(len(angles))
}

// fillResiduals records how far each panel's roll sits from its own clip's mean. Retained for clips
// smoothRolls left alone.
func fillResiduals(frames []PanelFrame, out []Orientation) {
	sums := map[string][]float64{}
	for i, f := range frames {
		if f.measurable() {
			sums[f.Source] = append(sums[f.Source], out[i].RollDeg*math.Pi/180)
		}
	}
	means := map[string]float64{}
	for clip, rolls := range sums {
		var sx, sy float64
		for _, a := range rolls {
			sx += math.Cos(a)
			sy += math.Sin(a)
		}
		means[clip] = math.Atan2(sy, sx)
	}
	for i, f := range frames {
		if !f.measurable() {
			continue
		}
		d := wrapPi(out[i].RollDeg*math.Pi/180 - means[f.Source])
		out[i].Residual = math.Abs(d) * 180 / math.Pi
	}
}

func wrapPi(a float64) float64 {
	for a > math.Pi {
		a -= 2 * math.Pi
	}
	for a < -math.Pi {
		a += 2 * math.Pi
	}
	return a
}
