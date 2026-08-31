// Package meteor finds the streaks in the layer the sky stack rejected, and decides which ones are
// worth keeping.
//
// A meteor is, to a sigma-clipped stack, indistinguishable from a defect: it is bright, it is in one
// frame, and it is nothing like what that pixel does in the others. The clip is right to keep it out
// of the average and wrong to discard it, so nightscape now hands over what it rejected and this
// package sorts it out.
//
// The sorting rests on ONE physical fact, and everything else here is support for it: a meteor
// happens once. A satellite comes back — the next frame shows it again, displaced along its orbit,
// parallel to where it was. An aircraft comes back too, and blinks while it does. So the question
// asked of every streak is not "what does this look like" but "does it happen again", which is a
// question about the SET of streaks and cannot be answered one streak at a time. That is why Detect
// classifies the whole layer at once and not each component as it is found.
package meteor

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/trail"
)

// Class is what a streak was decided to be.
type Class string

const (
	// Meteor happens once, in one frame, and is kept.
	Meteor Class = "meteor"
	// Satellite returns in a later frame, displaced and parallel. Dropped.
	Satellite Class = "satellite"
	// Aircraft returns AND blinks: a run of short colinear marks in one frame. Dropped.
	Aircraft Class = "aircraft"
	// HotPixel is small, round and repeats at the same place across frames. Dropped.
	HotPixel Class = "hot_pixel"
)

// Streak is one transient found in the rejected layer.
type Streak struct {
	// The fitted segment, in the layer's pixel coordinates.
	X1, Y1, X2, Y2 float64
	// LengthPx along the major axis, WidthPx across it.
	LengthPx, WidthPx float64
	// Straightness is the RMS distance of the streak's pixels from the fitted line. A meteor and a
	// satellite are both straight; a cloud edge or a registration artefact is not.
	StraightnessPx float64
	// Frame is which frame it came from, and Pixels how many pixels it covers.
	Frame, Pixels int
	// PeakExcess is how far above the clip its brightest pixel sat.
	PeakExcess float64
	// Duty is the fraction of the line's length that is actually lit. A meteor burns continuously
	// and reads near 1; an aircraft's strobe leaves gaps and reads far lower, and so does a chance
	// alignment of stars, which is lit at the stars and dark in between.
	Duty float64
	// Fullness is the fraction of the trail that sits above half its own brightness — the shape of the
	// light along it, rather than how much of it is lit.
	//
	// This is what has to separate a meteor from a satellite ON THIS DATA, and the reason is a
	// measured one that overturns the original plan. That plan discriminated by recurrence: a
	// satellite comes back in the next frame, a meteor does not. It does not hold here. The brightest
	// trail measured on panel 8 is 637 px, which at 64 arcsec per pixel is 11.3 degrees in a 10-second
	// exposure — 1.1 degrees per second. At a 43-second cadence that object has moved about 49 degrees
	// by the next frame and is simply gone. Recurrence can only convict the SLOW, high-orbit minority;
	// for everything else the streak is single-frame whatever it is, exactly like a meteor.
	//
	// What still separates them is why the trail ENDS. A satellite's ends are where the shutter opened
	// and closed, so it is flat-topped and stops abruptly at full brightness. A meteor's ends are
	// where it lit up and burned out, so it rises and fades and often flares. A flat-top reads near 1
	// and a tapered trail near a half.
	Fullness float64
	// Class and Why record the decision and the reason, so a dropped meteor can be argued with.
	Class Class
	Why   string
}

// Angle is the streak's direction in radians, in [0, pi).
func (s Streak) Angle() float64 {
	return math.Mod(math.Atan2(s.Y2-s.Y1, s.X2-s.X1)+2*math.Pi, math.Pi)
}

// Midpoint is the streak's centre.
func (s Streak) Midpoint() (x, y float64) { return (s.X1 + s.X2) / 2, (s.Y1 + s.Y2) / 2 }

// Layer is what nightscape's clip rejected: per pixel the brightest rejected frame's excess, which
// frame it was, and how many frames were rejected there.
type Layer struct {
	W, H   int
	Excess []float32
	Frame  []int32
	Count  []int32
}

