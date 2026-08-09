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
		var total int64
		for _, fr := range members {
			total += fr.ExposureMs
		}
		sets = append(sets, Set{
			Key:                key,
			Frames:             members,
			Count:              len(members),
			TotalIntegrationMs: total,
		})
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
	if fr.HasTemp && fr.Type != Bias {
		key.TempBucket = tempBucketC(fr)
	}
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

// tempBucketC buckets a frame's sensor temperature to the nearest 5 °C, so minor drift
// (e.g. -19.5/-20.0/-20.5) doesn't split a stack while genuinely different temperatures do.
func tempBucketC(fr *Frame) int {
	if !fr.HasTemp {
		return 0
	}
	return int(math.Round(fr.TempC()/5)) * 5
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
