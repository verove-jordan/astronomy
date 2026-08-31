package meteor

// classify.go decides what each streak is.
//
// The whole method is one question — DOES IT HAPPEN AGAIN — and it is asked of the set, never of a
// single streak. Within one frame a meteor and a satellite look alike: both are straight, both are
// bright, both are a line of light that was not there before. Shape cannot separate them and neither
// can brightness. What separates them is the next frame.

import "math"

// Classify assigns a Class to every streak in place.
func Classify(ss []Streak, o Options) {
	for i := range ss {
		ss[i].Class, ss[i].Why = Meteor, "seen once"
	}
	for i := range ss {
		if ss[i].StraightnessPx > o.MaxStraightnessPx {
			ss[i].Class, ss[i].Why = HotPixel, "not straight enough to be anything that flew"
			continue
		}
		// A line that is lit only in patches is a strobe, and a strobe is an aircraft. Checked before
		// recurrence because a blinking track also recurs.
		if ss[i].Duty < o.MinDuty {
			ss[i].Class, ss[i].Why = Aircraft, "blinking: the line is lit only in patches"
			continue
		}
		if j, dist := recurrence(ss, i, o); j >= 0 {
			ss[i].Class, ss[i].Why = Satellite, "came back in a later frame, along its own track"
			_ = dist
			continue
		}
		// Nothing came back, so the trail is single-frame — which on a wide field at this cadence is
		// true of a satellite too, and recurrence cannot be asked to settle it. What is left is the
		// shape of the light: a trail that stops abruptly at full brightness was ended by the shutter,
		// not by burning out.
		if ss[i].Fullness > o.MaxFullness {
			ss[i].Class, ss[i].Why = Satellite, "flat-topped: it was still shining when the exposure ended"
			continue
		}
	}
}

// recurrence looks for the same object in a nearby frame: a streak roughly parallel to this one,
// displaced, within a few frames. It returns the partner's index and the displacement, or -1.
//
// Parallel AND displaced is the pair that matters. Parallel alone would convict two meteors from the
// same shower, which travel along the radiant's lines and really can be parallel — but they do not
// then appear again a frame later a fixed step further on.
func recurrence(ss []Streak, i int, o Options) (int, float64) {
	a := ss[i]
	ax, ay := a.Midpoint()
	for j := range ss {
		if j == i || ss[j].Frame == a.Frame {
			continue
		}
		gap := ss[j].Frame - a.Frame
		if gap < 0 {
			gap = -gap
		}
		if gap > o.MaxFrameGap {
			continue
		}
		if angleDiff(a.Angle(), ss[j].Angle()) > o.ParallelTolRad {
			continue
		}
		bx, by := ss[j].Midpoint()
		d := math.Hypot(bx-ax, by-ay)
		// It must have MOVED — but the bar is its WIDTH, not its length. A satellite crossing the
		// frame in 10 seconds draws a long trail and then moves less than that trail between one
		// exposure and the next, so asking for a displacement of half a trail length would acquit
		// most real satellites. What is being excluded here is a transient recurring in the SAME
		// place, which is a sensor defect rather than anything that flew.
		if d < 3*math.Max(a.WidthPx, 2) {
			continue
		}
		// And it must have moved ALONG ITS OWN TRACK. Parallel-and-displaced alone is far too weak to
		// convict: measured on a real panel it labelled 86 of 91 candidates satellites, because with a
		// handful of streaks per frame over an eight-frame window some pair is always roughly parallel
		// by chance. An object that actually flew on continues down the line it drew, so the partner's
		// offset PERPENDICULAR to that line stays small while the offset along it grows.
		ux, uy := math.Cos(a.Angle()), math.Sin(a.Angle())
		across := math.Abs(-(bx-ax)*uy + (by-ay)*ux)
		if across > o.TrackTolPx+0.1*d {
			continue
		}
		return j, d
	}
	return -1, 0
}

// angleDiff is the separation of two undirected angles, in [0, pi/2].
func angleDiff(a, b float64) float64 {
	d := math.Mod(math.Abs(a-b), math.Pi)
	if d > math.Pi/2 {
		d = math.Pi - d
	}
	return d
}

// Kept returns everything classified as a meteor. Use Confident to decide what to PAINT.
func Kept(ss []Streak) []Streak {
	var out []Streak
	for _, s := range ss {
		if s.Class == Meteor {
			out = append(out, s)
		}
	}
	return out
}

// Confident narrows a classification down to the streaks worth painting back into the picture.
//
// It exists because classification and compositing want different bars. A record of every candidate
// is worth having; blending is destructive, so the rule this package states — anything uncertain is
// dropped, the cost being a meteor missed rather than junk painted over the sky — has to be applied
// somewhere, and this is where.
//
// The cut is on LENGTH, and the reasoning is about the detector rather than about meteors. Its
// structuring element is 84 pixels long by default, so a component barely longer than that is the
// shortest thing it is capable of reporting; in a field where the median pixel already carries a
// linear ridge at 1.33 sigma, structures at exactly the detection scale are dominated by chance
// alignments. Asking for several times the element length asks for evidence the element cannot
// manufacture on its own. Measured on a real panel, this separated one continuous 643-pixel trail
// from 55 short fat patches that turned out, when the layer was rendered and looked at, to be visible
// chains of individual stars.
//
// It is length ALONE, plus a blob check. Length-to-width was tried as a second discriminator and had
// to be withdrawn: a streak's measured width comes from the morphology that found it rather than from
// the streak, so aspect is length over a constant and merely repeats the length cut — while
// penalising short trails twice. At 12 it threw away a confirmed 275-pixel meteor at aspect 11.1.
//
// The cost is real and is accepted knowingly: a meteor seen nearly end-on near its radiant is
// foreshortened and short, and this drops it.
func Confident(ss []Streak, o Options) []Streak {
	var out []Streak
	for _, s := range ss {
		if s.Class != Meteor {
			continue
		}
		if s.LengthPx < o.MinBlendLengthPx {
			continue
		}
		if s.WidthPx > 0 && s.LengthPx/s.WidthPx < o.MinBlendAspect {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Counts summarises a classification for a run's warnings.
func Counts(ss []Streak) map[Class]int {
	m := map[Class]int{}
	for _, s := range ss {
		m[s.Class]++
	}
	return m
}
