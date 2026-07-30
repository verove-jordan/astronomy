package inspect

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/verove-jordan/astronomy/internal/filters"
)

// The physical filter-wheel (EFW) slot — read from the SharpCap sidecar or the filename into
// Frame.WheelSlot — is ground-truth filter identity for mono captures that carry no FILTER header. This
// file turns a slot number into a filter NAME using a legend, so info.txt is only a naming legend (not a
// per-frame token list): an off-by-one between info.txt tokens and capture folders no longer matters.

var rePlaceholderFilter = regexp.MustCompile(`^S\d+$`) // an unnamed slot, e.g. "S6"

// isCalibration reports whether a frame type is calibration/video (never named by a light wheel legend).
func isCalibration(t FrameType) bool {
	return t == Dark || t == Flat || t == Bias || t == DarkFlat || t == Video
}

// filterAgnosticType reports whether a type is calibrated without a per-filter identity: darks, bias,
// dark-flats and video group filter-agnostically (their SetKey ignores Filter), so a wheel-slot filter
// name is meaningless for them. FLATS are per-filter (SetKey includes Filter), so — like lights — they
// ARE named from their physical wheel slot when the sidecar alias/filename gave no filter name.
func filterAgnosticType(t FrameType) bool {
	return t == Dark || t == Bias || t == DarkFlat || t == Video
}

// legendFromManifest returns the distinct filters in the manifest's capture order (first appearance),
// which map to ascending wheel slots 1,2,3,…  e.g. info.txt "L L L R G B Ha" → [L,R,G,B,Ha] = slots 1..5.
// When the manifest names no filters (empty/numeric info.txt), the default order is used.
func legendFromManifest(man manifest) []string {
	seen := make(map[string]bool)
	var legend []string
	for _, s := range man.Slots {
		if s.Filter != "" && !seen[s.Filter] {
			seen[s.Filter] = true
			legend = append(legend, s.Filter)
		}
	}
	if len(legend) == 0 {
		return defaultSlotLegend()
	}
	return legend
}

// defaultSlotLegend is the conventional wheel order used when no info.txt names the slots: the full
// canonical set, so a 7-slot wheel's narrowband positions resolve to OIII/SII instead of the "S6"/"S7"
// placeholders. Slots past the wheel's physical count simply never occur, so this is safe for the
// 5-slot LRGB+Ha case too.
//
// Deliberately NOT channeldetect.DefaultOptions().Order: that list is what *signal detection* can
// discriminate (see its comment), which is a different and smaller question than what a slot can hold.
func defaultSlotLegend() []string {
	return filters.List()
}

// filterForSlot names a 1-based wheel slot from the legend, or a stable "S<n>" placeholder when the slot
// is beyond the legend — the frames still group correctly under the placeholder (the caller warns).
func filterForSlot(legend []string, slot int) string {
	if slot >= 1 && slot <= len(legend) {
		return legend[slot-1]
	}
	return fmt.Sprintf("S%d", slot)
}

// assignByWheelSlot names every light frame under the manifest's sub-dirs from its physical wheel slot,
// using the manifest as the slot→name legend and back-filling gain/exposure/temperature it specifies. It
// returns false (so the caller can fall back to positional folder mapping) when no frame carries a slot.
func assignByWheelSlot(man manifest, subdirs []string, framesByDir map[string][]*Frame, inv *Inventory) bool {
	if !anyWheelSlot(subdirs, framesByDir) {
		return false
	}
	slotName := slotLegendFromSubdirs(man, subdirs, framesByDir, inv)
	if len(slotName) == 0 {
		slotName = defaultSlotMap() // numeric/empty info.txt → name slots by the default wheel order
	}
	gainByFilter, expByFilter := manifestFilterMaps(man)
	for _, sd := range subdirs {
		for _, fr := range framesByDir[sd] {
			nameByWheelSlotMap(fr, slotName)
			backfillManifest(fr, man, gainByFilter, expByFilter)
		}
	}
	return true
}

// slotLegendFromSubdirs learns which physical wheel slot holds which filter by pairing each chronological
// capture sub-dir with the manifest token captured at the same position: the info.txt sequence is in
// capture order, and so are the folders, so folder i's physical slot IS man.Slots[i]'s filter. This is
// robust when the capture order is NOT the slot order — e.g. ngc6992 shot O3 first (physically slot 4),
// which the old "Nth distinct filter → slot N" legend mislabeled. The mapping is self-consistent (one
// slot → one filter across all its folders); a genuine clash means the legend and folders disagree.
func slotLegendFromSubdirs(man manifest, subdirs []string, framesByDir map[string][]*Frame, inv *Inventory) map[int]string {
	slotName := map[int]string{}
	n := len(man.Slots)
	if len(subdirs) < n {
		n = len(subdirs)
	}
	for i := 0; i < n; i++ {
		f := man.Slots[i].Filter
		if f == "" {
			continue
		}
		slot := folderSlot(framesByDir[subdirs[i]])
		if slot == 0 {
			continue
		}
		if prev, ok := slotName[slot]; ok && prev != f {
			inv.Warnings = append(inv.Warnings, fmt.Sprintf(
				"filter-wheel slot %d maps to both %q and %q in the info.txt legend — using %q", slot, prev, f, f))
		}
		slotName[slot] = f
	}
	if len(man.Slots) != len(subdirs) {
		inv.Warnings = append(inv.Warnings, fmt.Sprintf(
			"info.txt lists %d filter slots but there are %d capture folders — paired the first %d in capture order",
			len(man.Slots), len(subdirs), n))
	}
	return slotName
}