// Options tune detection. The zero value is not usable; call DefaultOptions.
type Options struct {
	// SeedK scales the Hough seed threshold (internal/trail, Residual mode).
	SeedK float64
	// NoiseQuantile and MemberMargin decide which pixels belong to a line the detector has already
	// found: excess above MemberMargin times that quantile of the non-zero excesses.
	//
	// Two rejected approaches, both measured on a real 31-frame panel where the sigma-clip rejected
	// SOMETHING at 38% of all pixels (p50 0.0002, p99 0.003, p99.9 0.014, max 0.55):
	//
	// median + k*MAD is the obvious choice and is wrong, because the rejected population is not a
	// sparse set of events over a noise floor — it IS mostly noise, so its MAD describes the noise
	// rather than the gap above it. That cut landed near p97 and labelled 3135 "streaks", not one of
	// them real.
	//
	// A HIGH quantile is worse still: it works on that panel and silently fails on a smaller frame,
	// because p99.9 sits inside the events themselves as soon as they exceed a thousandth of the
	// rejected pixels. The threshold then climbs above the very streaks it is looking for.
	//
	// A LOW quantile cannot be reached by events — they will never be a tenth of everything the clip
	// threw away — so it measures the noise whatever the frame size or the number of meteors, and the
	// margin above it is what separates them.
	NoiseQuantile float64
	MemberMargin  float64
	// MinPixels and MinLengthPx reject the specks: hot pixels and single-frame noise.
	MinPixels   int
	MinLengthPx float64
	// MaxStraightnessPx rejects components that are long but not straight — cloud edges, the drift
	// rim, registration seams.
	MaxStraightnessPx float64
	// ParallelTolRad and the frame gap define "it came back": a later frame showing a streak within
	// this angle of the first is the same object moving, not a second meteor that happened to fly
	// the same way.
	ParallelTolRad float64
	MaxFrameGap    int
	// TrackTolPx is how far off its own line the later trail may sit and still count as the same
	// object continuing along it. It grows with the displacement, so a distant partner is allowed
	// proportionally more slack than a near one.
	TrackTolPx float64
	// MaxFullness is the flat-topped-ness above which a single-frame trail is read as a satellite
	// rather than a meteor. PROVISIONAL: the principle is measured but the number rests on a handful
	// of real trails, so it is set where a tapering meteor cannot fall rather than where the two
	// populations are best separated.
	MaxFullness float64
	// RepeatCountFrames marks a pixel rejected in this many frames as a fixed defect rather than
	// anything that flew.
	RepeatCountFrames int
	// MinDuty is the lit fraction below which a line is read as blinking. PROVISIONAL: the principle
	// is solid but the number has not been calibrated against a real aircraft, only against synthetic
	// strobes, so it is set where a continuous meteor cannot fall.
	//
	// Note that duty does NOT separate a meteor from a chance chain of stars on a wide-field frame of
	// the galactic plane, which is what it was hoped to do. At 64 arcsec per pixel there is no dark
	// between the stars: the confusion background sits above any sensible "is this lit" threshold, and
	// measured on a real panel the star chains scored 0.64 to 0.92 against a real trail's 0.97. That
	// job belongs to Confident, which cuts on size instead.
	MinDuty float64
	// MinBlendLengthPx is the bar a streak must clear to be PAINTED rather than merely recorded. See
	// Confident for why the cut is on length and what it costs.
	MinBlendLengthPx float64
	// MinBlendAspect only rejects blobs. It is deliberately NOT a discriminator, because on this
	// detector aspect is not scale-invariant and a strict value silently throws real meteors away.
	//
	// Measured on two confirmed trails: 643 px long by 28.7 wide, and 275 px long by 24.8 wide. The
	// WIDTHS are the same — a streak's measured width is set by the morphology that found it (the
	// 3x3 thicken, then the bridging dilation), not by the streak. So aspect is very nearly length
	// divided by a constant: it says nothing MinBlendLengthPx has not already said, and it says it in
	// a way that penalises short trails twice. Set at 12 it rejected the 275-px trail at aspect 11.1
	// — a bright, straight, visibly tapered meteor, and the strongest linear structure in its frame.
	MinBlendAspect float64
}

func DefaultOptions() Options {
	return Options{
		SeedK: 3, NoiseQuantile: 0.9, MemberMargin: 4,
		MinPixels: 40, MinLengthPx: 120, MaxStraightnessPx: 6,
		ParallelTolRad: 12 * math.Pi / 180, MaxFrameGap: 4,
		TrackTolPx: 30, MaxFullness: 0.9,
		RepeatCountFrames: 3, MinDuty: 0.5,
		MinBlendLengthPx: 250, MinBlendAspect: 4,
	}
}

