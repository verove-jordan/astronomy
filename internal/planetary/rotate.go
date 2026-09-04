// Global ROTATION between a frame and the alignment reference.
//
// The lucky-imaging aligner models a frame's displacement as a translation plus a field of small
// local corrections: alignment points refine within ±apMaxShift (6 px) of the global shift, which is
// the right size for an atmospheric seeing residual. A rotation is not that. It displaces a point by
// θ·r, so on a 3840 px frame a rotation of half a degree moves the edges by 17 px — three times the
// AP search range. Every alignment point away from the centre then mislocks or is reset to the
// baseline, the frame is warped by a field that describes only its centre, and the stack averages a
// rotated copy of the Moon into the master. Hand-held afocal frames taken seconds apart rotate; ones
// taken minutes apart rotate a lot.
//
// So rotation is measured BEFORE the alignment points are, and folded into the field they refine
// from. That leaves the APs doing what they are designed for — the local seeing warp — instead of
// trying to absorb a global geometric term they cannot reach.
package planetary

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/fits"
)

const (
	// rotMaxDeg bounds the search. Beyond this the frame is not a lucky-imaging sibling of the
	// reference, it is a different pointing, and the panel segmentation is what should handle it.
	rotMaxDeg = 4.0
	// rotCoarseStepDeg / rotFineStepDeg: a coarse sweep then a refinement around its peak.
	rotCoarseStepDeg = 0.4
	rotFineStepDeg   = 0.04
	// rotMinDeg is the angle below which rotation is ignored and the historical uniform-translation
	// grid is emitted BYTE-IDENTICALLY. A tracked capture measures ~0 and is unaffected.
	rotMinDeg = 0.05
	// rotMinGain is the correlation improvement a non-zero angle must show over 0° to be believed;
	// without it, noise would hand back a small spurious rotation on every frame.
	rotMinGain = 0.002
)

// estimateRotation finds the rotation (radians, about the frame centre) that best registers tgt onto
// ref, given the already-measured global translation. Returns 0 when no angle beats 0° convincingly.
//
// It samples the target through the candidate transform rather than materializing a rotated image:
// the search is then a few hundred thousand bilinear samples per frame, which is affordable inside
// the per-frame measurement.
func estimateRotation(refSmall, tgtSmall *fits.Image, win comet.Point, radius int, tx, ty float64) float64 {
	if refSmall == nil || tgtSmall == nil || radius < 8 {
		return 0
	}
	cx, cy := float64(refSmall.W)/2, float64(refSmall.H)/2
	base := znccRotated(refSmall, tgtSmall, win, radius, 0, cx, cy, tx, ty)
	best, bestScore := 0.0, base
	for d := -rotMaxDeg; d <= rotMaxDeg+1e-9; d += rotCoarseStepDeg {
		if math.Abs(d) < 1e-9 {
			continue
		}
		th := d * math.Pi / 180
		if s := znccRotated(refSmall, tgtSmall, win, radius, th, cx, cy, tx, ty); s > bestScore {
			best, bestScore = th, s
		}
	}
	if best == 0 {
		return 0
	}
	// Refine around the coarse peak.
	lo, hi := best-rotCoarseStepDeg*math.Pi/180, best+rotCoarseStepDeg*math.Pi/180
	for th := lo; th <= hi+1e-12; th += rotFineStepDeg * math.Pi / 180 {
		if s := znccRotated(refSmall, tgtSmall, win, radius, th, cx, cy, tx, ty); s > bestScore {
			best, bestScore = th, s
		}
	}
	if bestScore-base < rotMinGain || math.Abs(best) < rotMinDeg*math.Pi/180 {
		return 0
	}
	return best
}

// znccRotated scores ref against tgt sampled through: rotate by theta about (cx,cy), then translate.
// The sampling convention matches comet.zncc — ref(p) is compared with tgt at the transformed p.
func znccRotated(ref, tgt *fits.Image, center comet.Point, radius int, theta, cx, cy, tx, ty float64) float64 {
	sin, cos := math.Sin(theta), math.Cos(theta)
	x0, y0 := int(center.X)-radius, int(center.Y)-radius
	x1, y1 := int(center.X)+radius, int(center.Y)+radius
	var sa, sb, saa, sbb, sab float64
	n := 0
	for y := y0; y <= y1; y += 2 {
		if y < 0 || y >= ref.H {
			continue
		}
		for x := x0; x <= x1; x += 2 {
			if x < 0 || x >= ref.W {
				continue
			}
			ux, uy := float64(x)-cx, float64(y)-cy
			sx := cx + ux*cos - uy*sin - tx
			sy := cy + ux*sin + uy*cos - ty
			if sx < 0 || sy < 0 || sx > float64(tgt.W-1) || sy > float64(tgt.H-1) {
				continue
			}
			a := float64(ref.Pix[0][y*ref.W+x])
			b := float64(bilinear(tgt, sx, sy))
			sa += a
			sb += b
			saa += a * a
			sbb += b * b
			sab += a * b
			n++
		}
	}
	if n < 256 {
		return -2
	}
	fn := float64(n)
	ca, cb := saa-sa*sa/fn, sbb-sb*sb/fn
	if ca <= 0 || cb <= 0 {
		return -2
	}
	return (sab - sa*sb/fn) / math.Sqrt(ca*cb)
}

// similarityField is the displacement field for "rotate by theta about (ccx,ccy), then shift by
// (gdx,gdy)", evaluated at the alignment points (apx,apy), in the warpByGrid convention
// out(p) = im(p − d(p)):
//
//	d(p) = (I − R(θ))·(p − c) + (gdx, gdy)
//
// It is evaluated at the REAL AP centres rather than an assumed lattice, so it cannot drift out of
// step with whatever apCenters lays down. At theta 0 every node is exactly (gdx,gdy) — the
// historical uniformGrid — so a capture with no measurable rotation warps bit-identically to before.
func similarityField(theta, ccx, ccy, gdx, gdy float64, apx, apy []float64) (dxGrid, dyGrid []float64) {
	dxGrid = make([]float64, len(apx))
	dyGrid = make([]float64, len(apy))
	sin, cos := math.Sin(theta), math.Cos(theta)
	for k := range apx {
		ux, uy := apx[k]-ccx, apy[k]-ccy
		dxGrid[k] = ux - (ux*cos - uy*sin) + gdx
		dyGrid[k] = uy - (ux*sin + uy*cos) + gdy
	}
	return dxGrid, dyGrid
}
