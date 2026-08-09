package inspect

import "fmt"

// ID serializes the key into the stable exclusion token carried by RunRequest.exclude_sets
// (the calib.SuggestID precedent). Analysis-time IDs come from a default-options scan and are
// matched again at run time, so the field order and format are a compatibility contract.
//
// SetKey.Color is deliberately NOT part of the token: appending it would invalidate every selection
// a user has already saved, to disambiguate a pair of sets that can only both exist in a folder
// mixing mono and colour lights — which the inventory already refuses to stack in one run.
func (k SetKey) ID() string {
	return fmt.Sprintf("%s|%s|%s|e%d|g%do%db%d|i%d|t%d|s:%s",
		k.Type, k.Object, k.Filter, k.ExposureMs, k.Gain, k.Offset, k.Bin, k.ISO, k.TempBucket, k.Session)
}

// ExcludeSets drops every frame belonging to a listed set and rebuilds the sets, returning how
// many frames and sets were removed. Mirrors ExcludeBayer: only the Inventory's slices are
// touched — Frame structs are never mutated — so it is safe on ScanCache-shared frames. Unknown
// IDs are ignored (the set may have been renamed by a rescan; the run proceeds with a warning
// from the caller when nothing matched).
func (inv *Inventory) ExcludeSets(ids []string) (int, int) {
	if len(ids) == 0 {
		return 0, 0
	}
	excluded := make(map[string]bool, len(ids))
	for _, id := range ids {
		excluded[id] = true
	}
	drop := make(map[*Frame]bool)
	droppedSets := 0
	for _, set := range inv.Sets {
		if !excluded[set.Key.ID()] {
			continue
		}
		droppedSets++
		for _, fr := range set.Frames {
			drop[fr] = true
		}
	}
	if len(drop) == 0 {
		return 0, 0
	}
	kept := inv.Frames[:0:0]
	for _, fr := range inv.Frames {
		if drop[fr] {
			continue
		}
		kept = append(kept, fr)
	}
	inv.Frames = kept
	inv.Sets = buildSets(inv.Frames)
	return len(drop), droppedSets
}
