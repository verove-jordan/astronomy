package grade

import "math"

// Interpretation helpers for the 3×3 registration homographies Siril writes to the .seq
// (SeqMetric.H, row-major): re-referencing a frame's transform onto a chosen reference frame,
// and reading the physical facts a cross-session merge needs — how far the frame's footprint
// overlaps the reference canvas, and how much the field is rotated. Pure math; the pipeline
// owns the policy built on top (anchor choice, absurd-transform rejection).

// RelativeH re-references frame homography h onto the frame whose homography is ref (both taken
// from the same sequence, hence sharing one base): rel = ref⁻¹ · h. ok is false when ref is not
// invertible (an all-zero matrix from an unregistered frame).
func RelativeH(ref, h [9]float64) (rel [9]float64, ok bool) {
	inv, ok := invertH(ref)
	if !ok {
		return rel, false
	}
	return mulH(inv, h), true
}

// FootprintOverlap returns the fraction (0..1) of the w×h reference canvas covered by the
// axis-aligned bounding box of a w×h frame transformed by rel. The AABB overestimates a rotated
// footprint slightly — fine for its purpose, an absurdity gate: a false star match lands the
// footprint thousands of pixels away (overlap ≈ 0), a genuine same-target frame keeps most of it.
func FootprintOverlap(rel [9]float64, w, h float64) float64 {
	if w <= 0 || h <= 0 {
		return 0
	}
	minX, minY := math.Inf(1), math.Inf(1)
	maxX, maxY := math.Inf(-1), math.Inf(-1)
	for _, c := range [4][2]float64{{0, 0}, {w, 0}, {0, h}, {w, h}} {
		x, y, ok := applyH(rel, c[0], c[1])
		if !ok {
			return 0
		}
		minX, maxX = math.Min(minX, x), math.Max(maxX, x)
		minY, maxY = math.Min(minY, y), math.Max(maxY, y)
	}
	ix := math.Min(maxX, w) - math.Max(minX, 0)
	iy := math.Min(maxY, h) - math.Max(minY, 0)
	if ix <= 0 || iy <= 0 {
		return 0
	}
	return ix * iy / (w * h)
}

// RotationDeg reads the field rotation (degrees, (-180,180]) from the linear part of rel. Exact
// for the similarity/euclidean transforms star registration produces; for a mild homography it is
// the rotation of the local linear approximation — plenty for telemetry.
func RotationDeg(rel [9]float64) float64 {
	deg := math.Atan2(rel[3], rel[0]) * 180 / math.Pi
	if deg <= -180 {
		deg += 360
	}
	return deg
}

// invertH inverts a 3×3 row-major matrix via the adjugate; ok is false for a singular matrix.
func invertH(m [9]float64) (inv [9]float64, ok bool) {
	a, b, c := m[0], m[1], m[2]
	d, e, f := m[3], m[4], m[5]
	g, h, i := m[6], m[7], m[8]
	det := a*(e*i-f*h) - b*(d*i-f*g) + c*(d*h-e*g)
	if math.Abs(det) < 1e-12 {
		return inv, false
	}
	inv = [9]float64{
		e*i - f*h, c*h - b*i, b*f - c*e,
		f*g - d*i, a*i - c*g, c*d - a*f,
		d*h - e*g, b*g - a*h, a*e - b*d,
	}
	for k := range inv {
		inv[k] /= det
	}
	return inv, true
}

// mulH multiplies two 3×3 row-major matrices (a·b).
func mulH(a, b [9]float64) [9]float64 {
	var out [9]float64
	for r := 0; r < 3; r++ {
		for c := 0; c < 3; c++ {
			out[r*3+c] = a[r*3]*b[c] + a[r*3+1]*b[3+c] + a[r*3+2]*b[6+c]
		}
	}
	return out
}

// applyH transforms point (x,y) by the homography (projective divide); ok is false at a
// degenerate horizon (w ≈ 0).
func applyH(m [9]float64, x, y float64) (float64, float64, bool) {
	w := m[6]*x + m[7]*y + m[8]
	if math.Abs(w) < 1e-12 {
		return 0, 0, false
	}
	return (m[0]*x + m[1]*y + m[2]) / w, (m[3]*x + m[4]*y + m[5]) / w, true
}
