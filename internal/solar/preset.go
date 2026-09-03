package solar

// preset.go is the mode's tunable surface, split the way the supervised finish and the Refine panel
// need it: the finish re-renders a persisted master in seconds, while anything touching ingest or
// registration means going back to the frames.

// Preset is everything a solar run can be tuned by.
type Preset struct {
	// Ingest and stacking — changing any of these means re-reading the source frames.
	KeepPercent int     `json:"keep_percent"`
	MaxFrames   int     `json:"max_frames"`
	CropMargin  float64 `json:"crop_margin"`
	Drizzle     float64 `json:"drizzle"`
	ClipSigma   float64 `json:"clip_sigma"`
	APAlign     bool    `json:"ap_align"`
	APPoints    int     `json:"align_points"`
	// APScale is how much the raster is reduced before the distortion field is measured. 1 measures
	// at full resolution, which is what the field's amplitude actually requires; larger is faster and
	// progressively destroys the outer disc. 0 → apMeasureScale.
	APScale       int     `json:"ap_scale"`
	WindowSeconds float64 `json:"window_seconds"`
	WindowFrames  int     `json:"window_frames"`
	MinFrames     int     `json:"min_frames"`
	Band          Band    `json:"band"`
	// ScaleTolerance and RescaleGroups control how triage groups a mixed folder.
	ScaleTolerance float64 `json:"scale_tolerance"`
	// RescaleGroups stacks every compatible group together, rescaling each frame onto one canonical
	// disc. Registration already derives a per-frame scale from the fitted limb, so this costs
	// nothing structurally — but a group shot at half the disc size contributes upscaled frames that
	// add signal-to-noise without adding resolution, so it stays an explicit choice.
	RescaleGroups bool `json:"rescale_groups"`
	// BracketMerge stacks a bracketed group's exposure tiers separately and composites the results,
	// instead of normalising them onto one level and stacking them as if they were one exposure.
	// It is on by default because the alternative is not a different rendering of the same data —
	// windowing splits the session per clip anyway, so without it a bracket silently renders one
	// exposure and drops the rest into the time-lapse.
	BracketMerge bool `json:"bracket_merge"`
	// BracketStops is the exposure gap, in stops, that opens a new tier. 0 → bracketGapStops.
	BracketStops float64 `json:"bracket_stops"`
	// TransparencyFloor drops frames a cloud passed in front of, as a fraction of the clip's clearest
	// transmission. 0 turns the gate off.
	TransparencyFloor float64 `json:"transparency_floor"`
	// TwoBody measures an occulting body alongside the solar limb, and masks it out of the stack and
	// of every on-disc measurement (pair.go, pairmask.go). It is what the eclipse mode turns on. A
	// full-disc frame comes through it reporting no occulter, so it is safe on a session that turns
	// out not to contain one — but it is not the identical arithmetic, so an ordinary solar run
	// leaves it off and gets exactly the fit it always got.
	TwoBody bool `json:"two_body"`
	// BestFrames exports this many of the sharpest individual frames as finished images, spread
	// through the clip. 0 exports none.
	//
	// It is not a diagnostic. A stack pays for registration twice — every frame is resampled, and
	// then estimates that disagree are averaged — and when the frames are already close to what the
	// optics can deliver, those costs can exceed what averaging wins. Measured on the 12 Aug eclipse
	// clip: single frames resolve the occulter's edge at sigma 1.09 px, the 834-frame master at 2.30.
	// Where that holds, the best single frame IS the best picture, and the run should hand it over
	// rather than only offering the average.
	BestFrames int `json:"best_frames"`
	// BestFrameGapSeconds is the least time between two exported frames. Sharpness drifts slowly, so
	// without it the top of the ranking is the same second over and over.
	BestFrameGapSeconds float64 `json:"best_frame_gap_seconds"`

	// SequencePanels renders the eclipse progression poster: this many phases, stepping from a
	// shallow bite through maximum and back out, brought into one sky frame and laid out on one
	// sheet. 0 renders none. It needs an occulter to sequence, so it does nothing without TwoBody.
	//
	// The number is a REQUEST. Which phases exist on both sides of maximum is a property of the
	// recording, not of the preset, so a session with a gap in it gets the most panels it can pair
	// honestly and says how many that was.
	SequencePanels int `json:"sequence_panels"`
	// SequenceStack builds each panel by stacking its window as well as taking that window's single
	// sharpest frame, and keeps whichever resolves the occulter's edge better. It is OFF by default,
	// which is not a performance choice — it is what the pictures say.
	//
	// A stack of a crescent pays for registration twice. The solar arc is short, so the fitted centre
	// is poorly constrained perpendicular to it, and that scatter goes into every pixel: measured on
	// the 12 Aug 2026 clips the 834-frame master resolved the occulter's edge at sigma 2.30 px
	// against 1.09 for a single frame. Worse than soft, it is visibly WRONG — the recovered band the
	// occulter's sweep took out joins along a hard dark arc through the crescent, the cusps double,
	// and sharpening turns the averaged noise into a mottled crust. A single frame has none of that.
	SequenceStack bool `json:"sequence_stack"`
	// SequenceAngleDeg is the rise of the line the panels sit on, as it is seen: positive climbs to
	// the right. 0 lays them in a row.
	SequenceAngleDeg float64 `json:"sequence_angle_deg"`
	// SequenceSpacing is the centre-to-centre step in solar DIAMETERS, so it means the same thing
	// whatever the discs end up being rendered at. Below 1 the discs overlap. 0 → the default.
	SequenceSpacing float64 `json:"sequence_spacing"`
	// SiteLatDeg and SiteLonDeg (east-positive) override where the capture was made. They are only
	// needed when the clips carry no location tag of their own: an iPhone writes one, a camera
	// usually does not, and the phase of an eclipse cannot be computed without it. Left at 0,0 the
	// clips' own tag is used — which is also why a genuine observation from the Gulf of Guinea has
	// to nudge one of them.
	SiteLatDeg float64 `json:"site_lat"`
	SiteLonDeg float64 `json:"site_lon"`

	// Finish — re-renders from the persisted master in seconds.
	Finish FinishOptions `json:"finish"`
}

