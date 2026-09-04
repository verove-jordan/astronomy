package inspect

import (
	"math"
	"sort"
)

// typeOrder gives a stable display/sort order for frame types.
var typeOrder = map[FrameType]int{Light: 0, Dark: 1, Flat: 2, DarkFlat: 3, Bias: 4}

// buildSets groups calibratable frames by their SetKey. Two passes: only when the frames span
// several KNOWN capture nights does the key gain the night (lights + flats only — see setKeyFor),
// so every single-night or undated scan produces byte-identical sets to the pre-sessionization
// behavior. Living inside buildSets, the rule holds for every caller (finalize, ExcludeBayer,
// ApplyFilterMapping) and stays a pure function of the frame set.
func buildSets(frames []*Frame) []Set {
	split := multiNight(frames)
	groups := make(map[SetKey][]*Frame)
	for _, fr := range frames {
		if fr.Type == Unknown || fr.Type == Video {
			continue
		}
		key := setKeyFor(fr, split)
		groups[key] = append(groups[key], fr)
	}

	sets := make([]Set, 0, len(groups))
	for key, members := range groups {
		// Temperature is decided across the group, not per frame — see clusterByTemperature.
		for _, cluster := range clusterByTemperature(key, members) {
			clusterKey := key
			clusterKey.TempBucket = cluster.bucketC
			var total int64
			for _, fr := range cluster.frames {
				total += fr.ExposureMs
			}
			sets = append(sets, Set{
				Key:                clusterKey,
				Frames:             cluster.frames,
				Count:              len(cluster.frames),
				TotalIntegrationMs: total,
			})
		}
	}
	sortSets(sets)
	return sets
}

// setKeyFor builds the grouping key for a frame, including only the fields that matter for
// its type (e.g. bias ignores exposure, filter and temperature). splitNights adds the frame's
// capture night to LIGHT and FLAT keys only: a night owns its sky and dust/orientation state
// (flats must not merge across nights), while darks/dark-flats/bias are closed-shutter thermal
// signatures the deep-master design deliberately pools across sessions.
func setKeyFor(fr *Frame, splitNights bool) SetKey {
	key := SetKey{Type: fr.Type, Gain: fr.Gain, Offset: fr.Offset, ISO: fr.ISO, Bin: fr.BinX, Color: fr.IsColor()}
	// TempBucket is deliberately NOT set here: it is assigned per cluster by clusterByTemperature,
	// once the whole group is known.
	switch fr.Type {
	case Light:
		key.Object = fr.Object
		key.Filter = fr.Filter
		key.ExposureMs = fr.ExposureMs
	case Dark, DarkFlat:
		key.ExposureMs = fr.ExposureMs
	case Flat:
		key.Filter = fr.Filter
		key.ExposureMs = fr.ExposureMs
	}
	if splitNights && (fr.Type == Light || fr.Type == Flat) {
		key.Session = fr.Session
	}
	return key
}

// Temperature grouping.
//
// This used to round each frame to the nearest 5 °C and put that in the key. The comment claimed it
// stopped drift from splitting a stack; it did the opposite, because a fixed grid has HARD EDGES at
// -7.5, -12.5 and so on. A cooler regulating at -9.8 with a couple of degrees of excursion lands on
// both sides of both edges, so ONE set of darks was reported as -5, -15 and "a few" -10 — and the
// stragglers, being sets of one or two frames, were then too small to build a master and were lost.
// MEASURED on a real 30-frame dark run: 3 + 1 + 26 across three "temperatures".
//
// So the temperature is decided for the GROUP, not the frame: sort the members, start a new cluster
// only where there is a real gap, and label each cluster with its median. Drift of any size within a
// run stays one set; a genuinely different regime still separates.
const (
	// tempClusterGapC is the jump between consecutive temperatures that starts a new set.
	//
	// Three degrees, not two: the reported failure was a cooler holding -9.8 with excursions to -7.4
	// and -12.4, whose consecutive gaps reach 2.2 °C. At two degrees those still split, which is the
	// bug. At three they stay one set, while the measured ramp that genuinely should split — a run
	// whose cooler was still coming down, with 3.7 and 4.0 °C jumps — still does. Dark current
	// roughly doubles every 6-7 °C, so three degrees is comfortably inside what the dark matcher
	// already treats as interchangeable (tempTolC = 5).
	tempClusterGapC = 3.0
	// tempClusterSpanC caps how wide one set may get. Without it a slowly ramping cooler chains every
	// frame into a single cluster spanning the whole ramp, and a −20 dark would be averaged into a
	// −10 master. Five degrees matches the matcher's own tolerance.
	tempClusterSpanC = 5.0
)

