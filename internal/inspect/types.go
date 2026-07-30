// Package inspect walks a capture directory, reads each file's FITS metadata, classifies
// every frame (light/dark/flat/bias/dark-flat/video), and groups frames into sets that the
// calibration and stacking stages consume.
package inspect

// FrameType is the kind of an astrophotography capture frame.
type FrameType string

const (
	Light    FrameType = "LIGHT"
	Dark     FrameType = "DARK"
	Flat     FrameType = "FLAT"
	Bias     FrameType = "BIAS"
	DarkFlat FrameType = "DARKFLAT"
	Video    FrameType = "VIDEO"
	Unknown  FrameType = "UNKNOWN"
)

// ClassSource records how a frame's type was determined.
const (
	SourceHeader    = "header"    // from the FITS IMAGETYP card
	SourceFilename  = "filename"  // from the file name / folder (e.g. Light_..._filter-B_...)
	SourceHeuristic = "heuristic" // inferred from exposure + pixel statistics
	SourceExtension = "extension" // from the file extension (videos)
	SourceSignal    = "signal"    // filter inferred from the signal via wheel-order detection
	SourceManual    = "manual"    // filter set by an explicit user override
	SourceManifest  = "manifest"  // filter/gain from an info.txt sidecar describing capture order
	SourceWheel     = "wheel"     // filter named from the physical EFW slot (sidecar/filename) via a legend
)

// Frame is one capture file plus the metadata extracted from it.
type Frame struct {
	Path       string    `json:"path"`
	Type       FrameType `json:"type"`
	Filter     string    `json:"filter,omitempty"`
	ExposureMs int64     `json:"exposure_ms"`
	Gain       int64     `json:"gain"`
	// HasGain distinguishes "no GAIN metadata anywhere" from a real gain of 0 (a legitimate ZWO
	// setting): photometric normalization must not apply a gain law to a value that is merely the
	// int zero-value. Set wherever Gain is filled (header, sidecar, filename, manifest legend).
	HasGain bool  `json:"has_gain,omitempty"`
	Offset  int64 `json:"offset"`
	// ISO is the camera ISO speed for phone/DSLR raws (read from EXIF); 0 for cooled-camera FITS,
	// which use Gain/Offset instead. It keys phone calibration masters the way gain does for the ZWO.
	ISO        int64 `json:"iso,omitempty"`
	TempMilliC int64 `json:"temp_milli_c"`
	HasTemp    bool  `json:"has_temp"`
	BinX       int   `json:"bin_x"`
	BinY       int   `json:"bin_y"`
	Width      int   `json:"width"`
	Height     int   `json:"height"`
	// Bayer is the colour-filter-array pattern (e.g. "GRBG") for one-shot-color frames; "" = monochrome.
	// OSC frames must be debayered, so the mono per-filter pipeline excludes them.
	Bayer       string `json:"bayer,omitempty"`
	Object     string `json:"object,omitempty"`
	Instrument string `json:"instrument,omitempty"`
	// Creator is the capture software (SWCREATE), e.g. "ASICAP" / "SharpCap". Old ASICAP writes NO
	// INSTRUME card, so this is the only in-header evidence the ZWO gain law applies (task #354:
	// its absence silently dropped the 10^(Δgain/200) factor across a g0–g450 five-night merge).
	Creator   string `json:"creator,omitempty"`
	Telescope string `json:"telescope,omitempty"`
	DateObs     string `json:"date_obs,omitempty"`
	DateObsMs   int64  `json:"date_obs_ms,omitempty"`
	// Session is the capture-night key ("YYYY-MM-DD", local-noon bucketed from DateObsMs; "" when
	// undated). Stamped at frame construction — NEVER in a later pass, because ScanCache shares
	// frames read-only across scans. See session.go.
	Session     string `json:"session,omitempty"`
	ClassSource string `json:"class_source"`
	// WheelSlot is the physical filter-wheel (EFW) position, 1-based; 0 = unknown. Read from the
	// SharpCap sidecar or the filename; named to a filter via a legend (info.txt / default order).
	WheelSlot int `json:"wheel_slot,omitempty"`
	// FilterConfidence is set when the filter was inferred from the signal (ClassSource "signal").
	FilterConfidence float64 `json:"filter_confidence,omitempty"`
	// WheelTransition marks a frame whose brightness is off because the filter wheel was still
	// moving when it was taken (the first frame of a run). The pipeline may drop these.
	WheelTransition bool `json:"wheel_transition,omitempty"`
	// Plate-solving hints read from the header when present (else config defaults are used).
	FocalLenMM  float64 `json:"focal_len_mm,omitempty"`
	PixelSizeUm float64 `json:"pixel_size_um,omitempty"`
	ObjCtRA     string  `json:"objctra,omitempty"`
	ObjCtDec    string  `json:"objctdec,omitempty"`
}

// ExposureSec is the exposure time in seconds.
func (f *Frame) ExposureSec() float64 { return float64(f.ExposureMs) / 1000 }

// TempC is the sensor temperature in °C (valid only when HasTemp).
func (f *Frame) TempC() float64 { return float64(f.TempMilliC) / 1000 }

