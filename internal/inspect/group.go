package inspect

import (
	"math"
	"sort"
)

// typeOrder gives a stable display/sort order for frame types.
var typeOrder = map[FrameType]int{Light: 0, Dark: 1, Flat: 2, DarkFlat: 3, Bias: 4}

// buildSets groups calibratable frames by their SetKey.
func buildSets(frames []*Frame) []Set {
	groups := make(map[SetKey][]*Frame)
	for _, fr := range frames {
		if fr.Type == Unknown || fr.Type == Video {
			continue
		}
		key := setKeyFor(fr)
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
// its type (e.g. bias ignores exposure, filter and temperature).
func setKeyFor(fr *Frame) SetKey {
	key := SetKey{Type: fr.Type, Gain: fr.Gain, Offset: fr.Offset, Bin: fr.BinX}
	if fr.HasTemp && fr.Type != Bias {
		key.TempBucket = int(math.Round(fr.TempC()))
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
	return key
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
		if a.ExposureMs != b.ExposureMs {
			return a.ExposureMs < b.ExposureMs
		}
		if a.Gain != b.Gain {
			return a.Gain < b.Gain
		}
		return a.Offset < b.Offset
	})
}
