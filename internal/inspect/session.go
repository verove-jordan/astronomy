// Capture-night sessionization: every dated frame carries the key of the OBSERVING NIGHT it belongs
// to ("YYYY-MM-DD", bucketed at local noon so an after-midnight sub joins its evening's night), and a
// multi-night scan splits the per-filter light/flat sets per night — different nights have different
// sky, focus and dust state, so their flats must not merge and their lights form separate calibration
// groups (photometrically normalized before the cross-night stack). A single-night (or undated) scan
// is BYTE-IDENTICAL to the pre-sessionization behavior: no key gains a Session, no set splits.
package inspect

import (
	"fmt"
	"sort"
	"time"
)

// NightKeyIn returns the observing-night key ("YYYY-MM-DD") of an epoch-ms instant in loc: the date
// the night STARTED, bucketed at local noon — 2023-02-28T03:40 belongs to the night of 2023-02-27.
// ms <= 0 (no DATE-OBS) → "".
func NightKeyIn(loc *time.Location, ms int64) string {
	if ms <= 0 {
		return ""
	}
	return time.UnixMilli(ms).In(loc).Add(-12 * time.Hour).Format("2006-01-02")
}

// NightKey is NightKeyIn in the engine's local timezone (capture rigs and the engine share a site).
func NightKey(ms int64) string {
	return NightKeyIn(time.Local, ms)
}

// multiNight reports whether the frames span at least two distinct KNOWN capture nights among the
// types the night split applies to (lights + flats). One real night plus undated strays must NOT
// split — that keeps a folder with a few headerless files behaving exactly like today.
func multiNight(frames []*Frame) bool {
	first := ""
	for _, fr := range frames {
		if fr.Session == "" || (fr.Type != Light && fr.Type != Flat) {
			continue
		}
		if first == "" {
			first = fr.Session
			continue
		}
		if fr.Session != first {
			return true
		}
	}
	return false
}

// sessionSummary aggregates the frames into per-night SessionInfo entries (sorted by night key,
// undated last): frame counts by type, the night's time window, and its per-config light counts.
// nil when NO frame carries a night — the single-undated-folder case stays payload-identical.
func sessionSummary(frames []*Frame) []SessionInfo {
	byNight := make(map[string]*SessionInfo)
	for _, fr := range frames {
		if fr.Type == Video {
			continue
		}
		info := byNight[fr.Session]
		if info == nil {
			info = &SessionInfo{Key: fr.Session, Counts: make(map[FrameType]int)}
			byNight[fr.Session] = info
		}
		info.Counts[fr.Type]++
		if fr.DateObsMs > 0 {
			if info.StartMs == 0 || fr.DateObsMs < info.StartMs {
				info.StartMs = fr.DateObsMs
			}
			if fr.DateObsMs > info.EndMs {
				info.EndMs = fr.DateObsMs
			}
		}
	}
	if len(byNight) == 0 || (len(byNight) == 1 && byNight[""] != nil) {
		return nil // nothing dated: keep the inventory payload exactly as before
	}
	out := make([]SessionInfo, 0, len(byNight))
	for key, info := range byNight {
		info.Configs = sessionConfigs(frames, key)
		out = append(out, *info)
	}
	sort.Slice(out, func(i, j int) bool {
		if (out[i].Key == "") != (out[j].Key == "") {
			return out[j].Key == "" // undated bucket sorts last
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// sessionConfigs returns one entry per distinct light configuration captured that night, stable-sorted.
func sessionConfigs(frames []*Frame, night string) []SessionConfig {
	counts := make(map[SessionConfig]int)
	for _, fr := range frames {
		if fr.Type != Light || fr.Session != night {
			continue
		}
		cfg := SessionConfig{
			Filter: fr.Filter, ExposureMs: fr.ExposureMs, Gain: fr.Gain, Offset: fr.Offset,
			Bin: fr.BinX, TempBucket: displayTempBucketC(fr),
		}
		counts[cfg]++
	}
	out := make([]SessionConfig, 0, len(counts))
	for cfg, n := range counts {
		cfg.Count = n
		out = append(out, cfg)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Filter != b.Filter {
			return a.Filter < b.Filter
		}
		if a.ExposureMs != b.ExposureMs {
			return a.ExposureMs < b.ExposureMs
		}
		return a.Gain < b.Gain
	})
	return out
}

// warnUndatedSplit adds the inventory warning when the night split is active but some light/flat
// frames carry no DATE-OBS — they form their own undated group instead of joining a night.
func warnUndatedSplit(inv *Inventory) {
	undated := 0
	for _, fr := range inv.Frames {
		if fr.Session == "" && (fr.Type == Light || fr.Type == Flat) {
			undated++
		}
	}
	if undated > 0 {
		inv.Warnings = append(inv.Warnings, fmt.Sprintf(
			"%d light/flat frame(s) carry no DATE-OBS — in this multi-night capture they group separately from every night; check their headers", undated))
	}
}
