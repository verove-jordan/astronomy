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

// CalibSuggestion is one library master proposed for a light set, in a given calibration role. ID is a
// stable per-(light-set, role) key the UI sends back to exclude this exact suggestion from a run.
type CalibSuggestion struct {
	ID     string `json:"id"`
	Role   string `json:"role"`
	Master Master `json:"master"`
}

// CalibChannel is one inspected light set and the library masters that would calibrate it. Notes carry
// the gaps and cross-filter fallbacks that MatchForLight already explains.
type CalibChannel struct {
	Filter      string `json:"filter"`
	ExposureMs  int64  `json:"exposure_ms"`
	Gain        int64  `json:"gain"`
	Offset      int64  `json:"offset"`
	TempBucketC int    `json:"temp_bucket_c"`
	Bin         int    `json:"bin"`

	Suggestions []CalibSuggestion `json:"suggestions"`
	Notes       []string          `json:"notes,omitempty"`
}

// CalibPreview is the calibration the library would contribute to a run, per light channel — the data
// behind the Import "Calibration · matched from your library" panel. No Siril, nothing persisted.
type CalibPreview struct {
	Channels []CalibChannel `json:"channels"`
}

// SuggestID is the stable identity of a (light-set, role) suggestion — the single key shared by the
// preview (to label a suggestion) and the run (to drop an excluded role). The light filter is part of
// the key so each channel excludes independently, even when a filterless dark serves several filters.
func SuggestID(light inspect.SetKey, role string) string {
	return fmt.Sprintf("%s|%s|g%do%db%d|e%d|t%d",
		role, light.Filter, light.Gain, light.Offset, light.Bin, light.ExposureMs, light.TempBucket)
}

// SuggestForInventory matches every light set in inv against the library masters and reports, per light
// channel, the dark/flat/bias that would be applied plus any gaps/fallbacks (MatchForLight's notes).
func SuggestForInventory(inv *inspect.Inventory, masters []Master) CalibPreview {
	var preview CalibPreview
	if inv == nil {
		return preview
	}
	for _, set := range inv.SetsOfType(inspect.Light) {
		sel := MatchForLight(set.Key, masters)
		ch := CalibChannel{
			Filter: set.Key.Filter, ExposureMs: set.Key.ExposureMs, Gain: set.Key.Gain,
			Offset: set.Key.Offset, TempBucketC: set.Key.TempBucket, Bin: set.Key.Bin,
			Notes: sel.Notes,
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
	return append(list, CalibSuggestion{ID: SuggestID(light, role), Role: role, Master: *m})
}

// MatchForLightExcluding picks masters as MatchForLight does, then drops any role the user excluded for
// this exact light set (by its SuggestID). The remaining selection is what the run applies — so an
// exclusion is honored whether the masters were rebuilt from raw frames or reused from the library.
func MatchForLightExcluding(light inspect.SetKey, masters []Master, excluded []string) Selection {
	sel := MatchForLight(light, masters)
	if len(excluded) == 0 {
		return sel
	}
	skip := make(map[string]bool, len(excluded))
	for _, id := range excluded {
		skip[id] = true
	}
	if sel.Dark != nil && skip[SuggestID(light, RoleDark)] {
		sel.Dark = nil
		sel.Notes = append(sel.Notes, "dark excluded — skipped on your request")
	}
	if sel.Flat != nil && skip[SuggestID(light, RoleFlat)] {
		sel.Flat = nil
		sel.Notes = append(sel.Notes, "flat excluded — skipped on your request")
	}
	if sel.Bias != nil && skip[SuggestID(light, RoleBias)] {
		sel.Bias = nil
		sel.Notes = append(sel.Notes, "bias excluded — skipped on your request")
	}
	return sel
}
