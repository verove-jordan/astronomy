package calib

import (
	"fmt"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

// Calibration roles a master can fill for a light (the user-facing kinds suggested in the Import UI).
const (
	RoleDark = "dark"
	RoleFlat = "flat"
	RoleBias = "bias"
)

// CalibSuggestion is one master proposed for a light set, in a given calibration role. ID is a
// stable per-(light-set, role) key the UI sends back to exclude this exact suggestion from a run.
type CalibSuggestion struct {
	ID     string `json:"id"`
	Role   string `json:"role"`
	Master Master `json:"master"`
	// FromCapture marks a master the run would BUILD from the capture's own calibration frames (a
	// synthetic candidate, Path empty) rather than reuse from the library (whose masters all carry a Path).
	FromCapture bool `json:"from_capture,omitempty"`
}

// CalibChannel is one inspected light set and the masters that would calibrate it. Notes carry
// the gaps and cross-filter fallbacks that MatchForLight already explains.
type CalibChannel struct {
	Filter      string `json:"filter"`
	ExposureMs  int64  `json:"exposure_ms"`
	Gain        int64  `json:"gain"`
	Offset      int64  `json:"offset"`
	TempBucketC int    `json:"temp_bucket_c"`
	Bin         int    `json:"bin"`
	// Session is the light set's capture night ("YYYY-MM-DD") when the scan spans several nights;
	// "" otherwise. The Import view groups the per-night calibration mapping by it.
	Session string `json:"session,omitempty"`

	Suggestions []CalibSuggestion `json:"suggestions"`
	Notes       []string          `json:"notes,omitempty"`
}

// CalibPreview is the calibration a run would apply, per light channel — the data behind the Import
// "Calibration" panel. Candidates come from the library AND from the capture's own calibration frames
// (see PreviewCandidates). No Siril, nothing persisted.
type CalibPreview struct {
	Channels []CalibChannel `json:"channels"`
}

// syntheticMaster describes the master a run WOULD build from a session calibration set — a preview-only
// candidate, never persisted. The empty Path is the from-capture discriminator (every library master
// carries one; see CalibSuggestion.FromCapture).
func syntheticMaster(set inspect.Set) Master {
	mt := masterByFrameType[set.Key.Type]
	return Master{
		Type:       mt,
		Filter:     set.Key.Filter,
		ExposureMs: set.Key.ExposureMs,
		Gain:       set.Key.Gain,
		Offset:     set.Key.Offset,
		TempMilliC: int64(set.Key.TempBucket) * 1000,
		HasTemp:    mt == MasterDark || mt == MasterDarkFlat,
		Bin:        set.Key.Bin,
		FrameCount: set.Count,
		Session:    set.Key.Session,
	}
}

// PreviewCandidates assembles the master candidate set a run would match lights against: the library
// masters plus a synthetic master for every calibration set in the inventory that no library master
// already field-matches — mirroring BuildOrReuseMasters' reuse-or-build without stacking anything. It
// deliberately skips the run's on-disk existence check: an S3-freed library master still calibrates
// (pulled back on demand), and the preview must stay free of filesystem I/O.
func PreviewCandidates(inv *inspect.Inventory, lib []Master) []Master {
	candidates := append([]Master(nil), lib...)
	if inv == nil {
		return candidates
	}
	for _, ft := range []inspect.FrameType{inspect.Bias, inspect.DarkFlat, inspect.Dark, inspect.Flat} {
		for _, set := range inv.SetsOfType(ft) {
			if findExisting(lib, set) != nil {
				continue // the run would reuse this library master, not rebuild
			}
			candidates = append(candidates, syntheticMaster(set))
		}
	}
	return candidates
}

// SuggestID is the stable identity of a (light-set, role) suggestion — the single key shared by the
// preview (to label a suggestion) and the run (to drop an excluded role). The light filter is part of
// the key so each channel excludes independently, even when a filterless dark serves several filters.
func SuggestID(light inspect.SetKey, role string) string {
	return fmt.Sprintf("%s|%s|g%do%db%d|e%d|t%d",
		role, light.Filter, light.Gain, light.Offset, light.Bin, light.ExposureMs, light.TempBucket)
}

// SuggestForInventory matches every light set in inv against the candidate masters (typically
// PreviewCandidates: library ∪ capture-built) and reports, per light channel, the dark/flat/bias that
// would be applied plus any gaps/fallbacks (matchForLight's notes). When force is set, mismatched
// (gain/temperature/exposure) masters are applied anyway, so the preview surfaces them as included
// suggestions instead of gap notes — the force_calibration_frames override.
func SuggestForInventory(inv *inspect.Inventory, masters []Master, force bool) CalibPreview {
	var preview CalibPreview
	if inv == nil {
		return preview
	}
	for _, set := range inv.SetsOfType(inspect.Light) {
		// Match against the same pool the RUN uses: masters from another sensor are excluded first
		// (see dims.go), so this preview shows what will actually be applied. They are excluded
		// under force too — force means "apply these despite the settings mismatch", and Siril
		// cannot apply a master of the wrong size at all, so forcing one would promise a correction
		// that silently never happens.
		usable, dimNote := poolFor(set, masters)
		sel := matchForLight(set.Key, usable, force)
		if dimNote != "" {
			sel.Notes = append(sel.Notes, dimNote)
		}
		ch := CalibChannel{
			Filter: set.Key.Filter, ExposureMs: set.Key.ExposureMs, Gain: set.Key.Gain,
			Offset: set.Key.Offset, TempBucketC: set.Key.TempBucket, Bin: set.Key.Bin,
			Session: set.Key.Session,
			Notes:   sel.Notes,
		}
		ch.Suggestions = appendSuggestion(ch.Suggestions, set.Key, RoleDark, sel.Dark)
		ch.Suggestions = appendSuggestion(ch.Suggestions, set.Key, RoleFlat, sel.Flat)
		ch.Suggestions = appendSuggestion(ch.Suggestions, set.Key, RoleBias, sel.Bias)
		preview.Channels = append(preview.Channels, ch)
	}
	return preview
}

func appendSuggestion(list []CalibSuggestion, light inspect.SetKey, role string, m *Master) []CalibSuggestion {
	if m == nil {
		return list
	}
	return append(list, CalibSuggestion{
		ID: SuggestID(light, role), Role: role, Master: *m,
		FromCapture: m.Path == "", // only synthetic (to-be-built) masters have no path
	})
}

// MatchForLightExcluding picks masters as matchForLight does (force relaxes the gain/temperature/exposure
// gates — the force_calibration_frames override), then drops any role the user excluded for this exact
// light set (by its SuggestID). The remaining selection is what the run applies — so an exclusion is
// honored whether the masters were rebuilt from raw frames or reused from the library.
func MatchForLightExcluding(light inspect.SetKey, masters []Master, excluded []string, force bool) Selection {
	sel := matchForLight(light, masters, force)
	if len(excluded) == 0 {
		return sel
	}
	skip := make(map[string]bool, len(excluded))
	for _, id := range excluded {
		skip[id] = true
	}
	if sel.Dark != nil && skip[SuggestID(light, RoleDark)] {
		sel.Dark = nil
		sel.DarkOptimize = false
		sel.Notes = append(sel.Notes, "dark excluded — skipped on your request")
	}
	if sel.Flat != nil && skip[SuggestID(light, RoleFlat)] {
		sel.Flat = nil
		sel.Notes = append(sel.Notes, "flat excluded — skipped on your request")
	}
	if sel.Bias != nil && skip[SuggestID(light, RoleBias)] {
		sel.Bias = nil
		sel.DarkOptimize = false // -opt needs the bias to isolate the thermal signal
		sel.Notes = append(sel.Notes, "bias excluded — skipped on your request")
	}
	return sel
}
