package annotate

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/deepstars"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

// Identifying stars is deliberately separate from LABELLING them. A label is a piece of text drawn
// on the image, so the list has to stay short and well spaced or the picture disappears under its
// own annotation. An identification is only read on hover, so there is no reason to ration it: every
// plotted marker that the catalogue can name should carry its name, its distance and its spectral
// type, whether or not it also won one of the eighty label slots.
//
// This is what the deep catalogue actually buys. On the embedded magnitude-9 extract a typical
// deep-sky frame has a handful of catalogued stars and everything else is an anonymous dot; on
// ATHYG the same frame resolves most of its brighter half.

// starInfo converts a catalogue entry into the wire shape, rounding every float so stars.json
// carries values, not float64 noise. Returns nil for an entry with nothing to say.
func starInfo(s deepstars.Star) *StarInfo {
	name := s.Primary()
	if name == "" && s.Spect == "" && s.DistPc == 0 {
		return nil
	}
	info := &StarInfo{
		Name:      name,
		Secondary: s.Secondary(),
		Mag:       round(s.Mag, 2),
		RADeg:     round(s.RADeg, 5),
		DecDeg:    round(s.DecDeg, 5),
		Spect:     s.Spect,
		Con:       s.Con,
	}
	if s.DistPc > 0 {
		info.DistPc = round(s.DistPc, 2)
	}
	if s.HasAbsMag {
		info.AbsMag = ptr(round(s.AbsMag, 2))
	}
	if s.HasCI {
		info.CI = ptr(round(s.CI, 3))
	}
	if s.HasRV {
		info.RVKmS = ptr(round(s.RVKmS, 1))
	}
	// Proper motion rides along even though nothing displays it as a number: combined with the
	// distance and the radial velocity it is a full three-dimensional space velocity, which is what
	// the 3D field map draws motion vectors from. Zero is a legitimate value only in the sense that
	// no real star has exactly zero proper motion — a catalogue that did not measure it also writes
	// 0, and 0 mas/yr contributes nothing to a velocity either way, so no sentinel is needed.
	info.PMRA = round(s.PMRA, 2)
	info.PMDec = round(s.PMDec, 2)
	return info
}

// identifyPeaks projects the field's catalogue stars onto the detected peaks the overlay plots and
// returns, per peak index, the star that landed on it. Stars arrive brightest-first and a peak is
// claimed once, so a close pair resolves to the brighter member rather than whichever was tried
// last — the same "first wins" rule the labels use.
func identifyPeaks(stars []deepstars.Star, wcs fits.WCS, m mapping, peaks []postprocess.StarPeak) map[int]deepstars.Star {
	if len(stars) == 0 || len(peaks) == 0 {
		return nil
	}
	grid := newIndexGrid(peaks)
	out := make(map[int]deepstars.Star, len(stars))
	for _, s := range stars {
		px, py, ok := wcs.SkyToPix(s.RADeg, s.DecDeg)
		if !ok {
			continue
		}
		fx, fy := m.wcsToFile(px, py)
		i, found := grid.nearestFree(fx, fy, matchTolPx, out)
		if !found {
			continue
		}
		out[i] = s
	}
	return out
}

// indexGrid is peakGrid's sibling: same 8 px buckets, but it yields the peak's INDEX so the caller
// can attach data to the very Point that index produced.
type indexGrid struct {
	cell  int
	peaks []postprocess.StarPeak
	byKey map[[2]int][]int
}

func newIndexGrid(peaks []postprocess.StarPeak) *indexGrid {
	g := &indexGrid{cell: 8, peaks: peaks, byKey: make(map[[2]int][]int, len(peaks))}
	for i, p := range peaks {
		k := [2]int{p.X / g.cell, p.Y / g.cell}
		g.byKey[k] = append(g.byKey[k], i)
	}
	return g
}

// nearestFree returns the closest peak within tol px that no brighter star has already claimed.
func (g *indexGrid) nearestFree(x, y, tol float64, taken map[int]deepstars.Star) (int, bool) {
	cx, cy := int(x)/g.cell, int(y)/g.cell
	best, bestD, found := 0, tol*tol, false
	for gy := cy - 1; gy <= cy+1; gy++ {
		for gx := cx - 1; gx <= cx+1; gx++ {
			for _, i := range g.byKey[[2]int{gx, gy}] {
				if _, claimed := taken[i]; claimed {
					continue
				}
				dx, dy := float64(g.peaks[i].X)-x, float64(g.peaks[i].Y)-y
				if d := dx*dx + dy*dy; d <= bestD {
					best, bestD, found = i, d, true
				}
			}
		}
	}
	return best, found
}

func round(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

func ptr[T any](v T) *T { return &v }
