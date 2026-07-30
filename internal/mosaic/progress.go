package mosaic

import (
	"sort"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

// Capture progress: what has ACTUALLY been shot for each panel, per filter, read back off the disk.
// A mosaic spans nights, so "where did I stop?" cannot live only in a checkbox the user remembers to
// tick — this reconstructs it from the frames themselves, which also picks up panels captured with
// other software before the plan existed.

// UnknownFilter is the bucket for lights whose filter could not be determined (no FILTER header, no
// wheel slot, no filename token). They are still counted, so a panel never looks empty just because
// its metadata is thin.
const UnknownFilter = "unknown"

// FilterProgress is one panel+filter tally.
type FilterProgress struct {
	Frames  int     `json:"frames"`
	Seconds float64 `json:"seconds"`           // total integration
	LastMs  int64   `json:"last_ms,omitempty"` // newest DATE-OBS seen (0 = no dated frame)
	Nights  int     `json:"nights,omitempty"`  // distinct capture nights contributing
}

// TileProgress maps panel folder ("p01") → filter ("L") → tally.
type TileProgress map[string]map[string]FilterProgress

// CountCaptured tallies the LIGHT frames of a scan by panel folder and filter. Frames outside any
// panel folder are ignored: they belong to a single-pointing capture, not to a tile of this mosaic.
func CountCaptured(frames []inspect.Frame, root string) TileProgress {
	nights := map[string]map[string]bool{} // panel|filter → set of night keys
	out := TileProgress{}
	for _, fr := range frames {
		if fr.Type != inspect.Light {
			continue
		}
		panel, ok := panelFolderKey(root, fr.Path)
		if !ok {
			continue
		}
		filter := fr.Filter
		if filter == "" {
			filter = UnknownFilter
		}
		if out[panel] == nil {
			out[panel] = map[string]FilterProgress{}
		}
		p := out[panel][filter]
		p.Frames++
		p.Seconds += float64(fr.ExposureMs) / 1000
		if fr.DateObsMs > p.LastMs {
			p.LastMs = fr.DateObsMs
		}
		if fr.Session != "" {
			key := panel + "|" + filter
			if nights[key] == nil {
				nights[key] = map[string]bool{}
			}
			nights[key][fr.Session] = true
			p.Nights = len(nights[key])
		}
		out[panel][filter] = p
	}
	return out
}

// CaptureTarget is the per-filter goal for every tile of a plan: how many frames of what exposure
// make a tile "done". It is what turns raw counts into a resumable to-do list.
type CaptureTarget struct {
	Filter     string `json:"filter"`
	Frames     int    `json:"frames"`
	ExposureMs int64  `json:"exposure_ms,omitempty"`
	Gain       int    `json:"gain,omitempty"`
	Offset     int    `json:"offset,omitempty"`
	Bin        int    `json:"bin,omitempty"`
	Dither     int    `json:"dither,omitempty"` // dither every N frames; 0 = never
}

// TileDone reports whether a panel has met every target. A plan with no targets can never be
// "done" from counts alone, so it falls back to the manual per-tile status the user ticks.
func TileDone(progress map[string]FilterProgress, targets []CaptureTarget) bool {
	if len(targets) == 0 {
		return false
	}
	for _, want := range targets {
		if want.Frames <= 0 {
			continue
		}
		if progress[want.Filter].Frames < want.Frames {
			return false
		}
	}
	return true
}

// RemainingFilters lists the filters a panel still owes frames for, in target order — the capture
// assistant's "what's left here" line.
func RemainingFilters(progress map[string]FilterProgress, targets []CaptureTarget) []string {
	var out []string
	for _, want := range targets {
		if want.Frames > 0 && progress[want.Filter].Frames < want.Frames {
			out = append(out, want.Filter)
		}
	}
	return out
}

// SortedPanels returns the panel keys of a progress map in capture order (p01, p02, …), so callers
// render a stable list.
func SortedPanels(p TileProgress) []string {
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		oi, oj := folderOrder(keys[i]), folderOrder(keys[j])
		if oi != oj {
			return oi < oj
		}
		return keys[i] < keys[j]
	})
	return keys
}
