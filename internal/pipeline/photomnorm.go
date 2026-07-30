package pipeline

import (
	"fmt"
	"math"

	"github.com/verove-jordan/astronomy/internal/photom"
)

// noiseWeightLog2 is the applied photometric |log₂ scale| beyond which the channel's stack switches
// to -weight=noise. A group correctly normalized at ×4+ carries its read/sky noise up by the same
// factor, and sharpness (wfwhm) weighting cannot down-weight those frames — noise weighting can.
// Task #355's Ha master: groups applied at ×16–27 under full wfwhm weight drowned the reference
// night's clean frames in amplified noise.
const noiseWeightLog2 = 2.0

// photomStackWeight picks the stack weighting for a photometrically normalized channel: the preset
// weighting normally, "noise" when any group's frames were rewritten at a scale beyond
// ×2^noiseWeightLog2 in either direction. The note ("" = no switch) names the worst offender for the
// journal and the channel record.
func photomStackWeight(preset string, records []photom.GroupRecord) (string, string) {
	if preset == "noise" {
		return preset, ""
	}
	worst, worstScale, worstLabel := 0.0, 1.0, ""
	for _, rec := range records {
		if !rec.Applied || rec.Scale <= 0 {
			continue
		}
		if l := math.Abs(math.Log2(rec.Scale)); l > worst {
			worst, worstScale, worstLabel = l, rec.Scale, rec.Label
		}
	}
	if worst <= noiseWeightLog2 {
		return preset, ""
	}
	note := fmt.Sprintf("stack weighting switched to noise: group %q normalized at ×%.3g — sharpness weighting cannot down-weight its amplified noise",
		worstLabel, worstScale)
	return "noise", note
}

// buildPhotomGroup constructs the photometric-normalization group for one calibration group's
// calibrated (pp_) frames. The Meta drives the metadata sanity-check that flags a scale disagreeing
// with the header exposure/gain.
func buildPhotomGroup(g lightGroup, paths []string) photom.Group {
	label := fmt.Sprintf("%s g%d o%d %dms t%dC", g.Filter, g.Key.Gain, g.Key.Offset, g.Key.ExposureMs, g.Key.TempBucket)
	if g.Session != "" {
		label = g.Session + " · " + label
	}
	return photom.Group{
		Paths: paths,
		Label: label,
		Meta: photom.Meta{
			ExposureMs: g.Key.ExposureMs,
			Gain:       g.Key.Gain,
			GainKnown:  groupGainKnown(g),
			Instrument: groupInstrument(g),
			Creator:    groupCreator(g),
		},
		SessionID: g.SessionID,
		Session:   g.Session,
	}
}

// groupInstrument returns the camera name from the first frame that carries one — used as a prior
// when comparing curves (the ZWO/ASI 0.1 dB gain convention only applies to those cameras).
func groupInstrument(g lightGroup) string {
	for _, f := range g.Frames {
		if f != nil && f.Instrument != "" {
			return f.Instrument
		}
	}
	return ""
}

// groupCreator returns the capture software (SWCREATE) from the first frame that carries it — old
// ASICAP writes no INSTRUME card, so this is the fallback evidence for the ZWO gain law.
func groupCreator(g lightGroup) string {
	for _, f := range g.Frames {
		if f != nil && f.Creator != "" {
			return f.Creator
		}
	}
	return ""
}

// groupGainKnown reports whether EVERY frame of the group carries real gain metadata — conservative
// on purpose: the group key's gain is only trustworthy for the gain law when no frame defaulted to
// the zero value.
func groupGainKnown(g lightGroup) bool {
	for _, f := range g.Frames {
		if f == nil || !f.HasGain {
			return false
		}
	}
	return len(g.Frames) > 0
}

// markReferenceGroup flags the photometric reference group — the one every other group is scaled
// onto — using the SAME ranking the merged registration uses for its anchor group
// (anchorGroupIndex: anchor-night membership > current capture > frame count > later night). One
// run-wide anchor night therefore sets both the geometric canvas AND the photometric scale of
// every channel: task #354 picked a different reference night per channel (largest current group),
// which put R/G on a 15 s g450 zero-point while L sat on 120 s g0 — channel backgrounds 0.63 vs
// 0.37 and regional colour casts no global calibration could fix.
func markReferenceGroup(pgs []photom.Group, groups []lightGroup, anchorNight string) {
	best := anchorGroupIndex(groups, anchorNight)
	if best >= 0 && best < len(pgs) {
		pgs[best].Ref = true
	}
}