// displayTempBucketC rounds one frame to the nearest 5 °C for a SUMMARY LABEL — the logbook's "what
// was shot that night" line. It is deliberately not used for grouping: a fixed grid has hard edges,
// which is the whole bug described above. Rounding a single number for a human to read is the one
// job it is safe for.
func displayTempBucketC(fr *Frame) int {
	if !fr.HasTemp {
		return 0
	}
	return int(math.Round(fr.TempC()/5)) * 5
}

// tempCluster is one temperature-coherent subset of a group.
type tempCluster struct {
	frames  []*Frame
	bucketC int
}

// usesTemperature reports whether a frame type's set is defined by its sensor temperature.
//
// Darks and dark-flats ARE a thermal signature, and a light has to be matched to one, so all three
// are grouped by it. A BIAS is a readout at essentially zero integration — it was already excluded.
// A FLAT is excluded now, and that is a fix rather than a shortcut: flat-field response is
// temperature-independent, the frame is normalised before use, and what little dark current a
// one-second flat carries is removed by its matched dark-flat. Keying flats on temperature only
// fragmented them — MEASURED on a real run, 50 flats taken while the sensor warmed from −8.7 °C to
// +16.1 °C became SIX sets of a handful of frames each, and a flat master needs all fifty.
func usesTemperature(t FrameType) bool {
	return t == Light || t == Dark || t == DarkFlat
}

// SetKeyUsesTemperature is usesTemperature for callers outside this package — a report that prints a
// set's temperature has to know which sets actually have one, or a flat's unset bucket reads as a
// measurement of 0 °C.
func SetKeyUsesTemperature(t FrameType) bool { return usesTemperature(t) }

// clusterByTemperature splits a group into temperature-coherent sets, in the frames' original order.
func clusterByTemperature(key SetKey, members []*Frame) []tempCluster {
	if !usesTemperature(key.Type) {
		return []tempCluster{{frames: members}}
	}
	var withTemp, without []*Frame
	for _, fr := range members {
		if fr.HasTemp {
			withTemp = append(withTemp, fr)
		} else {
			without = append(without, fr)
		}
	}
	var out []tempCluster
	if len(without) > 0 {
		// Frames that never recorded a temperature keep bucket 0, exactly as before: it is the absence
		// of a measurement, not a measurement of zero.
		out = append(out, tempCluster{frames: without})
	}
	if len(withTemp) == 0 {
		return out
	}

	byTemp := append([]*Frame(nil), withTemp...)
	sort.SliceStable(byTemp, func(i, j int) bool { return byTemp[i].TempMilliC < byTemp[j].TempMilliC })

	start := 0
	for i := 1; i <= len(byTemp); i++ {
		done := i == len(byTemp)
		if !done {
			gap := byTemp[i].TempC() - byTemp[i-1].TempC()
			span := byTemp[i].TempC() - byTemp[start].TempC()
			if gap <= tempClusterGapC && span <= tempClusterSpanC {
				continue
			}
		}
		out = append(out, newTempCluster(byTemp[start:i]))
		start = i
	}
	return out
}

// newTempCluster labels a cluster with its MEDIAN temperature, in whole degrees. The median rather
// than the mean because the frames taken while a cooler is still settling are outliers by definition,
// and the label should say where the run actually sat.
func newTempCluster(frames []*Frame) tempCluster {
	mid := frames[len(frames)/2] // frames are already sorted by temperature
	return tempCluster{
		frames:  restoreOrder(frames),
		bucketC: int(math.Round(mid.TempC())),
	}
}

// restoreOrder puts a cluster's frames back in capture order, which is what every consumer of a Set
// expects — the sort above exists only to find the clusters.
func restoreOrder(frames []*Frame) []*Frame {
	out := append([]*Frame(nil), frames...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func sortSets(sets []Set) {
	sort.Slice(sets, func(i, j int) bool {
		a, b := sets[i].Key, sets[j].Key
		if ta, tb := typeOrder[a.Type], typeOrder[b.Type]; ta != tb {
			return ta < tb
		}
		if a.Object != b.Object {
			return a.Object < b.Object
		}
		if a.Filter != b.Filter {
			return a.Filter < b.Filter
		}
		if a.Session != b.Session {
			return a.Session < b.Session // per-night sets sort chronologically (no-op when unsplit)
		}
		if a.ExposureMs != b.ExposureMs {
			return a.ExposureMs < b.ExposureMs
		}
		if a.Gain != b.Gain {
			return a.Gain < b.Gain
		}
		return a.Offset < b.Offset
	})
}
