// Package comet provides the pure geometry for comet-aligned stacking: locating a comet's centroid in a
// star-aligned frame, interpolating its position across a timestamped session, and translating frames so
// the comet (rather than the stars) is fixed. It is Siril-free and works on in-memory fits.Image data, so
// every piece is unit-testable without the engine.
package comet

import (
	"fmt"
	"math"
)

// Point is a pixel coordinate (x to the right, y downward — matching fits.Image row order).
type Point struct{ X, Y float64 }

// Track is a comet's apparent motion across one session, modeled as linear between two observed
// positions. Over a single session (hours) the sky motion projects to a near-linear pixel drift once the
// frames are star-aligned, so two anchor points plus per-frame timestamps place the comet in every frame.
type Track struct {
	P0, P1 Point
	T0, T1 int64 // anchor timestamps, epoch milliseconds (from DATE-OBS)
}

// NewTrack builds a track from two observed comet positions at two distinct times.
func NewTrack(p0 Point, t0 int64, p1 Point, t1 int64) (Track, error) {
	if t1 == t0 {
		return Track{}, fmt.Errorf("comet track: anchor frames share timestamp %d ms", t0)
	}
	return Track{P0: p0, P1: p1, T0: t0, T1: t1}, nil
}

// At returns the comet's interpolated (or extrapolated) position at time t.
func (tr Track) At(t int64) Point {
	f := float64(t-tr.T0) / float64(tr.T1-tr.T0)
	return Point{
		X: tr.P0.X + f*(tr.P1.X-tr.P0.X),
		Y: tr.P0.Y + f*(tr.P1.Y-tr.P0.Y),
	}
}

// Shift is the translation (dx,dy) that moves the comet from its position at time t onto the reference
// position ref. Apply it to that frame with Translate so the comet lands at ref in every frame.
func (tr Track) Shift(t int64, ref Point) (dx, dy float64) {
	p := tr.At(t)
	return ref.X - p.X, ref.Y - p.Y
}

// MidTime is the midpoint of the session's time span — the reference epoch at which the comet (and the
// star-aligned stack) sit, minimizing the maximum drift of both the comet and the stars.
func MidTime(times []int64) int64 {
	if len(times) == 0 {
		return 0
	}
	lo, hi := times[0], times[0]
	for _, t := range times {
		lo, hi = min(lo, t), max(hi, t)
	}
	return lo + (hi-lo)/2
}

// MiddleFrameIndex returns the index of the frame whose timestamp is closest to the session midpoint —
// the natural reference frame for star alignment (Siril `setref`).
func MiddleFrameIndex(times []int64) int {
	if len(times) == 0 {
		return 0
	}
	mid := MidTime(times)
	best, bestDist := 0, int64(math.MaxInt64)
	for i, t := range times {
		d := t - mid
		if d < 0 {
			d = -d
		}
		if d < bestDist {
			best, bestDist = i, d
		}
	}
	return best
}
