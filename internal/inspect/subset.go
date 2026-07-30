package inspect

// Subset returns a copy of inv containing only the frames keep accepts, with its sets rebuilt.
// The mosaic pipeline uses it to split one scan into per-panel inventories — each re-applies the
// night sessionization exactly like a normal multi-night run, because buildSets/sessionSummary run
// afresh over the KEPT frames (a subset spanning one night collapses back to unsplit sets).
//
// Frames are shared by pointer — the scanner's own read-only sharing convention (see ScanCache) —
// and inv itself is never mutated. A nil keep keeps every frame. ChannelDetection is deliberately
// dropped: its signal-run boundaries describe the whole scan and are meaningless for a subset.
func Subset(inv *Inventory, keep func(*Frame) bool) *Inventory {
	if inv == nil {
		return nil
	}
	out := &Inventory{
		Root:     inv.Root,
		Warnings: append([]string(nil), inv.Warnings...),
	}
	for _, fr := range inv.Frames {
		if keep == nil || keep(fr) {
			out.Frames = append(out.Frames, fr)
		}
	}
	for _, v := range inv.Videos {
		if keep == nil || keep(v) {
			out.Videos = append(out.Videos, v)
		}
	}
	out.Sets = buildSets(out.Frames)
	out.Sessions = sessionSummary(out.Frames)
	return out
}
