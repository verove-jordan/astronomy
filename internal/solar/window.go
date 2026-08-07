package solar

import "sort"

// window.go splits a session into the stretches that may be stacked together.
//
// A solar stack cannot simply take everything. The chromosphere visibly reorganises over minutes,
// the Sun rotates at 0.6 degrees per hour, and on an alt-az mount the field turns about a third of
// a degree per minute — so a stack spanning an hour averages away exactly the detail it was
// gathering. Splitting into short windows keeps each stack inside the time the scene is effectively
// frozen, and hands back a time series that becomes the session time-lapse for free.

const (
	// defaultWindowSeconds bounds one stack's time span. It is set by field rotation rather than by
	// the Sun: a minute is about seven pixels of limb motion at a 1200 px radius, which the
	// registration's rotation term can correct but which grows awkward beyond that.
	defaultWindowSeconds = 60.0
	// defaultWindowFrames bounds one stack's frame count. Community practice for Hα is 100-150
	// frames; past that the SNR gain goes as the square root while the scene has moved on.
	defaultWindowFrames = 150
	// defaultMinWindowFrames is the smallest window worth stacking.
	defaultMinWindowFrames = 12
)

// Window is one stretch of a session, and one stack.
type Window struct {
	Frames  []Frame `json:"-"`
	StartMs int64   `json:"start_ms"`
	EndMs   int64   `json:"end_ms"`
	Count   int     `json:"count"`
}

// WindowOptions tunes the split.
type WindowOptions struct {
	Seconds   float64
	MaxFrames int
	MinFrames int
}

func (o WindowOptions) seconds() float64 {
	if o.Seconds > 0 {
		return o.Seconds
	}
	return defaultWindowSeconds
}

func (o WindowOptions) maxFrames() int {
	if o.MaxFrames > 0 {
		return o.MaxFrames
	}
	return defaultWindowFrames
}

func (o WindowOptions) minFrames() int {
	if o.MinFrames > 0 {
		return o.MinFrames
	}
	return defaultMinWindowFrames
}

// Windows splits frames, in capture order, into stackable stretches.
//
// Windows shorter than the minimum are DROPPED rather than stacked with what they have. Several of
// the stacking machinery's behaviours change silently below a dozen or so frames — clipping is
// disabled, weighting degenerates — so a runt window would be processed differently from its
// neighbours and would then flicker in the time-lapse for reasons no one could see.
func Windows(frames []Frame, o WindowOptions) []Window {
	if len(frames) == 0 {
		return nil
	}
	ordered := append([]Frame(nil), frames...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].TimeMs != ordered[j].TimeMs {
			return ordered[i].TimeMs < ordered[j].TimeMs
		}
		return ordered[i].Index < ordered[j].Index
	})

	spanMs := int64(o.seconds() * 1000)
	var out []Window
	start := 0
	for i := 1; i <= len(ordered); i++ {
		overFrames := i-start >= o.maxFrames()
		overTime := spanMs > 0 && i < len(ordered) &&
			ordered[i].TimeMs > 0 && ordered[start].TimeMs > 0 &&
			ordered[i].TimeMs-ordered[start].TimeMs > spanMs
		if i < len(ordered) && !overFrames && !overTime {
			continue
		}
		if w := newWindow(ordered[start:i]); w.Count >= o.minFrames() {
			out = append(out, w)
		}
		start = i
	}
	// A session too short to fill even one window still deserves its stack.
	if len(out) == 0 && len(ordered) > 0 {
		out = append(out, newWindow(ordered))
	}
	return out
}

// newWindow wraps a run of frames.
func newWindow(frames []Frame) Window {
	w := Window{Frames: frames, Count: len(frames)}
	if len(frames) > 0 {
		w.StartMs, w.EndMs = frames[0].TimeMs, frames[len(frames)-1].TimeMs
	}
	return w
}

// Sharpest returns the index of the window whose frames are sharpest on median.
func Sharpest(windows []Window) int {
	best, bestScore := 0, -1.0
	for i, w := range windows {
		scores := make([]float64, 0, len(w.Frames))
		for _, f := range w.Frames {
			scores = append(scores, f.Score)
		}
		if s := median(scores); s > bestScore {
			best, bestScore = i, s
		}
	}
	return best
}
