// Package focus measures how well the telescope is focused, from the live frame, cheaply enough to
// run several times a second.
//
// The metric is HFD — half-flux diameter, the diameter of the circle containing half a star's
// light. It beats FWHM for this job for three reasons that all matter at the eyepiece: it stays
// meaningful when the star is a fat blur (FWHM needs a peak, and a badly defocused star barely has
// one), it uses every photon rather than the brightest pixels (so it is far steadier against
// noise), and it needs no curve fitting that could fail to converge while the user is turning a
// knob.
//
// What the measurement CANNOT do is tell you which way to turn. For an unobstructed refractor,
// inside and outside focus are optically symmetric — a star 200 µm inside focus looks exactly like
// one 200 µm outside. Any tool claiming otherwise from a single unmasked star is guessing. So the
// direction comes from history: turn the knob, and the meter says whether that helped.
package focus

import (
	"math"
	"sort"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// Options configure one measurement.
type Options struct {
	// ROIPx is the side of the centred square actually measured. Focusing does not need the whole
	// sensor, and a 1024² crop of a 16-megapixel frame is ~16× less work — which is what keeps the
	// meter light enough to run continuously.
	ROIPx int
	// MaxStars caps how many stars are measured; the median of a couple of dozen is already stable.
	MaxStars int

	PixelUm       float64 // sensor pixel pitch
	FocalMM       float64
	ApertureMM    float64
	ScaleArcsecPx float64
	// UmPerTurn converts a focuser distance into turns of the knob. Zero → turns are not reported.
	UmPerTurn float64
	// SeeingFloorPx is the best HFD this system could ever reach (seeing + optics). Used as the
	// reference before the session has seen anything sharper.
	SeeingFloorPx float64
}

func (o Options) withDefaults() Options {
	out := o
	if out.ROIPx <= 0 {
		out.ROIPx = 1024
	}
	if out.MaxStars <= 0 {
		out.MaxStars = 25
	}
	if out.SeeingFloorPx <= 0 {
		out.SeeingFloorPx = 3
	}
	return out
}

// Result is one focus reading.
type Result struct {
	HFDPx     float64
	HFDArcsec float64
	Stars     int
	Saturated bool
	Reliable  bool

	Score      float64 // 0–100
	BestHFDPx  float64
	DistanceUm float64
	Turns      float64
	Advice     string // "first" | "better" | "worse" | "at_focus" | "unreliable"

	// TiltCorners is the HFD in each corner of the frame — equal values mean the sensor is square
	// to the optical axis; a gradient means tilt. Empty when the corners had too few stars.
	TiltCorners []float64
}

// Advice values.
const (
	AdviceFirst      = "first"
	AdviceBetter     = "better"
	AdviceWorse      = "worse"
	AdviceSteady     = "steady"
	AdviceAtFocus    = "at_focus"
	AdviceUnreliable = "unreliable"
)

// The advice compares SMOOTHED readings, not consecutive frames. Seeing moves HFD several percent
// from one frame to the next, so a frame-to-frame comparison flips between "better" and "worse"
// while the focuser sits perfectly still — advice that is not just useless but actively misleading
// while someone is turning a knob. Comparing the median of the last few readings against the median
// of the ones before them answers the question actually being asked: "is it improving?"
const (
	adviceDeadband = 0.08 // fractional change that counts as real
	adviceWindow   = 3    // readings per side of the comparison
)

// atFocusTolerance is how close to the session's best HFD counts as "focused". Chasing the last few
// percent is chasing seeing, not focus.
const atFocusTolerance = 1.08

// Meter keeps the little history that makes direction advice possible: the best HFD the session has
// achieved, and what the previous reading was.
type Meter struct {
	mu      sync.Mutex
	best    float64
	history []float64 // recent reliable HFDs, oldest first
	lastAt  time.Time
}

// NewMeter starts a focus session.
func NewMeter() *Meter { return &Meter{} }

// Reset forgets the session history — used when the optics change (a filter swap shifts focus, a
// different camera changes everything).
func (m *Meter) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.best = 0
	m.history = nil
	m.lastAt = time.Time{}
}

