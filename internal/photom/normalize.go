package photom

import (
	"context"
	"fmt"
	"math"
)

const (
	// maxProbeFrames caps how many evenly-spaced frames per group are measured for the fit.
	maxProbeFrames = 7
	// scaleEps / offsetNoiseK define the "homogeneous" band within which a group is left untouched.
	scaleEps     = 0.02
	offsetNoiseK = 0.2
)

// Group is a set of frames sharing one photometric context (session/gain/optical train).
type Group struct {
	Paths []string
	Label string
	Meta  Meta
	Ref   bool // true for the reference group (transform ~identity, still measured)
}

// GroupRecord is the per-group outcome of NormalizeGroups.
type GroupRecord struct {
	SessionID    int64   `json:"session_id,omitempty"`
	Label        string  `json:"label"`
	Scale        float64 `json:"scale"`
	Offset       float64 `json:"offset"`
	Resid        float64 `json:"resid"`
	Frames       int     `json:"frames"`
	Clamped      bool    `json:"clamped,omitempty"`
	MetaDisagree bool    `json:"meta_disagree,omitempty"`
	Applied      bool    `json:"applied"`
}

// NormalizeGroups measures each group, fits it to the reference group's median curve, and (unless the
// transform is ~identity) rewrites every frame of the group in place as p = Scale*p + Offset. It
// returns one record per group plus human-readable notes, and soft-fails throughout: any unreadable
// frame, unwriteable (non-float32) group, or context cancellation is recorded as a note and leaves the
// affected frames untouched.
func NormalizeGroups(ctx context.Context, groups []Group) ([]GroupRecord, []string) {
	if len(groups) == 0 {
		return nil, nil
	}
	refIdx := pickReference(groups)
	refCurve, ok, notes := referenceCurve(groups[refIdx])
	records := make([]GroupRecord, 0, len(groups))
	if !ok {
		for _, g := range groups {
			records = append(records, skipRecord(g))
		}
		return records, notes
	}
	refMeta := groups[refIdx].Meta
	for i, g := range groups {
		if ctx.Err() != nil {
			notes = append(notes, "photometric normalization cancelled")
			for _, rg := range groups[i:] {
				records = append(records, skipRecord(rg))
			}
			return records, notes
		}
		rec, gnotes := normalizeGroup(ctx, g, refCurve, refMeta, i == refIdx)
		records = append(records, rec)
		notes = append(notes, gnotes...)
	}
	return records, notes
}

// pickReference returns the index of the reference group: the first with Ref set, else the group with
// the most frames.
func pickReference(groups []Group) int {
	best, bestFrames := 0, -1
	for i, g := range groups {
		if g.Ref {
			return i
		}
		if len(g.Paths) > bestFrames {
			best, bestFrames = i, len(g.Paths)
		}
	}
	return best
}

// referenceCurve is the component-wise median of up to maxProbeFrames evenly-spaced frames of the
// reference group. ok is false when no frame could be read.
func referenceCurve(ref Group) (FrameCurve, bool, []string) {
	var notes []string
	var curves []FrameCurve
	for _, p := range evenSpaced(ref.Paths, maxProbeFrames) {
		fc, err := MeasureFile(p)
		if err != nil {
			notes = append(notes, fmt.Sprintf("photometric reference %q: unreadable frame %s", ref.Label, p))
			continue
		}
		curves = append(curves, fc)
	}
	if len(curves) == 0 {
		notes = append(notes, fmt.Sprintf("photometric reference %q: no readable frames — normalization skipped", ref.Label))
		return FrameCurve{}, false, notes
	}
	return medianCurve(curves), true, notes
}

