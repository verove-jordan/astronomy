package calib

import (
	"fmt"
	"math"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

// Selection is the set of masters chosen for one light set, plus human-readable notes.
type Selection struct {
	Dark  *Master  `json:"dark,omitempty"`
	Flat  *Master  `json:"flat,omitempty"`
	Bias  *Master  `json:"bias,omitempty"`
	Notes []string `json:"notes,omitempty"`
}

// Siril paths for the selection.
func (s Selection) Masters() (dark, flat, bias string) {
	if s.Dark != nil {
		dark = s.Dark.Path
	}
	if s.Flat != nil {
		flat = s.Flat.Path
	}
	if s.Bias != nil {
		bias = s.Bias.Path
	}
	return
}

// MatchForLight picks the most appropriate dark, flat and bias masters for a light set:
//   - dark  — same gain/offset/bin and exposure, nearest sensor temperature (within tolerance);
//   - flat  — same filter (preferring matching gain/offset/bin);
//   - bias  — same gain/offset/bin.
//
// Missing categories are reported in Notes rather than failing.
func MatchForLight(light inspect.SetKey, masters []Master) Selection {
	var sel Selection

	if d := bestDark(light, masters); d != nil {
		sel.Dark = d
	} else {
		sel.Notes = append(sel.Notes, "no matching dark — dark calibration skipped")
	}
	if f := bestFlat(light, masters); f != nil {
		sel.Flat = f
		if f.Filter != light.Filter {
			sel.Notes = append(sel.Notes, fmt.Sprintf(
				"no %s flat — using the %s flat (corrects shared dust/vignetting)", light.Filter, flatLabel(f)))
		}
	} else {
		sel.Notes = append(sel.Notes, "no flat available — flat correction skipped")
	}
	if b := bestBias(light, masters); b != nil {
		sel.Bias = b
	} else if sel.Dark == nil {
		sel.Notes = append(sel.Notes, "no bias or dark — no read-noise calibration available")
	}
	return sel
}

func bestDark(light inspect.SetKey, masters []Master) *Master {
	var best *Master
	bestTempDelta := math.MaxFloat64
	for i := range masters {
		m := &masters[i]
		if m.Type != MasterDark || !sameCamera(light, m) || m.ExposureMs != light.ExposureMs {
			continue
		}
		delta := math.Abs(float64(m.TempMilliC-int64(light.TempBucket)*1000)) / 1000
		if delta > tempTolC {
			continue
		}
		if best == nil || delta < bestTempDelta || (delta == bestTempDelta && m.FrameCount > best.FrameCount) {
			best, bestTempDelta = m, delta
		}
	}
	return best
}

// bestFlat picks the flat to apply to a light: an exact filter match when one exists, otherwise ANY
// available flat — common for older sessions that shot a single flat set. A wrong-filter flat still
// corrects the session's shared dust/vignetting (most dust sits on the sensor window, common to every
// filter), which is far better than stacking raw. MatchForLight notes when a cross-filter flat is used.
func bestFlat(light inspect.SetKey, masters []Master) *Master {
	if m := pickFlat(light, masters, true); m != nil {
		return m
	}
	return pickFlat(light, masters, false)
}

// pickFlat selects a flat master, optionally requiring its filter to match the light; among candidates
// it prefers one whose camera settings match, then the one built from the most frames.
func pickFlat(light inspect.SetKey, masters []Master, requireFilter bool) *Master {
	var best *Master
	for i := range masters {
		m := &masters[i]
		if m.Type != MasterFlat || (requireFilter && m.Filter != light.Filter) {
			continue
		}
		if best == nil ||
			(sameCamera(light, m) && !sameCamera(light, best)) ||
			(sameCamera(light, m) == sameCamera(light, best) && m.FrameCount > best.FrameCount) {
			best = m
		}
	}
	return best
}

// flatLabel names a flat for a note: its filter, or "session" for an unfiltered/shared flat set.
func flatLabel(m *Master) string {
	if m.Filter == "" {
		return "session"
	}
	return m.Filter
}

func bestBias(light inspect.SetKey, masters []Master) *Master {
	var best *Master
	for i := range masters {
		m := &masters[i]
		if m.Type != MasterBias || !sameCamera(light, m) {
			continue
		}
		if best == nil || m.FrameCount > best.FrameCount {
			best = m
		}
	}
	return best
}

func sameCamera(light inspect.SetKey, m *Master) bool {
	return m.Gain == light.Gain && m.Offset == light.Offset && m.Bin == light.Bin
}
