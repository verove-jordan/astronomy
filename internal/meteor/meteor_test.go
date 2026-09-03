package meteor

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// layerBuilder paints synthetic streaks into a rejected layer.
type layerBuilder struct{ l Layer }

func newLayer(w, h int) *layerBuilder {
	return &layerBuilder{Layer{W: w, H: h,
		Excess: make([]float32, w*h), Frame: make([]int32, w*h), Count: make([]int32, w*h)}}
}

// streak draws a line of half-width 1 from (x1,y1) to (x2,y2), attributed to one frame.
func (b *layerBuilder) streak(x1, y1, x2, y2 float64, frame int, peak float32) *layerBuilder {
	n := int(math.Hypot(x2-x1, y2-y1)) * 3
	for k := 0; k <= n; k++ {
		t := float64(k) / float64(n)
		cx, cy := x1+t*(x2-x1), y1+t*(y2-y1)
		for dy := -1; dy <= 1; dy++ {
			for dx := -1; dx <= 1; dx++ {
				x, y := int(cx)+dx, int(cy)+dy
				if x < 0 || y < 0 || x >= b.l.W || y >= b.l.H {
					continue
				}
				i := y*b.l.W + x
				if b.l.Excess[i] < peak {
					b.l.Excess[i] = peak
					b.l.Frame[i] = int32(frame)
					b.l.Count[i] = 1
				}
			}
		}
	}
	return b
}

// noise gives the layer the floor a real one has, and the proportions matter. Measured on a real
// 31-frame panel the sigma-clip rejects SOMETHING at 38% of pixels, with p50 0.0002, p99 0.003 and
// p99.9 0.014 — so events are a few thousandths of a per cent of the rejected population, not a few
// per cent. A fixture with too few noise pixels puts the distribution's tail ON the streak and hides
// exactly the failure this threshold has to survive.
func (b *layerBuilder) noise() *layerBuilder {
	for i := range b.l.Excess {
		if b.l.Excess[i] != 0 || i%5 == 0 {
			continue // ~40% of pixels carry a small rejection, the rest none at all
		}
		e := 0.0002 * float32(1+i%3)
		if i%977 == 0 {
			e = 0.01 // the tail: a few pixels rejected much harder than the rest
		}
		b.l.Excess[i] = e
		b.l.Frame[i] = int32(i % 5)
		b.l.Count[i] = 1
	}
	return b
}

func TestDetect_FindsAStreakAndItsShape(t *testing.T) {
	l := newLayer(400, 400).streak(100, 100, 300, 200, 4, 0.5).noise().l

	got := Detect(l, DefaultOptions())

	require.Len(t, got, 1)
	s := got[0]
	assert.InDelta(t, math.Hypot(200, 100), s.LengthPx, 8, "length")
	assert.Equal(t, 4, s.Frame)
	assert.Greater(t, s.LengthPx/s.WidthPx, 3.0, "a streak is long and thin")
	assert.InDelta(t, math.Atan2(100, 200), s.Angle(), 0.05, "direction")
}

// TestDetect_ASingleStreakIsAMeteor is the whole point: it happened once, so it is kept.
func TestDetect_ASingleStreakIsAMeteor(t *testing.T) {
	l := newLayer(400, 400).streak(80, 80, 260, 190, 3, 0.6).noise().l

	got := Detect(l, DefaultOptions())

	require.Len(t, got, 1)
	assert.Equal(t, Meteor, got[0].Class)
	assert.Len(t, Kept(got), 1)
}

// The recurrence rule is tested on the classifier directly rather than through a detector, because
// two trails that lie on ONE line are one line: any line detector returns them as a single segment,
// which is correct of it and useless for testing what happens to a pair.
func TestClassify_ARecurringStreakAlongItsOwnTrackIsASatellite(t *testing.T) {
	ss := []Streak{
		{X1: 100, Y1: 100, X2: 260, Y2: 180, WidthPx: 4, Duty: 1, Frame: 2},
		{X1: 300, Y1: 200, X2: 460, Y2: 280, WidthPx: 4, Duty: 1, Frame: 3}, // one step further down it
	}
	Classify(ss, DefaultOptions())
	for _, s := range ss {
		assert.Equal(t, Satellite, s.Class, "frame %d", s.Frame)
	}
	assert.Empty(t, Kept(ss))
}

// TestClassify_ParallelButOffTrackIsNotASatellite pins the rule that replaced "parallel and displaced".
//
// The pair here is exactly what the old rule convicted: same direction, next frame, moved — but moved
// SIDEWAYS, off the line either of them drew. Nothing in orbit does that, and on a real panel the
// loose rule labelled 86 of 91 candidates satellites because with a few streaks per frame over an
// eight-frame window some pair is always roughly parallel by chance.
func TestClassify_ParallelButOffTrackIsNotASatellite(t *testing.T) {
	ss := []Streak{
		{X1: 100, Y1: 100, X2: 260, Y2: 180, WidthPx: 4, Duty: 1, Frame: 2},
		{X1: 140, Y1: 180, X2: 300, Y2: 260, WidthPx: 4, Duty: 1, Frame: 3},
	}
	Classify(ss, DefaultOptions())
	for _, s := range ss {
		assert.Equal(t, Meteor, s.Class, "frame %d", s.Frame)
	}
}