// Measure evaluates one frame. pix is row-major 16-bit data of w×h.
func (m *Meter) Measure(pix []uint16, w, h int, opts Options) Result {
	o := opts.withDefaults()
	x0, y0, roiW, roiH := centreROI(w, h, o.ROIPx)
	im, saturated := imageFromROI(pix, w, x0, y0, roiW, roiH)

	stars := measureStars(im, o)
	res := Result{Stars: len(stars), Saturated: saturated}
	if len(stars) == 0 {
		res.Advice = AdviceUnreliable
		return m.finish(res, o)
	}
	sort.Float64s(stars)
	res.HFDPx = stars[len(stars)/2]
	res.HFDArcsec = res.HFDPx * o.ScaleArcsecPx
	// Two stars is not a measurement; a saturated star's flux is clipped, which biases HFD low and
	// would make the user chase a focus that is not there.
	res.Reliable = len(stars) >= 3 && !saturated && res.HFDPx > 0
	res.TiltCorners = cornerHFD(pix, w, h, o)
	return m.finish(res, o)
}

// finish folds the session history in: the score, the distance to focus, and the direction advice.
func (m *Meter) finish(res Result, o Options) Result {
	m.mu.Lock()
	defer m.mu.Unlock()

	if res.Reliable && (m.best == 0 || res.HFDPx < m.best) {
		m.best = res.HFDPx
	}
	res.BestHFDPx = m.best

	ref := o.SeeingFloorPx
	if m.best > 0 && m.best < ref {
		ref = m.best
	}
	if res.HFDPx > 0 && ref > 0 {
		res.Score = math.Min(100, 100*ref/res.HFDPx)
	}
	res.DistanceUm = DefocusUm(res.HFDPx, ref, o.PixelUm, fRatio(o))
	if o.UmPerTurn > 0 {
		res.Turns = res.DistanceUm / o.UmPerTurn
	}

	if res.Reliable {
		m.history = append(m.history, res.HFDPx)
		if len(m.history) > 2*adviceWindow {
			m.history = m.history[len(m.history)-2*adviceWindow:]
		}
		m.lastAt = time.Now()
	}
	res.Advice = m.adviceLocked(res, ref)
	return res
}

// adviceLocked turns the recent history into a direction hint. Caller holds m.mu.
func (m *Meter) adviceLocked(res Result, ref float64) string {
	switch {
	case !res.Reliable:
		return AdviceUnreliable
	case res.HFDPx <= ref*atFocusTolerance:
		return AdviceAtFocus
	case len(m.history) < adviceWindow+1:
		return AdviceFirst
	}
	recent := medianOf(m.history[len(m.history)-adviceWindow:])
	earlier := medianOf(m.history[:len(m.history)-adviceWindow])
	if earlier <= 0 {
		return AdviceFirst
	}
	switch {
	case math.Abs(recent-earlier) < adviceDeadband*earlier:
		return AdviceSteady
	case recent < earlier:
		return AdviceBetter
	default:
		return AdviceWorse
	}
}

func medianOf(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	cp := append([]float64(nil), vals...)
	sort.Float64s(cp)
	return cp[len(cp)/2]
}

// DefocusUm converts a measured HFD into how far the focuser is from focus.
//
// The geometry is exact: a defocus of Δ makes a blur circle of diameter Δ/N at the sensor, for a
// telescope of focal ratio N. Seeing and defocus add in quadrature, so subtracting the in-focus HFD
// that way is what makes the estimate honest near focus instead of blaming the atmosphere on the
// focuser.
func DefocusUm(hfdPx, focusedHFDPx, pixelUm, ratio float64) float64 {
	if hfdPx <= 0 || pixelUm <= 0 || ratio <= 0 {
		return 0
	}
	excess := hfdPx*hfdPx - focusedHFDPx*focusedHFDPx
	if excess <= 0 {
		return 0
	}
	return math.Sqrt(excess) * pixelUm * ratio
}

func fRatio(o Options) float64 {
	if o.ApertureMM <= 0 || o.FocalMM <= 0 {
		return 0
	}
	return o.FocalMM / o.ApertureMM
}

// centreROI is the centred measurement window, clipped to the frame.
func centreROI(w, h, side int) (x0, y0, outW, outH int) {
	if side > w {
		side = w
	}
	if side > h {
		side = h
	}
	return (w - side) / 2, (h - side) / 2, side, side
}

// imageFromROI copies a window into the float image the star detector expects, normalised to 0–1
// (the detector's saturation cut is expressed in those units).
func imageFromROI(pix []uint16, srcW, x0, y0, w, h int) (*fits.Image, bool) {
	im := fits.NewImage(w, h, 1)
	plane := im.Pix[0]
	saturated := false
	for y := 0; y < h; y++ {
		row := (y0 + y) * srcW
		for x := 0; x < w; x++ {
			v := pix[row+x0+x]
			if v >= 64000 {
				saturated = true
			}
			plane[y*w+x] = float32(v) / 65535
		}
	}
	return im, saturated
}
