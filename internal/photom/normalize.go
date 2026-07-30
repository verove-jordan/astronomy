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
	// satNoteFrac is the saturated-pixel fraction above which a group gets an explanatory note
	// (~150 px of a 16 MP frame — a bright star core alone stays quiet, a clipped galaxy core does not).
	satNoteFrac = 1e-5
)

// Group is a set of frames sharing one photometric context (session/gain/optical train).
type Group struct {
	Paths []string
	Label string
	Meta  Meta
	Ref   bool // true for the reference group (transform ~identity, still measured)
	// SessionID / Session identify the group's origin for the record: the catalog session id
	// (0 = the current run's capture) and the capture-night key ("YYYY-MM-DD", "" = undated).
	SessionID int64
	Session   string
}

// GroupRecord is the per-group outcome of NormalizeGroups.
type GroupRecord struct {
	SessionID int64  `json:"session_id,omitempty"`
	Session   string `json:"session,omitempty"` // capture-night key — the UI's join key
	Label     string `json:"label"`
	Scale     float64 `json:"scale"`
	Offset    float64 `json:"offset"`
	Resid     float64 `json:"resid"`
	Frames    int     `json:"frames"`
	Clamped   bool    `json:"clamped,omitempty"`
	// MetaDisagree: measured scale far from the exposure/gain prediction (measurement kept).
	// MetaSeeded: curves too flat to measure — the scale IS the exposure/gain prediction.
	// Ref: this group is the photometric reference the others were mapped onto.
	MetaDisagree bool `json:"meta_disagree,omitempty"`
	MetaSeeded   bool `json:"meta_seeded,omitempty"`
	Ref          bool `json:"ref,omitempty"`
	Applied      bool `json:"applied"`
	// Method names the ladder rung that set Scale (measured|seeded|bg-matched|offset-only|identity).
	Method string `json:"method,omitempty"`
	// Reverted: the fitted transform would have clipped the group's sky below zero and was degraded
	// (see the no-clip gate in normalizeGroup) — the recorded Scale/Offset are the degraded ones.
	Reverted bool `json:"reverted,omitempty"`
	// SatFrac is the group's sensor-saturated pixel fraction (median across the probed frames, at
	// SatDetectLevel) — capture truth the transform must NOT be skewed by; the pre-stack repair
	// replaces those pixels from unsaturated nights instead. SatCeiling is where that saturation
	// plateau lands AFTER this group's transform (Scale·SatDetectLevel + Offset) — the per-frame
	// detection ceiling of the repair.
	SatFrac    float64 `json:"sat_frac,omitempty"`
	SatCeiling float64 `json:"sat_ceiling,omitempty"`
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
// reference or the transform is ~identity. A transform that would clip the group's sky below zero is
// first degraded (bg-matched, then offset-only) — a floored sky poisons the stack far worse than an
// imperfect scale (task #354's black frames). It returns the record plus any warning/skip notes.
func normalizeGroup(ctx context.Context, g Group, ref FrameCurve, refMeta Meta, isRef bool) (GroupRecord, []string) {
	rec := skipRecord(g)
	rec.Ref = isRef
	t, groupCurve, notes := measureGroupTransform(g, ref, refMeta)
	if !isRef {
		if degraded, note := degradeClippingTransform(g.Label, t, groupCurve, ref); note != "" {
			t = degraded
			rec.Reverted = true
			notes = append(notes, note)
		}
	}
	rec.Scale, rec.Offset, rec.Resid = t.Scale, t.Offset, t.Resid
	rec.Method = t.Method
	rec.Clamped, rec.MetaDisagree, rec.MetaSeeded = t.Clamped, t.MetaDisagree, t.MetaSeeded
	rec.SatFrac = groupCurve.SatFrac
	rec.SatCeiling = t.Scale*SatDetectLevel + t.Offset
	if rec.SatFrac > satNoteFrac {
		notes = append(notes, fmt.Sprintf("group %q: %.2f%% of pixels near sensor saturation — the clipped core carries no photometric information (the pre-stack repair draws on unsaturated nights)",
			g.Label, rec.SatFrac*100))
	}
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

// degradeClippingTransform is the pre-apply no-clip gate: when the fitted transform would push a
// HEALTHY sky below zero at 3σ (`s·bg + o − 3·s·noise < 0` — mapped pixels would be floored), it
// returns a degraded transform — the measured background ratio when available, else a pure
// background offset — plus the warning note. A group whose sky already straddles zero on its own
// (a dark-subtracted near-zero background) is not the transform's fault and is left alone — no
// degrade could improve it. There is deliberately NO upper noise-blowup bound: a shallow night
// correctly mapped onto a deep reference legitimately amplifies its noise. A clean transform
// returns with an empty note.
func degradeClippingTransform(label string, t Transform, group, ref FrameCurve) (Transform, string) {
	if !clipsSky(t, group) || clipsOwnSky(group) {
		return t, ""
	}
	orig := t
	if bg, ok := bgScale(group, ref); ok {
		// Provably clip-free here: a bg-matched map clips iff 3·noise > bg — the group's own sky
		// clipping — which the gate above already excluded.
		t = Transform{Scale: bg, Offset: ref.Bg - bg*group.Bg, Method: MethodBgMatched}
	} else {
		// Last resort: match the sky pedestal only — scale 1 cannot amplify anything into the floor.
		t = Transform{Scale: 1, Offset: ref.Bg - group.Bg, Method: MethodOffsetOnly}
	}
	note := fmt.Sprintf("group %q: transform ×%.3g %+.4g would clip its sky below zero — degraded to %s (×%.3g %+.4g)",
		label, orig.Scale, orig.Offset, t.Method, t.Scale, t.Offset)
	return t, note
}

// clipsSky reports whether the transform pushes the group's sky below zero at 3σ.
func clipsSky(t Transform, c FrameCurve) bool {
	return t.Scale*c.Bg+t.Offset-3*t.Scale*c.Noise < 0
}

// clipsOwnSky reports whether the group's UNTRANSFORMED sky already straddles zero at 3σ.
func clipsOwnSky(c FrameCurve) bool {
	return c.Bg-3*c.Noise < 0
}

// measureGroupTransform fits up to maxProbeFrames evenly-spaced frames of g against ref and returns
// the component-median transform plus the group's own median curve (the no-clip gate needs its
// background/noise); the Clamped/MetaDisagree flags are OR-ed across the probed frames and the
// Method is the most frequent across them.
func measureGroupTransform(g Group, ref FrameCurve, refMeta Meta) (Transform, FrameCurve, []string) {
	var notes []string
	var scales, offsets, resids []float64
	var curves []FrameCurve
	methods := map[string]int{}
	clamped, disagree, seeded, bgDisagree := false, false, false, false
	for _, p := range evenSpaced(g.Paths, maxProbeFrames) {
		fc, err := MeasureFile(p)
		if err != nil {
			notes = append(notes, fmt.Sprintf("group %q: unreadable frame %s", g.Label, p))
			continue
		}
		t := FitCurves(fc, ref, g.Meta, refMeta)
		curves = append(curves, fc)
		scales = append(scales, t.Scale)
		offsets = append(offsets, t.Offset)
		resids = append(resids, t.Resid)
		methods[t.Method]++
		clamped = clamped || t.Clamped
		disagree = disagree || t.MetaDisagree
		seeded = seeded || t.MetaSeeded
		bgDisagree = bgDisagree || t.BgDisagree
	}
	if len(scales) == 0 {
		return Transform{Scale: 1, Method: MethodIdentity}, FrameCurve{}, notes
	}
	return Transform{
		Scale:        medianFloat(scales),
		Offset:       medianFloat(offsets),
		Resid:        medianFloat(resids),
		Method:       dominantMethod(methods),
		Clamped:      clamped,
		MetaDisagree: disagree,
		MetaSeeded:   seeded,
		BgDisagree:   bgDisagree,
	}, medianCurve(curves), notes
}

// dominantMethod returns the most frequent method across a group's probed frames (deterministic
// tie-break by the Method constants' ladder order, most-authoritative first).
func dominantMethod(counts map[string]int) string {
	best, bestN := MethodIdentity, 0
	for _, m := range []string{MethodMeasured, MethodSeeded, MethodBgMatched, MethodOffsetOnly, MethodIdentity} {
		if counts[m] > bestN {
			best, bestN = m, counts[m]
		}
	}
	return best
}

// transformWarnings renders human-readable notes for a clamped scale, a metadata disagreement, or a
// metadata-seeded (unmeasurable) scale.
func transformWarnings(label string, t Transform) []string {
	var notes []string
	if t.Clamped {
		notes = append(notes, fmt.Sprintf("group %q: photometric scale clamped to [%.2g, %.2g] (measured out of range)", label, scaleMin, scaleMax))
	}
	if t.MetaDisagree {
		notes = append(notes, fmt.Sprintf("group %q: measured scale %.3g disagrees with header exposure/gain — check GAIN header", label, t.Scale))
	}
	if t.MetaSeeded {
		notes = append(notes, fmt.Sprintf("group %q: percentile curve too flat to measure (narrowband/sky pedestal) — scale %.3g seeded from header exposure/gain", label, t.Scale))
	}
	if t.Method == MethodBgMatched {
		notes = append(notes, fmt.Sprintf("group %q: scale %.3g matched from the measured sky backgrounds (no trustworthy header prediction)", label, t.Scale))
	}
	if t.BgDisagree {
		notes = append(notes, fmt.Sprintf("group %q: the measured sky-background ratio grossly disagrees with the confirmed header seed ×%.3g — residual pedestal (missing darks) or a strong sky-brightness difference; the seed (object flux) was kept", label, t.Scale))
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
	return GroupRecord{SessionID: g.SessionID, Session: g.Session, Label: g.Label, Frames: len(g.Paths), Scale: 1}
}
