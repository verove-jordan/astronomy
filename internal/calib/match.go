package calib

import (
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

// Selection is the set of masters chosen for one light set, plus human-readable notes.
type Selection struct {
	Dark  *Master  `json:"dark,omitempty"`
	Flat  *Master  `json:"flat,omitempty"`
	Bias  *Master  `json:"bias,omitempty"`
	Notes []string `json:"notes,omitempty"`
	// DarkOptimize marks Dark as a DIFFERENT-exposure master to be scaled onto the light's thermal
	// signal by Siril dark optimization (calibrate -opt). Only ever set together with a Bias master —
	// the optimization needs the bias to isolate the thermal signal.
	DarkOptimize bool `json:"dark_optimize,omitempty"`
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

// MatchForLight picks the most appropriate dark, flat and bias masters for a light set (no forcing,
// no exclusions) — the read-only default used by the preview and live-stacking paths. See matchForLight.
func MatchForLight(light inspect.SetKey, masters []Master) Selection {
	return matchForLight(light, masters, false)
}

// matchForLight picks the most appropriate dark, flat and bias masters for a light set:
//   - dark  — same gain/offset/bin and exposure, nearest sensor temperature (within tolerance); when
//     none exists but a same-camera dark of a DIFFERENT exposure + a bias are available, that dark is
//     selected with DarkOptimize (Siril -opt scales its thermal signal to the light's exposure);
//   - flat  — same filter (preferring matching gain/offset/bin);
//   - bias  — same gain/offset/bin.
//
// When force is true (the user's force_calibration_frames override) the camera-settings (gain/offset/bin)
// and sensor-temperature gates on the dark and bias are dropped, and a wrong-exposure dark is applied even
// without a bias to scale it — so whatever masters exist are always applied, mismatch and all, with a note
// explaining the mismatch. Missing categories are reported in Notes rather than failing.
func matchForLight(light inspect.SetKey, masters []Master, force bool) Selection {
	var sel Selection

	sel.Bias = bestBias(light, masters, force)
	if d := bestDark(light, masters, force); d != nil {
		sel.Dark = d
	} else if d := bestScalableDark(light, masters, force); d != nil && sel.Bias != nil {
		sel.Dark = d
		sel.DarkOptimize = true
		sel.Notes = append(sel.Notes, fmt.Sprintf(
			"no %dms dark — dark-optimized from the %dms master (thermal signal bias-scaled)",
			light.ExposureMs, d.ExposureMs))
	} else if force {
		// Forced last resort: no exposure-matched dark and no bias to scale one, but the user asked to
		// apply their darks anyway — subtract the closest available dark directly (approximate thermal fit).
		if d := closestDark(light, masters); d != nil {
			sel.Dark = d
			sel.Notes = append(sel.Notes, fmt.Sprintf(
				"forced dark — the %dms master is applied to %dms lights without scaling (thermal signal may be over- or under-subtracted)",
				d.ExposureMs, light.ExposureMs))
		} else {
			sel.Notes = append(sel.Notes, "no matching dark — dark calibration skipped")
		}
	} else {
		sel.Notes = append(sel.Notes, "no matching dark — dark calibration skipped")
	}
	// Forced camera/temperature-mismatch transparency: the dark/bias was applied despite differing settings.
	if force {
		if n := forcedNote(light, sel.Dark, RoleDark); n != "" {
			sel.Notes = append(sel.Notes, n)
		}
		if n := forcedNote(light, sel.Bias, RoleBias); n != "" {
			sel.Notes = append(sel.Notes, n)
		}
	}
	if f := bestFlat(light, masters); f != nil {
		sel.Flat = f
		if f.Filter != light.Filter {
			sel.Notes = append(sel.Notes, fmt.Sprintf(
				"no %s flat — using the %s flat (corrects shared dust/vignetting)", light.Filter, flatLabel(f)))
		}
		if light.Session != "" && f.Session != "" && f.Session != light.Session {
			sel.Notes = append(sel.Notes, fmt.Sprintf(
				"no %s flat — using the night %s flat (dust may have moved between nights)", light.Session, f.Session))
		}
	} else {
		sel.Notes = append(sel.Notes, "no flat available — flat correction skipped")
	}
	if sel.Bias == nil && sel.Dark == nil {
		sel.Notes = append(sel.Notes, "no bias or dark — no read-noise calibration available")
	}
	if n := borrowedNote(sel); n != "" {
		sel.Notes = append(sel.Notes, n)
	}
	return sel
}

// borrowedNote names the masters this light set is calibrated with that did NOT come from frames in
// this run's own folder. A session that calibrates itself says nothing — Notes are the exceptions.
//
// It exists because the run had no way of saying so, and the gap is not cosmetic. bestFlat falls
// back to ANY flat in the library when the session brought none; the only trace was an inventory
// warning reading "vignetting/dust correction skipped UNLESS a library master matches", which is
// equally true whether nothing happened or a stranger's flat was divided into every light. On a
// camera that publishes no gain or offset — every DSLR — the library key cannot tell two bodies
// apart, so "a matching master" is a much weaker statement than it looks.
func borrowedNote(sel Selection) string {
	var borrowed []string
	for _, c := range []struct {
		role string
		m    *Master
	}{{RoleDark, sel.Dark}, {RoleFlat, sel.Flat}, {RoleBias, sel.Bias}} {
		if c.m != nil && c.m.FromLibrary {
			borrowed = append(borrowed, fmt.Sprintf("%s %s (%d frames)", c.role, filepath.Base(c.m.Path), c.m.FrameCount))
		}
	}
	if len(borrowed) == 0 {
		return ""
	}
	return "from the calibration library, not this session's own frames: " + strings.Join(borrowed, " · ")
}

func bestDark(light inspect.SetKey, masters []Master, force bool) *Master {
	var best *Master
	bestTempDelta := math.MaxFloat64
	for i := range masters {
		m := &masters[i]
		if m.Type != MasterDark || m.ExposureMs != light.ExposureMs {
			continue
		}
		if !force && !sameCamera(light, m) {
			continue
		}
		delta := math.Abs(float64(m.TempMilliC-int64(light.TempBucket)*1000)) / 1000
		if !force && delta > tempTolC {
			continue
		}
		if best == nil || delta < bestTempDelta || (delta == bestTempDelta && m.FrameCount > best.FrameCount) {
			best, bestTempDelta = m, delta
		}
	}
	return best
}

// bestScalableDark picks a same-camera dark of a DIFFERENT exposure for Siril dark optimization
// (-opt): nearest temperature within tolerance, then the longest exposure (scaling a deep thermal
// signal down beats amplifying a shallow one), then the deepest stack. The caller only uses it when a
// bias master is also available (the optimization needs it). When force is set the camera-settings and
// temperature-tolerance gates are dropped (any different-exposure dark becomes a scaling candidate).
func bestScalableDark(light inspect.SetKey, masters []Master, force bool) *Master {
	var best *Master
	bestTempDelta := math.MaxFloat64
	for i := range masters {
		m := &masters[i]
		if m.Type != MasterDark || m.ExposureMs == light.ExposureMs {
			continue
		}
		if !force && !sameCamera(light, m) {
			continue
		}
		delta := math.Abs(float64(m.TempMilliC-int64(light.TempBucket)*1000)) / 1000
		if !force && delta > tempTolC {
			continue
		}
		better := best == nil || delta < bestTempDelta ||
			(delta == bestTempDelta && (m.ExposureMs > best.ExposureMs ||
				(m.ExposureMs == best.ExposureMs && m.FrameCount > best.FrameCount)))
		if better {
			best, bestTempDelta = m, delta
		}
	}
	return best
}

// closestDark picks the single nearest dark ignoring camera settings, exposure and temperature — the
// forced last-resort applied by matchForLight when no exposure-matched dark and no bias (for -opt
// scaling) exist. Preference: smallest exposure delta, then smallest temperature delta, then the
// deepest stack. Applied directly, so the caller warns the subtraction is only approximate.
func closestDark(light inspect.SetKey, masters []Master) *Master {
	var best *Master
	var bestExp, bestTemp int64 = math.MaxInt64, math.MaxInt64
	lightTempMilliC := int64(light.TempBucket) * 1000
	for i := range masters {
		m := &masters[i]
		if m.Type != MasterDark {
			continue
		}
		expD := absI64(m.ExposureMs - light.ExposureMs)
		tempD := absI64(m.TempMilliC - lightTempMilliC)
		better := best == nil || expD < bestExp ||
			(expD == bestExp && (tempD < bestTemp ||
				(tempD == bestTemp && m.FrameCount > best.FrameCount)))
		if better {
			best, bestExp, bestTemp = m, expD, tempD
		}
	}
	return best
}

// forcedNote describes why a forced master differs from the light on gain/offset/bin (and, for a dark,
// sensor temperature) — the transparency line shown in the calibration panel. It returns "" when the
// master is nil or actually matches on those axes (so no note is emitted for a coincidental match).
func forcedNote(light inspect.SetKey, m *Master, role string) string {
	if m == nil {
		return ""
	}
	var diffs []string
	if !sameCamera(light, m) {
		diffs = append(diffs, fmt.Sprintf("gain %d/offset %d/bin %d (yours: %d/%d/%d)",
			m.Gain, m.Offset, m.Bin, light.Gain, light.Offset, light.Bin))
	}
	if role == RoleDark && m.HasTemp {
		if d := math.Abs(float64(m.TempMilliC-int64(light.TempBucket)*1000)) / 1000; d > tempTolC {
			diffs = append(diffs, fmt.Sprintf("%+.0f°C (yours: ~%+d°C)", float64(m.TempMilliC)/1000, light.TempBucket))
		}
	}
	if len(diffs) == 0 {
		return ""
	}
	return fmt.Sprintf("forced %s — applied despite mismatched %s", role, strings.Join(diffs, ", "))
}

// absI64 returns the absolute value of x.
func absI64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
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

// pickFlat selects a flat master, optionally requiring its filter to match the light; among
// candidates it prefers the light's own capture night, then matching camera settings, then depth.
func pickFlat(light inspect.SetKey, masters []Master, requireFilter bool) *Master {
	var best *Master
	for i := range masters {
		m := &masters[i]
		if m.Type != MasterFlat || (requireFilter && m.Filter != light.Filter) {
			continue
		}
		if best == nil || flatBeats(light, m, best) {
			best = m
		}
	}
	return best
}

// flatBeats ranks flat candidates: same capture night first (dust/orientation state is per-night),
// then — among flats from OTHER known nights — the temporally NEAREST night, then matching camera
// settings, then the deepest stack. The nearest-night rank exists because dust drifts with TIME,
// not with stack depth: a multi-night merge whose anchor night had no own flats saw two equally
// deep candidates tie all the way down and the pick fall to list order — one channel got a
// 3-week-old flat (visible optical residue) while its neighbours got the 11-day one.
func flatBeats(light inspect.SetKey, m, best *Master) bool {
	ns, bs := nightScore(light.Session, m.Session), nightScore(light.Session, best.Session)
	if ns != bs {
		return ns > bs
	}
	if ns == 0 { // both from different known nights: the closest night wins
		if md, bd := nightGapDays(light.Session, m.Session), nightGapDays(light.Session, best.Session); md != bd {
			return md < bd
		}
	}
	if sc, bc := sameCamera(light, m), sameCamera(light, best); sc != bc {
		return sc
	}
	return m.FrameCount > best.FrameCount
}

// nightGapDays is the absolute distance in days between two YYYY-MM-DD night keys (huge on a parse
// failure, so unparseable nights rank last among the known-different candidates).
func nightGapDays(a, b string) int {
	ta, errA := time.Parse("2006-01-02", a)
	tb, errB := time.Parse("2006-01-02", b)
	if errA != nil || errB != nil {
		return math.MaxInt32
	}
	d := int(ta.Sub(tb).Hours() / 24)
	if d < 0 {
		d = -d
	}
	return d
}

// nightScore ranks a flat's capture night against the light's: 2 = the same known night, 0 = a
// DIFFERENT known night, -1 = the MASTER's night is unknown (library masters and metadata-less flat
// sets). An unknown night means unknowable dust age — it must never outrank a candidate dated days
// away (a promoted header-less flat once beat every per-night flat this way and re-polluted the L
// channel). On a single-night run (light night unknown) every candidate scores 1, keeping the
// pre-sessionization ordering exactly.
func nightScore(lightNight, masterNight string) int {
	switch {
	case lightNight == "":
		return 1
	case lightNight == masterNight:
		return 2
	case masterNight == "":
		return -1
	default:
		return 0
	}
}

// flatLabel names a flat for a note: its filter, or "session" for an unfiltered/shared flat set.
func flatLabel(m *Master) string {
	if m.Filter == "" {
		return "session"
	}
	return m.Filter
}

func bestBias(light inspect.SetKey, masters []Master, force bool) *Master {
	var best *Master
	for i := range masters {
		m := &masters[i]
		if m.Type != MasterBias {
			continue
		}
		if !force && !sameCamera(light, m) {
			continue
		}
		// Under force, prefer a same-camera bias if one exists, else fall back to the deepest available.
		if best == nil ||
			(sameCamera(light, m) && !sameCamera(light, best)) ||
			(sameCamera(light, m) == sameCamera(light, best) && m.FrameCount > best.FrameCount) {
			best = m
		}
	}
	return best
}

func sameCamera(light inspect.SetKey, m *Master) bool {
	return m.Gain == light.Gain && m.Offset == light.Offset && m.Bin == light.Bin
}