// A single trail that stops abruptly at full brightness was ended by the shutter, not by burning out.
// This is the only discriminator left once recurrence cannot apply, which on a wide field at a slow
// cadence is most of the time.
func TestClassify_AFlatToppedSingleTrailIsASatellite(t *testing.T) {
	ss := []Streak{{X1: 100, Y1: 100, X2: 400, Y2: 250, WidthPx: 4, Duty: 1, Fullness: 0.97, Frame: 2}}
	Classify(ss, DefaultOptions())
	assert.Equal(t, Satellite, ss[0].Class)

	ss = []Streak{{X1: 100, Y1: 100, X2: 400, Y2: 250, WidthPx: 4, Duty: 1, Fullness: 0.55, Frame: 2}}
	Classify(ss, DefaultOptions())
	assert.Equal(t, Meteor, ss[0].Class, "a trail that rises and fades is a meteor")
}

// TestDetect_TwoShowerMeteorsAreNotSatellites guards the obvious false positive. Meteors from one
// shower radiate from a point, so two of them really can be parallel — but they do not recur a frame
// later a step further along.
func TestDetect_TwoShowerMeteorsAreNotSatellites(t *testing.T) {
	l := newLayer(500, 500).
		streak(100, 100, 260, 180, 2, 0.6).
		streak(140, 180, 300, 260, 2, 0.6). // parallel and displaced, but the SAME frame
		noise().l

	got := Detect(l, DefaultOptions())

	require.Len(t, got, 2)
	for _, s := range got {
		assert.Equal(t, Meteor, s.Class)
	}
}

// TestDetect_ABlinkingTrackIsAnAircraft: a strobe leaves gaps along the track it flies.
func TestDetect_ABlinkingTrackIsAnAircraft(t *testing.T) {
	// The flashes lie along the track AND point along it — an aircraft's strobe marks its own path,
	// so the chain and each mark share one direction.
	b := newLayer(700, 500)
	for k := 0; k < 4; k++ {
		x, y := 60+float64(k)*130, 100+float64(k)*65
		b.streak(x, y, x+40, y+20, 5, 0.7)
	}
	got := Detect(b.noise().l, DefaultOptions())

	// One line through the whole chain, not four marks: that is what a line detector returns, and it
	// is why blinking is judged by the line's DUTY CYCLE rather than by counting colinear neighbours.
	require.Len(t, got, 1)
	assert.Equal(t, Aircraft, got[0].Class)
	assert.Less(t, got[0].Duty, 0.5, "a strobe leaves gaps along its own track")
	assert.Empty(t, Kept(got))
}

func TestDetect_IgnoresSpecksAndRoundBlobs(t *testing.T) {
	b := newLayer(300, 300)
	for _, p := range [][2]int{{50, 50}, {51, 50}, {50, 51}, {200, 200}} { // hot pixels
		i := p[1]*300 + p[0]
		b.l.Excess[i] = 0.9
		b.l.Frame[i] = 1
		b.l.Count[i] = 1
	}
	got := Detect(b.noise().l, DefaultOptions())

	assert.Empty(t, got, "a few bright pixels are not a streak")
}

func TestDetect_EmptyLayer(t *testing.T) {
	assert.Nil(t, Detect(Layer{}, DefaultOptions()))
	assert.Nil(t, Detect(Layer{W: 10, H: 10, Excess: make([]float32, 100)}, DefaultOptions()))
}

func TestAngleDiff(t *testing.T) {
	tests := []struct {
		name string
		a, b float64
		want float64
	}{
		{"identical", 1, 1, 0},
		{"a right angle", 0, math.Pi / 2, math.Pi / 2},
		{"wrapping past pi is still close", 0.05, math.Pi - 0.05, 0.1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.InDelta(t, tt.want, angleDiff(tt.a, tt.b), 1e-9)
		})
	}
}

func TestCounts(t *testing.T) {
	got := Counts([]Streak{{Class: Meteor}, {Class: Meteor}, {Class: Satellite}})

	assert.Equal(t, 2, got[Meteor])
	assert.Equal(t, 1, got[Satellite])
	assert.Zero(t, got[Aircraft])
}

// TestConfident_KeepsAShortRealMeteor is the regression for a measured false negative.
//
// These are the two CONFIRMED trails from the 2026-08-10 session, both bright, straight, continuous
// and visibly tapered. Note the widths: 28.7 and 24.8 for lengths of 643 and 275. A streak's measured
// width comes from the morphology that found it, not from the streak — so an aspect threshold is a
// length threshold in disguise that penalises short trails twice, and at 12 it discarded the 275-px
// one at aspect 11.1.
func TestConfident_KeepsAShortRealMeteor(t *testing.T) {
	long := Streak{X1: 100, Y1: 100, X2: 675, Y2: 388, LengthPx: 643, WidthPx: 28.7,
		Duty: 0.97, Fullness: 0.77, Class: Meteor}
	short := Streak{X1: 1102, Y1: 63, X2: 1355, Y2: 170, LengthPx: 275, WidthPx: 24.8,
		Duty: 0.97, Fullness: 0.68, Class: Meteor}

	got := Confident([]Streak{long, short}, DefaultOptions())
	assert.Len(t, got, 2, "both confirmed meteors must be painted")

	// And the population that must still be refused: the short fat chains of stars, which the length
	// cut alone removes.
	chain := Streak{LengthPx: 154, WidthPx: 18, Duty: 0.8, Class: Meteor}
	assert.Empty(t, Confident([]Streak{chain}, DefaultOptions()))
}