// SetKey identifies a group of compatible frames. Fields irrelevant to a type are zero
// (e.g. darks carry no Filter/Object; bias carries no Exposure/Filter).
type SetKey struct {
	Type       FrameType `json:"type"`
	Object     string    `json:"object,omitempty"`
	Filter     string    `json:"filter,omitempty"`
	ExposureMs int64     `json:"exposure_ms"`
	Gain       int64     `json:"gain"`
	Offset     int64     `json:"offset"`
	ISO        int64     `json:"iso,omitempty"`
	TempBucket int       `json:"temp_bucket_c"`
	Bin        int       `json:"bin"`
	// Session is the capture-night key, set ONLY when the scan spans several known nights and ONLY
	// for lights + flats (a night owns its sky/dust state; darks/bias are night-agnostic thermal
	// signatures pooled across sessions). Zero for every single-night scan — sets, sort order and
	// master names stay byte-identical to the pre-sessionization behavior. See buildSets.
	Session string `json:"session,omitempty"`
}

// Set is a group of frames that share a SetKey.
type Set struct {
	Key                SetKey   `json:"key"`
	Frames             []*Frame `json:"-"`
	Count              int      `json:"count"`
	TotalIntegrationMs int64    `json:"total_integration_ms"`
}

// Inventory is the full result of scanning a directory.
type Inventory struct {
	Root             string            `json:"root"`
	Frames           []*Frame          `json:"frames"`
	Sets             []Set             `json:"sets"`
	Videos           []*Frame          `json:"videos"`
	Warnings         []string          `json:"warnings"`
	ChannelDetection *ChannelDetection `json:"channel_detection,omitempty"`
	// Sessions summarizes the capture nights found in the scan (per-night counts, time window and
	// light configs), sorted by night. nil when no frame carries a DATE-OBS. See session.go.
	Sessions []SessionInfo `json:"sessions,omitempty"`
}

// SessionInfo summarizes one capture night of the scan (Key "" = the undated bucket).
type SessionInfo struct {
	Key     string            `json:"key"`
	StartMs int64             `json:"start_ms,omitempty"`
	EndMs   int64             `json:"end_ms,omitempty"`
	Counts  map[FrameType]int `json:"counts"`
	Configs []SessionConfig   `json:"configs,omitempty"`
}

// SessionConfig is one distinct light-capture configuration within a night, with its frame count.
type SessionConfig struct {
	Filter     string `json:"filter,omitempty"`
	ExposureMs int64  `json:"exposure_ms"`
	Gain       int64  `json:"gain"`
	Offset     int64  `json:"offset"`
	Bin        int    `json:"bin"`
	TempBucket int    `json:"temp_bucket_c"`
	Count      int    `json:"count"`
}

// ChannelDetection summarizes signal-based filter detection so the UI can show and override it.
type ChannelDetection struct {
	Order             []string      `json:"order"`
	OverallConfidence float64       `json:"overall_confidence"`
	Runs              []DetectedRun `json:"runs"`
}

// DetectedRun is one contiguous same-filter capture block found by detection.
type DetectedRun struct {
	Filter          string  `json:"filter"`
	Count           int     `json:"count"`
	Confidence      float64 `json:"confidence"`
	FirstFrame      string  `json:"first_frame"`
	LastFrame       string  `json:"last_frame"`
	WheelTransition int     `json:"wheel_transition,omitempty"`
}

// CountsByType returns the number of frames of each type.
func (inv *Inventory) CountsByType() map[FrameType]int {
	counts := make(map[FrameType]int)
	for _, f := range inv.Frames {
		counts[f.Type]++
	}
	for range inv.Videos {
		counts[Video]++
	}
	return counts
}

// LightFilters returns the distinct filters present among light frames, in stable order.
func (inv *Inventory) LightFilters() []string {
	seen := make(map[string]bool)
	var out []string
	for _, f := range inv.Frames {
		if f.Type == Light && f.Filter != "" && !seen[f.Filter] {
			seen[f.Filter] = true
			out = append(out, f.Filter)
		}
	}
	return out
}

// SetsOfType returns the grouped sets matching a frame type.
func (inv *Inventory) SetsOfType(t FrameType) []Set {
	var out []Set
	for _, s := range inv.Sets {
		if s.Key.Type == t {
			out = append(out, s)
		}
	}
	return out
}

// ExcludeBayer removes one-shot-color (Bayer) frames from the inventory and rebuilds the sets, returning
// how many were removed. The monochrome per-filter pipeline cannot stack OSC mosaics, so they are dropped
// here (the caller warns); the OSC pipeline processes them instead.
func (inv *Inventory) ExcludeBayer() int {
	kept := inv.Frames[:0:0]
	removed := 0
	for _, fr := range inv.Frames {
		if fr.Bayer != "" {
			removed++
			continue
		}
		kept = append(kept, fr)
	}
	if removed == 0 {
		return 0
	}
	inv.Frames = kept
	inv.Sets = buildSets(inv.Frames)
	return removed
}
