package pipeline

// nightpanoPatch is the proposed change to the panorama's canvas (all optional). The grade knobs it
// shares with milkyway live in nightscapePatch — every panel is stacked by that recipe, so the
// panel-level controls are the same controls.
type nightpanoPatch struct {
	// Projection selects the canvas: stereographic | galactic | altaz | both | all.
	Projection *string `json:"projection,omitempty"`
	// ScaleDegPerPix is the canvas scale. The panels are about 0.02 deg/px, so going much finer than
	// the 0.03 default buys pixels rather than detail: the plate solution cannot place a star more
	// precisely than the residual it was fitted to.
	ScaleDegPerPix *float64 `json:"scale_deg_per_pix,omitempty"`
	// GroupStepDeg splits pointings when consecutive frames move further than this on the sky. Raise
	// it when a session drifted a lot and one pointing has been split in two; lower it when two real
	// pointings were merged.
	GroupStepDeg *float64 `json:"group_step_deg,omitempty"`
	// BandMaskLatDeg is the galactic latitude inside which canvas pixels count as BAND rather than
	// background. The band is the subject: measuring the sky's own level and colour anywhere inside
	// it neutralises the Milky Way to grey.
	BandMaskLatDeg *float64 `json:"band_mask_lat_deg,omitempty"`
	// Background removes the residual sky dome from the assembled canvas with a low-order polynomial
	// fitted outside the band.
	Background *bool `json:"pano_background,omitempty"`
	// Foreground composites the landscape under the arch, from whichever panel was aimed lowest. It
	// applies to the altaz canvas only: the other projections have no horizon to stand it on.
	Foreground *bool `json:"pano_foreground,omitempty"`
}

const nightpanoKnobMenu = `You may tune these panorama controls (a change re-assembles the canvas):
- look: natural | iphone | deepsky — the per-panel grade, as in milkyway
- brightness (0.03..0.2): where the sky background lands
- saturation_scale (0..2): scales the look's own saturation
- highlight_ceiling (0.3..0.95): dims the Milky-Way core
- projection: stereographic (looking up at the sky) | galactic (the band laid level) | altaz (the arch over the horizon) | both | all
- scale_deg_per_pix (0.005..0.2): canvas scale; 0.03 matches the panels
- group_step_deg (0.1..20): how far frames may move before they count as a new pointing
- band_mask_lat_deg (0..60): galactic latitude inside which pixels are the band, not the background
- pano_background (true/false): remove the residual sky dome from the canvas
- pano_foreground (true/false): composite the landscape under the arch (altaz canvas only)
- keep_meteors (true/false): blend the meteors the clip rejected back in, minus satellites and aircraft
`