// DefaultPreset is the standard Hα recipe.
func DefaultPreset() Preset {
	return Preset{
		KeepPercent:    defaultKeepPercent,
		MaxFrames:      defaultMaxFrames,
		CropMargin:     defaultCropMargin,
		Drizzle:        1,
		ClipSigma:      stackClipSigma,
		APAlign:        true,
		WindowSeconds:  defaultWindowSeconds,
		WindowFrames:   defaultWindowFrames,
		MinFrames:      defaultMinWindowFrames,
		Band:           BandAuto,
		ScaleTolerance: defaultScaleTolerance,
		BracketMerge:   true,
		BracketStops:   bracketGapStops,

		TransparencyFloor: defaultTransparencyFloor,
		// The sequence's shape is defaulted here rather than in the eclipse preset so the two
		// presets stay identical outside the geometry, and so the knobs read the same whichever
		// mode a caller is tuning. They do nothing at all until SequencePanels is asked for.
		SequenceAngleDeg: DefaultSequenceLayout().AngleDeg,
		SequenceSpacing:  DefaultSequenceLayout().Spacing,
		Finish:           DefaultFinish(),
	}
}

// Tiers splits a group's members into the exposure tiers this preset would stack separately. A
// preset with the merge switched off always reports one tier, so callers have a single code path.
func (p Preset) Tiers(members []Member) [][]Member {
	if !p.BracketMerge {
		return [][]Member{keepLive(members)}
	}
	return ExposureTiers(members, p.BracketStops)
}

// IngestOpts projects the preset onto the ingest layer.
func (p Preset) IngestOpts(workDir, ffmpegBin string, targetRadius float64) IngestOptions {
	return IngestOptions{
		FFmpegBin: ffmpegBin, WorkDir: workDir,
		KeepPct: p.KeepPercent, MaxFrames: p.MaxFrames, CropMargin: p.CropMargin,
		TargetRadius: targetRadius, Band: p.Band, TransparencyFloor: p.TransparencyFloor,
		TwoBody: p.TwoBody,
	}
}

// StackOpts projects the preset onto the stacker.
func (p Preset) StackOpts() StackOptions {
	return StackOptions{Drizzle: p.Drizzle, CropMargin: p.CropMargin, ClipSigma: p.ClipSigma,
		APAlign: p.APAlign, APPoints: p.APPoints, APScale: p.APScale}
}

// WindowOpts projects the preset onto session windowing.
func (p Preset) WindowOpts() WindowOptions {
	return WindowOptions{Seconds: p.WindowSeconds, MaxFrames: p.WindowFrames, MinFrames: p.MinFrames}
}

// WantsNativeColour reports whether the run has to measure the recording's own colour before it can
// finish, which costs a handful of extra frame decodes.
func (p Preset) WantsNativeColour() bool { return p.Finish.Palette == PaletteNative }

// WantsSequence reports whether this run should render the phase-sequence poster. It takes an
// occulter to sequence an eclipse, so the two-body fit is part of the condition rather than a thing
// the caller has to remember.
func (p Preset) WantsSequence() bool { return p.TwoBody && p.SequencePanels >= 3 }

// SequenceLayoutOpts projects the preset onto the sheet layout.
func (p Preset) SequenceLayoutOpts() SequenceLayout {
	lay := DefaultSequenceLayout()
	lay.AngleDeg = p.SequenceAngleDeg
	if p.SequenceSpacing > 0 {
		lay.Spacing = p.SequenceSpacing
	}
	return lay
}

// TriageOpts projects the preset onto triage.
func (p Preset) TriageOpts(ffmpegBin string) Options {
	return Options{ScaleTolerance: p.ScaleTolerance, FFmpegBin: ffmpegBin, TwoBody: p.TwoBody}
}