// folderSlot returns the physical wheel slot shared by a capture sub-run's frames (the first non-zero).
func folderSlot(frames []*Frame) int {
	for _, fr := range frames {
		if fr.WheelSlot > 0 {
			return fr.WheelSlot
		}
	}
	return 0
}

// defaultSlotMap maps slots 1,2,3,… onto the conventional wheel order, for a numeric/empty info.txt.
func defaultSlotMap() map[int]string {
	legend := defaultSlotLegend()
	m := make(map[int]string, len(legend))
	for i, f := range legend {
		m[i+1] = f
	}
	return m
}

// nameRemainingWheelSlots names any still-unnamed light frame that carries a wheel slot but was not
// covered by an info.txt manifest (e.g. a sibling capture folder with no sidecar text), using the default
// wheel order. Frames already named (manifest/header/folder) are left untouched.
func nameRemainingWheelSlots(inv *Inventory) {
	legend := defaultSlotLegend()
	for _, fr := range inv.Frames {
		nameByWheelSlot(fr, legend)
	}
}

// nameByWheelSlot fills a frame's filter from its wheel slot via the legend, only when a light or a flat
// still lacks a filter (darks/bias are filter-agnostic — see filterAgnosticType). Per-filter EFW flats
// whose numeric slot alias carried no filter name are named here, so they don't collapse into one merged
// cross-filter master flat. Order-independent: each frame is named by its own slot.
func nameByWheelSlot(fr *Frame, legend []string) {
	if fr.Filter != "" || fr.WheelSlot == 0 || filterAgnosticType(fr.Type) {
		return
	}
	fr.Filter = filterForSlot(legend, fr.WheelSlot)
	fr.ClassSource = SourceWheel
	fr.FilterConfidence = 1
	if fr.Type == Unknown {
		fr.Type = Light
	}
}

// nameByWheelSlotMap fills a frame's filter from its physical wheel slot via the learned slot→name map,
// only when the frame is an uncalibrated light that still lacks a filter. A slot absent from the map
// (captured but never named by the legend) gets a stable "S<n>" placeholder so it still groups; the
// caller warns. Order-independent: each frame is named by its own slot.
func nameByWheelSlotMap(fr *Frame, slotName map[int]string) {
	if fr.Filter != "" || fr.WheelSlot == 0 || filterAgnosticType(fr.Type) {
		return
	}
	if f, ok := slotName[fr.WheelSlot]; ok {
		fr.Filter = f
	} else {
		fr.Filter = fmt.Sprintf("S%d", fr.WheelSlot)
	}
	fr.ClassSource = SourceWheel
	fr.FilterConfidence = 1
	if fr.Type == Unknown {
		fr.Type = Light
	}
}

// backfillManifest fills gain/exposure (by the frame's resolved filter) and temperature from the manifest,
// only where the header/sidecar left them blank — for filename-slot captures whose info.txt records them.
func backfillManifest(fr *Frame, man manifest, gainByFilter, expByFilter map[string]int64) {
	if isCalibration(fr.Type) {
		return
	}
	if fr.Gain == 0 {
		if g, ok := gainByFilter[fr.Filter]; ok {
			fr.Gain = g
			fr.HasGain = true
		}
	}
	if fr.ExposureMs == 0 {
		if e, ok := expByFilter[fr.Filter]; ok {
			fr.ExposureMs = e
		}
	}
	if !fr.HasTemp && man.HasTemp {
		fr.TempMilliC, fr.HasTemp = man.TempMilliC, true
	}
}

// manifestFilterMaps collects the gain and exposure the manifest specifies per filter name (consistent
// across that filter's slots), so they can back-fill frames named by their wheel slot.
func manifestFilterMaps(man manifest) (gain map[string]int64, exp map[string]int64) {
	gain, exp = make(map[string]int64), make(map[string]int64)
	for _, s := range man.Slots {
		if s.Filter == "" {
			continue
		}
		if s.HasGain {
			gain[s.Filter] = s.Gain
		}
		if s.ExposureMs > 0 {
			exp[s.Filter] = s.ExposureMs
		}
	}
	return gain, exp
}

// anyWheelSlot reports whether any frame in the given dirs carries a wheel slot.
func anyWheelSlot(subdirs []string, framesByDir map[string][]*Frame) bool {
	for _, sd := range subdirs {
		for _, fr := range framesByDir[sd] {
			if fr.WheelSlot > 0 {
				return true
			}
		}
	}
	return false
}

// warnUnnamedWheelSlots warns once per distinct wheel slot that had no legend entry (named "S<n>"), so
// the user knows to map it to a channel; the frames still group correctly under the placeholder.
func warnUnnamedWheelSlots(inv *Inventory) {
	seen := make(map[string]bool)
	for _, fr := range inv.Frames {
		if fr.ClassSource != SourceWheel || !rePlaceholderFilter.MatchString(fr.Filter) || seen[fr.Filter] {
			continue
		}
		seen[fr.Filter] = true
		inv.Warnings = append(inv.Warnings, fmt.Sprintf(
			"filter-wheel slot %s has no name legend — labeled %q; map it to a channel in the UI",
			strings.TrimPrefix(fr.Filter, "S"), fr.Filter))
	}
}