// normalizeGroup measures the group, fits it to ref, and applies the transform unless the group is the
// reference or the transform is ~identity. It returns the record plus any warning/skip notes.
func normalizeGroup(ctx context.Context, g Group, ref FrameCurve, refMeta Meta, isRef bool) (GroupRecord, []string) {
	rec := skipRecord(g)
	t, notes := measureGroupTransform(g, ref, refMeta)
	rec.Scale, rec.Offset, rec.Resid = t.Scale, t.Offset, t.Resid
	rec.Clamped, rec.MetaDisagree = t.Clamped, t.MetaDisagree
	notes = append(notes, transformWarnings(g.Label, t)...)

	if isRef || isIdentity(t.Scale, t.Offset, ref.Noise) {
		return rec, notes
	}
	if err := applyTransform(ctx, g.Paths, t); err != nil {
		if ctx.Err() != nil {
			return rec, append(notes, fmt.Sprintf("group %q: photometric normalization cancelled", g.Label))
		}
		return rec, append(notes, fmt.Sprintf("photometric normalization skipped for group %q: frames are not 32-bit float", g.Label))
	}
	rec.Applied = true
	return rec, notes
}

// measureGroupTransform fits up to maxProbeFrames evenly-spaced frames of g against ref and returns the
// component-median transform; the Clamped/MetaDisagree flags are OR-ed across the probed frames.
func measureGroupTransform(g Group, ref FrameCurve, refMeta Meta) (Transform, []string) {
	var notes []string
	var scales, offsets, resids []float64
	clamped, disagree := false, false
	for _, p := range evenSpaced(g.Paths, maxProbeFrames) {
		fc, err := MeasureFile(p)
		if err != nil {
			notes = append(notes, fmt.Sprintf("group %q: unreadable frame %s", g.Label, p))
			continue
		}
		t := FitCurves(fc, ref, g.Meta, refMeta)
		scales = append(scales, t.Scale)
		offsets = append(offsets, t.Offset)
		resids = append(resids, t.Resid)
		clamped = clamped || t.Clamped
		disagree = disagree || t.MetaDisagree
	}
	if len(scales) == 0 {
		return Transform{Scale: 1}, notes
	}
	return Transform{
		Scale:        medianFloat(scales),
		Offset:       medianFloat(offsets),
		Resid:        medianFloat(resids),
		Clamped:      clamped,
		MetaDisagree: disagree,
	}, notes
}

// transformWarnings renders human-readable notes for a clamped scale or a metadata disagreement.
func transformWarnings(label string, t Transform) []string {
	var notes []string
	if t.Clamped {
		notes = append(notes, fmt.Sprintf("group %q: photometric scale clamped to [%.2g, %.2g] (measured out of range)", label, scaleMin, scaleMax))
	}
	if t.MetaDisagree {
		notes = append(notes, fmt.Sprintf("group %q: measured scale %.3g disagrees with header exposure/gain — check GAIN header", label, t.Scale))
	}
	return notes
}

// isIdentity reports whether a transform is close enough to identity to leave the group untouched.
func isIdentity(scale, offset, refNoise float64) bool {
	return math.Abs(scale-1) < scaleEps && math.Abs(offset) < offsetNoiseK*refNoise
}

// medianCurve returns the component-wise median FrameCurve of curves.
func medianCurve(curves []FrameCurve) FrameCurve {
	var out FrameCurve
	for i := range CurveQ {
		idx := i
		out.Q[idx] = medianOf(curves, func(c FrameCurve) float64 { return c.Q[idx] })
	}
	out.Bg = medianOf(curves, func(c FrameCurve) float64 { return c.Bg })
	out.Noise = medianOf(curves, func(c FrameCurve) float64 { return c.Noise })
	return out
}

// medianOf returns the median of sel applied across curves.
func medianOf(curves []FrameCurve, sel func(FrameCurve) float64) float64 {
	vals := make([]float64, len(curves))
	for i, c := range curves {
		vals[i] = sel(c)
	}
	return medianFloat(vals)
}

// evenSpaced returns at most n evenly-spaced elements of paths (mirroring imgops.Subsample's stride).
func evenSpaced(paths []string, n int) []string {
	if n <= 0 || len(paths) <= n {
		return paths
	}
	step := len(paths) / n
	out := make([]string, 0, n)
	for i := 0; i < len(paths) && len(out) < n; i += step {
		out = append(out, paths[i])
	}
	return out
}

// skipRecord builds the default (measured-but-not-applied) record for a group.
func skipRecord(g Group) GroupRecord {
	return GroupRecord{Label: g.Label, Frames: len(g.Paths), Scale: 1}
}
