package inspect

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/verove-jordan/astronomy/internal/channeldetect"
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

// defaultSlotLegend is the conventional LRGB(Ha) wheel order used when no info.txt names the slots.
func defaultSlotLegend() []string {
	return append([]string(nil), channeldetect.DefaultOptions().Order...)
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
func assignByWheelSlot(man manifest, subdirs []string, framesByDir map[string][]*Frame) bool {
	if !anyWheelSlot(subdirs, framesByDir) {
		return false
	}
	legend := legendFromManifest(man)
	gainByFilter, expByFilter := manifestFilterMaps(man)
	for _, sd := range subdirs {
		for _, fr := range framesByDir[sd] {
			nameByWheelSlot(fr, legend)
			backfillManifest(fr, man, gainByFilter, expByFilter)
		}
	}
	return true
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

// nameByWheelSlot fills a frame's filter from its wheel slot via the legend, only when the frame is an
// uncalibrated light that still lacks a filter. Order-independent: each frame is named by its own slot.
func nameByWheelSlot(fr *Frame, legend []string) {
	if fr.Filter != "" || fr.WheelSlot == 0 || isCalibration(fr.Type) {
		return
	}
	fr.Filter = filterForSlot(legend, fr.WheelSlot)
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
