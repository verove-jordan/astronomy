// Package panelgroup decides which frames of a hand-framed session were shot at the same pointing,
// so the engine knows what belongs in one stack before it tries to build a mosaic.
//
// A tripod session has no pointing headers, but it does leave a signature: while the camera sits
// still the frame-to-frame pointing barely moves, and when it is re-aimed the pointing jumps. Both
// signals were measured on a real 201-frame Milky Way arch session before the thresholds here were
// chosen — within a panel the pointing steps stayed under 0.83 degrees and the roll under 0.89,
// while the smallest genuine re-aim was 1.46 degrees. The defaults sit in those gaps.
//
// Roll is part of the rule and not decoration. The frames either side of one boundary in that
// session differ by 0.1 degrees of pointing and 6.4 degrees of roll, because the camera never moved
// — the photographer only reached over to cap the lens and start shooting darks.
package panelgroup

import (
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/verove-jordan/astronomy/internal/pointing"
)

// Frame is one light with a known pointing.
type Frame struct {
	Path     string
	At       time.Time
	Pointing pointing.Frame
}

// Panel is a set of frames that share a pointing closely enough to stack as one.
type Panel struct {
	Label  string
	Frames []Frame
	// Center is the mean pointing: the normalized mean of the frames' axis vectors, with the roll
	// averaged circularly so a panel straddling zero does not average to 180.
	Center pointing.Frame
	// SpreadDeg is the largest angle any frame sits from Center — the honest measure of how much the
	// tripod drifted, and worth surfacing because it bounds how much field the stack can keep.
	SpreadDeg  float64
	Start, End time.Time
}

// Options tune the segmentation. The zero value is not usable; call DefaultOptions.
type Options struct {
	// StepDeg splits when consecutive frames move further than this on the sky.
	StepDeg float64
	// RollStepDeg splits when the camera rotates about its own axis by more than this.
	RollStepDeg float64
	// GapSeconds splits when the session paused for longer than this, since a pause usually means
	// the photographer walked away and re-framed.
	GapSeconds float64
	// MinFrames is the size below which a segment is absorbed into whichever neighbour it is closer
	// to, rather than surviving as a panel too thin to reject anything.
	MinFrames int
}

// DefaultOptions are the measured thresholds described in the package comment.
func DefaultOptions() Options {
	return Options{
		StepDeg:     1.0,
		RollStepDeg: 3.0,
		GapSeconds:  600,
		MinFrames:   2,
	}
}

// Group segments frames into panels. Input order does not matter — frames are sorted by capture
// time first, because the whole method rests on consecutive-in-time meaning consecutive-in-aim.
//
// Pass lights only. Calibration frames must be separated beforehand: a dark shot without moving the
// tripod sits at the same pointing as the panel before it, and no amount of geometry can tell them
// apart — only the pixels can.
func Group(frames []Frame, o Options) []Panel {
	if len(frames) == 0 {
		return nil
	}
	ordered := make([]Frame, len(frames))
	copy(ordered, frames)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].At.Before(ordered[j].At) })

	segments := split(ordered, o)
	segments = absorbShort(segments, o)
	return label(segments)
}

// split cuts the run wherever the camera demonstrably moved.
func split(frames []Frame, o Options) [][]Frame {
	var segments [][]Frame
	current := []Frame{frames[0]}
	for i := 1; i < len(frames); i++ {
		if movedBetween(frames[i-1], frames[i], o) {
			segments = append(segments, current)
			current = nil
		}
		current = append(current, frames[i])
	}
	return append(segments, current)
}

func movedBetween(a, b Frame, o Options) bool {
	if pointing.SeparationDeg(a.Pointing, b.Pointing) > o.StepDeg {
		return true
	}
	if math.Abs(angleDiff(a.Pointing.RollDeg, b.Pointing.RollDeg)) > o.RollStepDeg {
		return true
	}
	gap := b.At.Sub(a.At).Seconds()
	return o.GapSeconds > 0 && gap > o.GapSeconds
}

// There is deliberately no pass that re-joins adjacent segments. It was tried and removed: to fire
// at all it needed a tolerance wider than the split threshold, which put it within a hair of
// merging genuinely different aims, and it silently undid the pause rule by re-joining two sittings
// at the same pointing. The split rules already handle the case that matters — a slow drift never
// exceeds the per-frame threshold and stays one panel — and if a knock does fragment a panel, both
// fragments still stack and still blend into the mosaic. Over-splitting costs a little depth;
// over-merging costs correctness.

// absorbShort folds a too-small segment into the closer of its neighbours. A single frame caught
// mid-re-aim is real data at a real pointing, but it cannot be stacked alone, and the panel it
// lands in is wide enough that a couple of degrees of offset registers fine.
func absorbShort(segments [][]Frame, o Options) [][]Frame {
	for i := 0; i < len(segments); {
		if len(segments[i]) >= o.MinFrames || len(segments) == 1 {
			i++
			continue
		}
		target := nearerNeighbour(segments, i)
		if target < 0 {
			i++
			continue
		}
		segments[target] = append(segments[target], segments[i]...)
		sort.SliceStable(segments[target], func(a, b int) bool {
			return segments[target][a].At.Before(segments[target][b].At)
		})
		segments = append(segments[:i], segments[i+1:]...)
		if target > i {
			continue // indices shifted left; re-check this slot
		}
		i = 0 // a merge can leave an earlier segment short again
	}
	return segments
}

// nearerNeighbour returns the index of the adjacent segment closest on the sky, or -1 if there is
// no neighbour.
func nearerNeighbour(segments [][]Frame, i int) int {
	center := centerOf(segments[i])
	best, bestSep := -1, math.Inf(1)
	for _, j := range []int{i - 1, i + 1} {
		if j < 0 || j >= len(segments) {
			continue
		}
		if sep := pointing.SeparationDeg(center, centerOf(segments[j])); sep < bestSep {
			best, bestSep = j, sep
		}
	}
	return best
}

func label(segments [][]Frame) []Panel {
	panels := make([]Panel, 0, len(segments))
	for i, seg := range segments {
		center := centerOf(seg)
		panels = append(panels, Panel{
			Label:     fmt.Sprintf("p%02d", i+1),
			Frames:    seg,
			Center:    center,
			SpreadDeg: spreadOf(seg, center),
			Start:     seg[0].At,
			End:       seg[len(seg)-1].At,
		})
	}
	return panels
}
