package solar

// preset.go is the mode's tunable surface, split the way the supervised finish and the Refine panel
// need it: the finish re-renders a persisted master in seconds, while anything touching ingest or
// registration means going back to the frames.

// Preset is everything a solar run can be tuned by.
type Preset struct {
	// Ingest and stacking — changing any of these means re-reading the source frames.
	KeepPercent   int     `json:"keep_percent"`
	MaxFrames     int     `json:"max_frames"`
	CropMargin    float64 `json:"crop_margin"`
	Drizzle       float64 `json:"drizzle"`
	ClipSigma     float64 `json:"clip_sigma"`
	APAlign       bool    `json:"ap_align"`
	APPoints      int     `json:"align_points"`
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
		Finish:         DefaultFinish(),
	}
}

// IngestOpts projects the preset onto the ingest layer.
func (p Preset) IngestOpts(workDir, ffmpegBin string, targetRadius float64) IngestOptions {
	return IngestOptions{
		FFmpegBin: ffmpegBin, WorkDir: workDir,
		KeepPct: p.KeepPercent, MaxFrames: p.MaxFrames, CropMargin: p.CropMargin,
		TargetRadius: targetRadius, Band: p.Band,
	}
}

// StackOpts projects the preset onto the stacker.
func (p Preset) StackOpts() StackOptions {
	return StackOptions{Drizzle: p.Drizzle, CropMargin: p.CropMargin, ClipSigma: p.ClipSigma,
		APAlign: p.APAlign, APPoints: p.APPoints}
}

// WindowOpts projects the preset onto session windowing.
func (p Preset) WindowOpts() WindowOptions {
	return WindowOptions{Seconds: p.WindowSeconds, MaxFrames: p.WindowFrames, MinFrames: p.MinFrames}
}

// TriageOpts projects the preset onto triage.
func (p Preset) TriageOpts(ffmpegBin string) Options {
	return Options{ScaleTolerance: p.ScaleTolerance, FFmpegBin: ffmpegBin}
}
