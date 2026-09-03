package skypano

// quad.go implements asterism ("quad") hashing, the matching scheme astrometry.net is built on.
//
// It is here because brightness-ranked matching does not work on this data. The brightest stars in a
// stacked phone frame are SATURATED — their measured peaks sit against the ceiling — so flux stops
// ordering them by magnitude, and "the 30 brightest detections" is simply a different set of stars
// from "the 30 brightest catalogue entries". Any scheme that pairs the two lists by rank, or counts
// nearest neighbours between them, is matching sets that do not correspond.
//
// A quad sidesteps that entirely. Take four stars, use the two furthest apart to define a local
// frame, and write the other two down in it: the resulting four numbers are unchanged by
// translation, rotation and scale, and they do not care which star is brightest. Two quads with the
// same code are the same shape, and one shape match gives four star correspondences to build a
// camera from.
//
// Code layout follows Lang et al. (2010): A at (0,0), B at (1,1), C and D written in that frame,
// with the labelling symmetries broken so the same four stars always hash the same way.

import (
	"math"
	"sort"
)

// Point is a star position in a plane — image pixels, or catalogue stars projected into them.
type Point struct{ X, Y float64 }

// Quad is four stars and the shape they make.
type Quad struct {
	// Code is (Cx, Cy, Dx, Dy) in the frame where A is the origin and B is (1,1).
	Code [4]float64
	// Idx are the indices of A, B, C and D in the point list the quad was built from.
	Idx [4]int
	// DiameterPx is the AB separation, kept so a match can be sanity-checked on scale.
	DiameterPx float64
}

// QuadOptions tune quad construction.
type QuadOptions struct {
	// Neighbours is how many nearest stars each anchor considers. Quads are drawn from that
	// neighbourhood so they stay small on the sky, which keeps a quad's shape from being distorted
	// by the projection across a 72-degree field.
	Neighbours int
	// MaxStars caps how many stars are used as anchors.
	MaxStars int
	// MinDiameterPx rejects quads too small to be measured precisely.
	MinDiameterPx float64
	// MaxDiameterPx rejects quads so large that projection curvature changes their shape between
	// the image and the catalogue.
	MaxDiameterPx float64
}

// DefaultQuadOptions suit a several-thousand-pixel wide-field frame.
func DefaultQuadOptions() QuadOptions {
	return QuadOptions{Neighbours: 7, MaxStars: 250, MinDiameterPx: 120, MaxDiameterPx: 900}
}

// BuildQuads makes quads from local neighbourhoods of pts.
func BuildQuads(pts []Point, o QuadOptions) []Quad {
	n := len(pts)
	if n < 4 || o.Neighbours < 3 {
		return nil
	}
	anchors := n
	if o.MaxStars > 0 && anchors > o.MaxStars {
		anchors = o.MaxStars
	}

	seen := make(map[[4]int]bool)
	var quads []Quad
	for a := 0; a < anchors; a++ {
		near := nearest(pts, a, o.Neighbours, o.MaxDiameterPx)
		// Every triple from the neighbourhood, together with the anchor, is a candidate quad.
		for i := 0; i < len(near); i++ {
			for j := i + 1; j < len(near); j++ {
				for k := j + 1; k < len(near); k++ {
					idx := [4]int{a, near[i], near[j], near[k]}
					key := idx
					sort.Ints(key[:])
					if seen[key] {
						continue
					}
					seen[key] = true
					if q, ok := makeQuad(pts, idx, o); ok {
						quads = append(quads, q)
					}
				}
			}
		}
	}
	return quads
}

// nearest returns the indices of the k points closest to pts[a], within maxDist.
func nearest(pts []Point, a, k int, maxDist float64) []int {
	type nb struct {
		i  int
		d2 float64
	}
	max2 := maxDist * maxDist
	cand := make([]nb, 0, len(pts))
	for i := range pts {
		if i == a {
			continue
		}
		dx, dy := pts[i].X-pts[a].X, pts[i].Y-pts[a].Y
		d2 := dx*dx + dy*dy
		if maxDist > 0 && d2 > max2 {
			continue
		}
		cand = append(cand, nb{i, d2})
	}
	sort.Slice(cand, func(i, j int) bool { return cand[i].d2 < cand[j].d2 })
	if len(cand) > k {
		cand = cand[:k]
	}
	out := make([]int, len(cand))
	for i, c := range cand {
		out[i] = c.i
	}
	return out
}

// makeQuad builds the hash code for four stars, or reports that they do not form a usable quad.
func makeQuad(pts []Point, idx [4]int, o QuadOptions) (Quad, bool) {
	// A and B are the widest-separated pair; the other two become C and D.
	ai, bi, best := 0, 1, -1.0
	for i := 0; i < 4; i++ {
		for j := i + 1; j < 4; j++ {
			d := dist(pts[idx[i]], pts[idx[j]])
			if d > best {
				ai, bi, best = i, j, d
			}
		}
	}
	if best < o.MinDiameterPx || (o.MaxDiameterPx > 0 && best > o.MaxDiameterPx) {
		return Quad{}, false
	}
	var rest []int
	for i := 0; i < 4; i++ {
		if i != ai && i != bi {
			rest = append(rest, i)
		}
	}

	a, b := pts[idx[ai]], pts[idx[bi]]
	c, ok1 := toABFrame(a, b, pts[idx[rest[0]]])
	d, ok2 := toABFrame(a, b, pts[idx[rest[1]]])
	if !ok1 || !ok2 {
		return Quad{}, false
	}
	// Both inner stars must lie in the circle with AB as its diameter. That is what makes the shape
	// well conditioned: a star outside it is barely constrained by the frame it is measured in.
	if !inABCircle(c) || !inABCircle(d) {
		return Quad{}, false
	}

	A, B, C, D := idx[ai], idx[bi], idx[rest[0]], idx[rest[1]]
	// Break the labelling symmetries so the same four stars always hash identically: C before D,
	// and A before B.
	if c.X > d.X {
		c, d = d, c
		C, D = D, C
	}
	if c.X+d.X > 1 {
		// Swapping A and B maps the frame q -> (1,1) - q.
		c, d = Point{1 - d.X, 1 - d.Y}, Point{1 - c.X, 1 - c.Y}
		A, B = B, A
		C, D = D, C
	}
	return Quad{Code: [4]float64{c.X, c.Y, d.X, d.Y}, Idx: [4]int{A, B, C, D}, DiameterPx: best}, true
}

// toABFrame expresses p in the frame where a is the origin and b is (1,1).
func toABFrame(a, b, p Point) (Point, bool) {
	ux, uy := b.X-a.X, b.Y-a.Y
	l2 := ux*ux + uy*uy
	if l2 <= 0 {
		return Point{}, false
	}
	px, py := p.X-a.X, p.Y-a.Y
	// Project onto AB and its perpendicular, then scale so B lands on (1,1).
	along := (px*ux + py*uy) / l2
	perp := (px*uy - py*ux) / l2
	return Point{X: along - perp, Y: along + perp}, true
}

// inABCircle reports whether a code-space point lies in the circle with AB as diameter.
func inABCircle(p Point) bool {
	dx, dy := p.X-0.5, p.Y-0.5
	return dx*dx+dy*dy <= 0.5
}

func dist(a, b Point) float64 { return math.Hypot(a.X-b.X, a.Y-b.Y) }
