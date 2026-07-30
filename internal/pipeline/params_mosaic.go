package pipeline

import (
	"encoding/json"

	"github.com/verove-jordan/astronomy/internal/mode"
)

// mosaicPatch is the tiled-panel mosaic mode's OWN knob surface, applied on top of the full
// deepsky patch model (the mode shares the deepsky finish; these keys drive the panel assembler).
type mosaicPatch struct {
	OverlapExpected *float64 `json:"overlap_expected,omitempty"`
	FeatherFrac     *float64 `json:"feather_frac,omitempty"`
	PhotomMatch     *string  `json:"photom_match,omitempty"`
	CanvasCrop      *string  `json:"canvas_crop,omitempty"`
	MinPanelFrames  *int     `json:"min_panel_frames,omitempty"`
	PanelSource     *string  `json:"panel_source,omitempty"`
}

// applyMosaicParamPatch runs the deepsky patch first (the shared finish surface), then the
// assembler keys. Any assembler change raises the tier to C — those knobs only take effect by
// re-assembling the canvas, never in a re-finish.
func applyMosaicParamPatch(working mode.Preset, raw json.RawMessage) (mode.Preset, tier, bool) {
	next, t, changed := applyDeepskyParamPatch(working, raw)
	var patch mosaicPatch
	if err := json.Unmarshal(raw, &patch); err != nil {
		return next, t, changed
	}
	before := next
	if patch.OverlapExpected != nil {
		next.MosaicOverlapExpected = clampf(*patch.OverlapExpected, 0.05, 0.5)
	}
	if patch.FeatherFrac != nil {
		next.MosaicFeatherFrac = clampf(*patch.FeatherFrac, 0.1, 1)
	}
	if patch.PhotomMatch != nil {
		next.MosaicPhotomMatch = oneOfMosaic(*patch.PhotomMatch, next.MosaicPhotomMatch, "gain_offset", "offset", "off")
	}
	if patch.CanvasCrop != nil {
		next.MosaicCanvasCrop = oneOfMosaic(*patch.CanvasCrop, next.MosaicCanvasCrop, "common", "union", "plan")
	}
	if patch.MinPanelFrames != nil {
		next.MosaicMinPanelFrames = clampi(*patch.MinPanelFrames, 1, 50)
	}
	if patch.PanelSource != nil {
		next.MosaicPanelSource = oneOfMosaic(*patch.PanelSource, next.MosaicPanelSource, "auto", "folders", "coords")
	}
	if mosaicKnobsChanged(before, next) {
		t = tierC
		changed = true
	}
	return next, t, changed
}

func mosaicKnobsChanged(a, b mode.Preset) bool {
	return a.MosaicOverlapExpected != b.MosaicOverlapExpected ||
		a.MosaicFeatherFrac != b.MosaicFeatherFrac ||
		a.MosaicPhotomMatch != b.MosaicPhotomMatch ||
		a.MosaicCanvasCrop != b.MosaicCanvasCrop ||
		a.MosaicMinPanelFrames != b.MosaicMinPanelFrames ||
		a.MosaicPanelSource != b.MosaicPanelSource
}

// oneOfMosaic keeps the current value when v is not one of the allowed enum values — an invalid
// string knob must degrade, never propagate.
func oneOfMosaic(v, current string, allowed ...string) string {
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return current
}
