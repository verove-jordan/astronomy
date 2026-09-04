package planetary

import (
	"context"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// Canonical (distortion-free) reference geometry. Pass 1 registers every frame onto the single
// sharpest frame — whose own frozen seeing warp the whole stack then inherits (every region is
// averaged from frames all bent to match ONE atmospheric instant; the MTF cost near Nyquist is
// ~0.6–0.8, see doublestack.go). Each frame's measured field is F_i ≈ A_ref − G_i − A_i
// (A = atmospheric warp, G = pointing drift; the field stores sample-from offsets), and the
// atmosphere averages to ~zero over many frames, so the per-node MEDIAN of the fields isolates
// the reference's own warp: M ≈ A_ref − median(G). Off-disk nodes carry exactly −G_i (only the
// global baseline is measured over the dark limb/sky), so the median of M over off-disk nodes
// is that −median(G) DC term; subtracting it anchors the correction: C = M − DC ≈ A_ref
// on-disk and ≈ 0 off-disk — the limb stays continuous and the master keeps the reference's
// global position. Every frame then warps by F_i′ = F_i − C — the reference itself by −C —
// landing all of them on the distortion-free MEAN geometry instead of the reference's.
const (
	// canonicalMin is the fewest kept frames worth canonicalizing: below it the median field
	// removes less reference warp than the surviving frames' own atmosphere contaminates it.
	canonicalMin = 8
	// dcMinOffDisk is the fewest off-disk grid nodes trusted for the DC anchor; with fewer
	// (the disk fills the frame) the anchor falls back to the median over ALL nodes.
	dcMinOffDisk = 8
)

// measureAllFields measures every frame's displacement field onto the reference — sweep 1 of
// the canonical alignment: parallel decode + ZNCC only, no resampling, no writes. Fields are
// measured with the two-level estimator (coarse 10×10 seeds the dense grid, densefield.go).
// The reference slot gets a zero field (it is its own registration; canonicalizeFields turns
// it into −C); an unreadable frame leaves nil slots and drops out in sweep 2.
func measureAllFields(ctx context.Context, paths []string, refIdx int, rc10, rcD *refContext,
	seeds []frameSeed) (dx, dy [][]float64, corr []float64, err error) {
	dx = make([][]float64, len(paths))
	dy = make([][]float64, len(paths))
	corr = make([]float64, len(paths))
	err = forEachFrame(ctx, len(paths), planetaryWorkers(), func(i int) error {
		if i == refIdx {
			dx[i], dy[i] = uniformGrid(0, 0, rcD.gridN)
			corr[i] = 1
			return nil
		}
		im, rerr := fits.ReadImage(paths[i])
		if rerr != nil {
			return nil // frame drops out in sweep 2, same as the legacy path
		}
		dx[i], dy[i], corr[i] = measureTwoLevelField(im, rc10, rcD, seedAt(seeds, i))
		return nil
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return dx, dy, corr, nil
}

// medianField returns the per-node median over every measured field except the reference's —
// its zero field is a registration identity, not a measurement, and would bias the median.
func medianField(fields [][]float64, refIdx, nodes int) []float64 {
	med := make([]float64, nodes)
	vals := make([]float64, 0, len(fields))
	for k := 0; k < nodes; k++ {
		vals = vals[:0]
		for i, f := range fields {
			if f == nil || i == refIdx {
				continue
			}
			vals = append(vals, f[k])
		}
		if len(vals) > 0 {
			med[k] = medianOf(vals)
		}
	}
	return med
}

// fieldDC is the correction's DC anchor: the median over PURE off-disk nodes — nodes whose
// whole 3×3 neighborhood is off-disk, which smoothGrid can only ever have filled with the
// global baseline (pure pointing drift, no atmosphere). Limb-adjacent off-disk nodes carry a
// smoothed share of on-disk warp and would bias the anchor. Falls back to all off-disk nodes,
// then to every node when the disk fills the frame.
func fieldDC(med []float64, onDisk []bool) float64 {
	n := gridSize(med)
	pure := make([]float64, 0, len(med))
	off := make([]float64, 0, len(med))
	for k, on := range onDisk {
		if on {
			continue
		}
		off = append(off, med[k])
		if offDiskNeighborhood(onDisk, n, k) {
			pure = append(pure, med[k])
		}
	}
	if len(pure) >= dcMinOffDisk {
		return medianOf(pure)
	}
	if len(off) >= dcMinOffDisk {
		return medianOf(off)
	}
	all := append([]float64(nil), med...)
	return medianOf(all)
}

// offDiskNeighborhood reports whether node k and every in-bounds 3×3 neighbor are off-disk.
func offDiskNeighborhood(onDisk []bool, n, k int) bool {
	i, j := k%n, k/n
	for dj := -1; dj <= 1; dj++ {
		for di := -1; di <= 1; di++ {
			ii, jj := i+di, j+dj
			if ii < 0 || jj < 0 || ii >= n || jj >= n {
				continue
			}
			if onDisk[jj*n+ii] {
				return false
			}
		}
	}
	return true
}

// canonicalCorrection turns the median field into the reference-warp correction C: the
// off-disk DC (pointing drift) is subtracted so C ≈ A_ref on-disk and ≈ 0 off-disk.
func canonicalCorrection(medX, medY []float64, onDisk []bool) (cx, cy []float64) {
	dcx, dcy := fieldDC(medX, onDisk), fieldDC(medY, onDisk)
	cx = make([]float64, len(medX))
	cy = make([]float64, len(medY))
	for k := range medX {
		cx[k] = medX[k] - dcx
		cy[k] = medY[k] - dcy
	}
	return cx, cy
}

// subtractField subtracts the correction from one frame's field in place: F′ = F − C.
func subtractField(field, corr []float64) {
	for k := range field {
		field[k] -= corr[k]
	}
}

// canonicalizeFields applies the correction to every measured field in place. The reference's
// zero field becomes −C, so sweep 2 resamples the reference like any other frame.
func canonicalizeFields(dx, dy [][]float64, refIdx int, onDisk []bool) {
	nodes := len(onDisk)
	cx, cy := canonicalCorrection(medianField(dx, refIdx, nodes), medianField(dy, refIdx, nodes), onDisk)
	for i := range dx {
		if dx[i] == nil {
			continue
		}
		subtractField(dx[i], cx)
		subtractField(dy[i], cy)
	}
}