// Detect finds every streak in the layer and classifies the set.
//
// It looks for LINES, not for bright blobs, and that is the whole difference between finding meteors
// and finding stars. Measured on a real panel: the rejected layer is dominated by star-trail residue,
// because the sigma-clip nips the moving edge of every trailed star in the field, and those specks are
// BRIGHTER than the meteors. Any brightness threshold that reaches a meteor has already marked
// thousands of stars — one threshold pass returned 3135 "streaks", not one of them real. What a meteor
// has and a star does not is LENGTH: it crosses a large part of the frame in a straight line. So the
// primitive is the Hough detector internal/trail already uses to find faint satellite trails.
func Detect(l Layer, o Options) []Streak {
	if l.W < 32 || l.H < 32 || len(l.Excess) != l.W*l.H {
		return nil
	}
	var out []Streak
	for _, seg := range trail.DetectSegments(l.Excess, l.W, l.H, trail.DefaultParams(o.SeedK)) {
		s, ok := fromSegment(l, seg, o)
		if !ok {
			continue
		}
		out = append(out, s)
	}
	Classify(out, o)
	return out
}

// fromSegment turns a detected line into a Streak by measuring the pixels that actually lit it.
//
// The line is infinite and the extent the detector reports is generous, so the pixels are re-measured
// here: which frame they came from, how far along the line they really run, and how bright they got.
// The frame is the point of it — a line with no frame cannot be told from a satellite later.
func fromSegment(l Layer, seg trail.Segment, o Options) (Streak, bool) {
	cut := lowCut(l, o)
	frames := map[int32]int{}
	minT, maxT := math.Inf(1), math.Inf(-1)
	peak, n := 0.0, 0
	lit := map[int]bool{} // which whole-pixel steps along the line carry signal
	// Walk only the band around the line, not the whole frame.
	for y := 0; y < l.H; y++ {
		for x := 0; x < l.W; x++ {
			if !seg.Contains(float64(x), float64(y)) {
				continue
			}
			i := y*l.W + x
			e := float64(l.Excess[i])
			if e < cut {
				continue
			}
			t := -seg.Ny*float64(x) + seg.Nx*float64(y)
			minT, maxT = math.Min(minT, t), math.Max(maxT, t)
			lit[int(math.Round(t))] = true
			if e > peak {
				peak = e
			}
			if f := l.Frame[i]; f >= 0 {
				frames[f]++
			}
			n++
		}
	}
	if n < o.MinPixels || math.IsInf(minT, 1) {
		return Streak{}, false
	}
	length := maxT - minT
	if length < o.MinLengthPx {
		return Streak{}, false
	}
	duty := 1.0
	if length > 0 {
		duty = math.Min(float64(len(lit))/length, 1)
	}
	// A point on the line at parameter t: the closest point to the origin plus t along the direction.
	dx, dy := -seg.Ny, seg.Nx
	px, py := seg.Nx*seg.C, seg.Ny*seg.C
	return Streak{
		X1: px + minT*dx, Y1: py + minT*dy,
		X2: px + maxT*dx, Y2: py + maxT*dy,
		LengthPx: length, WidthPx: math.Max(seg.Width, 1),
		StraightnessPx: seg.Width / 2, // a Hough line is straight by construction
		Pixels:         n, PeakExcess: peak, Duty: duty, Frame: modeFrame(frames),
	}, true
}

// lowCut is the modest threshold used only to decide which pixels belong to a line the detector has
// ALREADY found. It can be low precisely because the line is known: the question here is extent and
// provenance, not existence.
func lowCut(l Layer, o Options) float64 {
	v := make([]float32, 0, len(l.Excess)/4)
	for _, e := range l.Excess {
		if e > 0 {
			v = append(v, e)
		}
	}
	if len(v) < 64 {
		return 0
	}
	sort.Slice(v, func(a, b int) bool { return v[a] < v[b] })
	nq := math.Min(math.Max(o.NoiseQuantile, 0.05), 0.99)
	return float64(v[int(nq*float64(len(v)-1))]) * o.MemberMargin
}

func modeFrame(m map[int32]int) int {
	best, bestN := -1, 0
	for f, n := range m {
		if n > bestN {
			best, bestN = int(f), n
		}
	}
	return best
}
