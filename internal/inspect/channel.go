package inspect

import (
	"fmt"
	"regexp"
	"strconv"

	"github.com/verove-jordan/astronomy/internal/channeldetect"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// channelStatsSample is how many center pixels to read per light when fingerprinting for detection.
const channelStatsSample = 200000

var reFrameIdx = regexp.MustCompile(`(?i)frame[-_]?0*(\d+)`)

// cfgKey groups light frames that come from the same camera configuration and target. Filter is
// intentionally excluded for unlabeled detection (it is what we are inferring) and exposure is fed
// as a per-frame signal, so a per-filter exposure difference does not split a capture.
type cfgKey struct {
	object     string
	gain       int64
	offset     int64
	tempBucket int
	bin        int
}

func keyForConfig(fr *Frame) cfgKey {
	k := cfgKey{object: fr.Object, gain: fr.Gain, offset: fr.Offset, bin: fr.BinX}
	if fr.HasTemp {
		k.tempBucket = int(fr.TempC()/5) * 5
	}
	return k
}

// processChannels fingerprints every light once, infers filters for unlabeled groups via
// wheel-order detection, and flags filter-wheel-transition frames on already-labeled groups too.
func processChannels(inv *Inventory, opts channeldetect.Options) {
	var lights []*Frame
	for _, fr := range inv.Frames {
		if fr.Type == Light {
			lights = append(lights, fr)
		}
	}
	if len(lights) == 0 {
		return
	}
	fps := fingerprintAll(lights)

	detectUnlabeled(inv, lights, fps, opts)
	if opts.DetectTransitions {
		flagLabeled(inv, lights, fps, opts)
	}
}

// detectUnlabeled runs full wheel-order detection on each group of filter-less lights.
func detectUnlabeled(inv *Inventory, lights []*Frame, fps map[string]channeldetect.Fingerprint, opts channeldetect.Options) {
	groups := map[cfgKey][]*Frame{}
	for _, fr := range lights {
		if fr.Filter == "" {
			groups[keyForConfig(fr)] = append(groups[keyForConfig(fr)], fr)
		}
	}
	for _, group := range groups {
		res := channeldetect.Detect(samplesFor(group, fps), opts)
		byPath := map[string]channeldetect.Assignment{}
		for _, a := range res.Assignments {
			byPath[a.Path] = a
		}
		for _, fr := range group {
			a := byPath[fr.Path]
			fr.Filter = a.Filter
			fr.ClassSource = SourceSignal
			fr.FilterConfidence = a.Confidence
			fr.WheelTransition = a.WheelTransition
		}
		recordDetection(inv, group, res)
	}
}

// flagLabeled flags transition frames within each already-labeled (filter, config) group.
func flagLabeled(inv *Inventory, lights []*Frame, fps map[string]channeldetect.Fingerprint, opts channeldetect.Options) {
	type fkey struct {
		cfg    cfgKey
		filter string
	}
	groups := map[fkey][]*Frame{}
	for _, fr := range lights {
		if fr.Filter != "" && fr.ClassSource != SourceSignal {
			k := fkey{keyForConfig(fr), fr.Filter}
			groups[k] = append(groups[k], fr)
		}
	}
	for _, group := range groups {
		byPath := map[string]*Frame{}
		for _, fr := range group {
			byPath[fr.Path] = fr
		}
		for _, a := range channeldetect.FlagTransitions(samplesFor(group, fps), opts) {
			if a.WheelTransition {
				if fr := byPath[a.Path]; fr != nil {
					fr.WheelTransition = true
				}
			}
		}
	}
}

// fingerprintAll reads a center sample from each light and derives its signal fingerprint once.
func fingerprintAll(lights []*Frame) map[string]channeldetect.Fingerprint {
	out := make(map[string]channeldetect.Fingerprint, len(lights))
	for _, fr := range lights {
		fp := channeldetect.Fingerprint{ExposureMs: fr.ExposureMs}
		if f, err := fits.Open(fr.Path); err == nil {
			if st, serr := f.Stats(channelStatsSample); serr == nil {
				fp.Background = st.Median
				fp.Flux = st.P90 - st.Median
				fp.StarRichness = st.BrightFrac
				fp.Noise = st.MAD
			}
		}
		out[fr.Path] = fp
	}
	return out
}

// samplesFor builds detection samples (acquisition-ordered) for a group of frames.
func samplesFor(group []*Frame, fps map[string]channeldetect.Fingerprint) []channeldetect.Sample {
	samples := make([]channeldetect.Sample, len(group))
	for i, fr := range group {
		samples[i] = channeldetect.Sample{Order: orderKey(fr, i), Path: fr.Path, FP: fps[fr.Path]}
	}
	return samples
}

// orderKey is the acquisition-order key: DATE-OBS ms, else a frame index parsed from the filename,
// else the stable position so equal keys preserve walk order.
func orderKey(fr *Frame, pos int) int64 {
	if fr.DateObsMs > 0 {
		return fr.DateObsMs
	}
	if m := reFrameIdx.FindStringSubmatch(fr.Path); m != nil {
		if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			return n
		}
	}
	return int64(pos)
}

// lowConfidence is the threshold below which detection is surfaced for user override.
const lowConfidence = 0.45

// recordDetection accumulates a group's detection into the inventory (UI mapping + override source).
func recordDetection(inv *Inventory, group []*Frame, res channeldetect.Result) {
	det := inv.ChannelDetection
	if det == nil {
		det = &ChannelDetection{Order: res.Order}
		inv.ChannelDetection = det
	}
	base := len(det.Runs)
	for _, r := range res.Runs {
		dr := DetectedRun{Filter: r.Filter, Count: r.Count, Confidence: r.Confidence}
		if len(r.Paths) > 0 {
			dr.FirstFrame, dr.LastFrame = r.Paths[0], r.Paths[len(r.Paths)-1]
		}
		det.Runs = append(det.Runs, dr)
	}
	for _, a := range res.Assignments {
		if a.WheelTransition && base+a.RunIndex < len(det.Runs) {
			det.Runs[base+a.RunIndex].WheelTransition++
		}
	}
	det.OverallConfidence = weakestConfidence(det.OverallConfidence, res.OverallConfidence)
	if res.OverallConfidence < lowConfidence {
		inv.Warnings = append(inv.Warnings, fmt.Sprintf(
			"channel detection low confidence (%.2f) — review/override the filter mapping", res.OverallConfidence))
	}
}

func weakestConfidence(a, b float64) float64 {
	if a == 0 {
		return b
	}
	if b < a {
		return b
	}
	return a
}

// ApplyFilterMapping remaps detected/known filters to user-chosen channels (e.g. {"R":"G"}); a
// target of "" or "ignore" excludes those light frames from stacking. Sets ClassSource "manual"
// and rebuilds the sets. Used to apply a UI override after scanning.
func ApplyFilterMapping(inv *Inventory, mapping map[string]string) {
	if len(mapping) == 0 {
		return
	}
	for _, fr := range inv.Frames {
		if fr.Type != Light {
			continue
		}
		to, ok := mapping[fr.Filter]
		if !ok {
			continue
		}
		switch to {
		case "", "ignore":
			fr.Type = Unknown
		default:
			fr.Filter = normalizeFilter(to)
			fr.ClassSource = SourceManual
		}
	}
	inv.Sets = buildSets(inv.Frames)
}
