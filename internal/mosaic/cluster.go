package mosaic

// Coordinate clustering: when no folder layout names the panels, the OBJCTRA/OBJCTDEC pointing
// headers do — greedy running-mean unit-vector clustering splits the lights into pointings.

import (
	"fmt"
	"math"

	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/skycat"
)

// joinFracFOV is the cluster-join threshold as a fraction of the panel field of view: two
// pointings closer than this are dither/drift of one panel, farther is a new panel.
const joinFracFOV = 0.35

// segmentCoords greedily clusters the lights' OBJCTRA/OBJCTDEC pointings on the unit sphere:
// running-mean unit-vector centroids, each frame joining the nearest cluster within
// joinFracFOV×fovDeg, else founding a new one. Lights with missing/unparsable coordinates cannot
// be placed — they merge into the FIRST panel with a warning (the pipeline decides what to do with
// them). No usable signal at all → a single panel holding every light.
func segmentCoords(lights []inspect.Frame, fovDeg float64) ([]Panel, []string) {
	if fovDeg <= 0 {
		return []Panel{panelOf(lights)}, []string{"no panel field-of-view estimate — treating every light as one panel"}
	}
	clusters, stray := clusterPointings(lights, math.Cos(joinFracFOV*fovDeg*math.Pi/180))
	if len(clusters) == 0 {
		return []Panel{panelOf(lights)}, []string{"no usable OBJCTRA/OBJCTDEC pointing headers — treating every light as one panel"}
	}
	panels := make([]Panel, 0, len(clusters))
	for _, cl := range clusters {
		panels = append(panels, panelOf(cl.members))
	}
	var warns []string
	if len(stray) > 0 {
		for _, fr := range stray {
			panels[0].Paths[fr.Path] = true
		}
		warns = append(warns, fmt.Sprintf("%d light frame(s) carry missing/unparsable pointing headers — grouped into the first panel", len(stray)))
	}
	return panels, warns
}

// coordCluster is one running-mean pointing cluster: the unit-vector sum of its members.
type coordCluster struct {
	sx, sy, sz float64
	members    []inspect.Frame
}

// clusterPointings assigns each light to the nearest cluster whose centroid lies within the join
// threshold (dot product > cosJoin), else founds a new cluster. Coordinate-less lights come back
// as strays.
func clusterPointings(lights []inspect.Frame, cosJoin float64) ([]*coordCluster, []inspect.Frame) {
	var clusters []*coordCluster
	var stray []inspect.Frame
	for _, fr := range lights {
		ra, dec, ok := frameCoords(fr)
		if !ok {
			stray = append(stray, fr)
			continue
		}
		v := raDecVec(ra, dec)
		best, bestDot := -1, cosJoin
		for i, cl := range clusters {
			norm := math.Sqrt(cl.sx*cl.sx + cl.sy*cl.sy + cl.sz*cl.sz)
			if norm == 0 {
				continue
			}
			if dot := (v[0]*cl.sx + v[1]*cl.sy + v[2]*cl.sz) / norm; dot > bestDot {
				best, bestDot = i, dot
			}
		}
		if best < 0 {
			clusters = append(clusters, &coordCluster{sx: v[0], sy: v[1], sz: v[2], members: []inspect.Frame{fr}})
			continue
		}
		cl := clusters[best]
		cl.sx, cl.sy, cl.sz = cl.sx+v[0], cl.sy+v[1], cl.sz+v[2]
		cl.members = append(cl.members, fr)
	}
	return clusters, stray
}

// frameCoords parses a frame's OBJCTRA/OBJCTDEC pointing headers (sexagesimal or decimal degrees)
// via the shared skycat parser.
func frameCoords(fr inspect.Frame) (raDeg, decDeg float64, ok bool) {
	ra, ok1 := skycat.ParseRA(fr.ObjCtRA)
	dec, ok2 := skycat.ParseDec(fr.ObjCtDec)
	if !ok1 || !ok2 {
		return 0, 0, false
	}
	return ra, dec, true
}

// raDecVec converts J2000 degrees to a unit vector for spherical averaging.
func raDecVec(raDeg, decDeg float64) [3]float64 {
	sr, cr := math.Sincos(raDeg * math.Pi / 180)
	sd, cd := math.Sincos(decDeg * math.Pi / 180)
	return [3]float64{cd * cr, cd * sr, sd}
}

// vecRADec converts a (not necessarily unit) vector back to J2000 degrees; ok=false for a
// degenerate near-zero vector (antipodal cancellation).
func vecRADec(x, y, z float64) (raDeg, decDeg float64, ok bool) {
	norm := math.Sqrt(x*x + y*y + z*z)
	if norm < 1e-12 {
		return 0, 0, false
	}
	ra := math.Atan2(y, x) * 180 / math.Pi
	if ra < 0 {
		ra += 360
	}
	return ra, math.Asin(z/norm) * 180 / math.Pi, true
}
