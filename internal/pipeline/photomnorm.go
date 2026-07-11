package pipeline

import (
	"fmt"

	"github.com/verove-jordan/astronomy/internal/photom"
)

// buildPhotomGroup constructs the photometric-normalization group for one calibration group's
// calibrated (pp_) frames. The Meta drives the metadata sanity-check that flags a scale disagreeing
// with the header exposure/gain.
func buildPhotomGroup(g lightGroup, paths []string) photom.Group {
	return photom.Group{
		Paths: paths,
		Label: fmt.Sprintf("%s g%d o%d %dms t%dC", g.Filter, g.Key.Gain, g.Key.Offset, g.Key.ExposureMs, g.Key.TempBucket),
		Meta:  photom.Meta{ExposureMs: g.Key.ExposureMs, Gain: g.Key.Gain, Instrument: groupInstrument(g)},
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

// markReferenceGroup flags the largest current-session group as the photometric reference, onto
// which every other group is scaled. With no current group it leaves the choice to photom's
// most-frames default.
func markReferenceGroup(pgs []photom.Group, groups []lightGroup) {
	best, bestN := -1, -1
	for i, g := range groups {
		if g.Current && len(g.Frames) > bestN {
			best, bestN = i, len(g.Frames)
		}
	}
	if best >= 0 && best < len(pgs) {
		pgs[best].Ref = true
	}
}
