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
	// A Bayer frame is still ONE plane of data — it has to be debayered before it is colour.
	Bayer string `json:"bayer,omitempty"`
	// Channels is how many planes the frame carries: 1 for monochrome and for undebayered CFA, 3 for
	// an already-demosaiced RGB frame (a developed camera raw, a colour TIFF/PNG/JPEG, a FITS with
	// NAXIS3=3). 0 means "not determined" and is read as 1.
	//
	// Bayer and Channels together name the three states the pipeline has to tell apart, which a
	// single flag could not: mono (Bayer=="" && Channels<=1), CFA awaiting debayer (Bayer!=""), and
	// RGB (Channels==3). Conflating the last two is what made a debayered colour frame look mono.
	Channels   int    `json:"channels,omitempty"`
	Object     string `json:"object,omitempty"`
	Instrument string `json:"instrument,omitempty"`
	// Creator is the capture software (SWCREATE), e.g. "ASICAP" / "SharpCap". Old ASICAP writes NO
	// INSTRUME card, so this is the only in-header evidence the ZWO gain law applies (task #354:
	// its absence silently dropped the 10^(Δgain/200) factor across a g0–g450 five-night merge).
	Creator   string `json:"creator,omitempty"`
	Telescope string `json:"telescope,omitempty"`
	DateObs   string `json:"date_obs,omitempty"`
	DateObsMs int64  `json:"date_obs_ms,omitempty"`
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
	// AzDeg/AltDeg/RollDeg are where the camera was aimed, recovered from a phone's compass bearing
	// and gravity vector (see internal/pointing). HasPointing separates "no such metadata" from a
	// genuine zero — only phone raws carry it, and a missing tilt read as 0 would say "horizon".
	// This is what lets a hand-framed session be split into panels, and what proves a frame shot
	// with the camera aimed at the ground cannot be a light.
	AzDeg       float64 `json:"az_deg,omitempty"`
	AltDeg      float64 `json:"alt_deg,omitempty"`
	RollDeg     float64 `json:"roll_deg,omitempty"`
	HasPointing bool    `json:"has_pointing,omitempty"`
	// Plate-solving hints read from the header when present (else config defaults are used).
	FocalLenMM  float64 `json:"focal_len_mm,omitempty"`
	PixelSizeUm float64 `json:"pixel_size_um,omitempty"`
	ObjCtRA     string  `json:"objctra,omitempty"`
	ObjCtDec    string  `json:"objctdec,omitempty"`
}

// IsColor reports whether the frame carries all three primaries — either as an undebayered CFA
// mosaic or as three already-demosaiced planes. Prefer this over testing Bayer directly: a
// developed DSLR raw and a colour TIFF have no Bayer pattern yet are unambiguously colour.
func (f *Frame) IsColor() bool { return f.Bayer != "" || f.Channels >= 3 }

// NeedsDebayer reports whether the frame is still a raw CFA mosaic, so calibration has to run in
// CFA-aware mode and demosaic afterwards (Siril `-cfa -equalize_cfa -debayer`). An already-RGB
// frame must NOT take that path.
//
// A CAMERA RAW qualifies on its extension alone, before the header terms are consulted, because its
// metadata actively lies about this: finalizeRawTypes stamps Channels = 3 on every camera raw to mark
// it one-shot-color for the paths that DEVELOP it to RGB (nightscape via sips), and a raw carries no
// BAYERPAT card to set Bayer from. Both header terms therefore read "already RGB" for a file that is
// in fact a mosaic — which silently produced a MONOCHROME deep-sky stack from a Nikon NEF session:
// no -debayer was passed, the whole run averaged the Bayer mosaic, and it only surfaced at the very
// end as `rmgreen: command is not for monochrome images`.
func (f *Frame) NeedsDebayer() bool {
	if isCFARaw(f.Path) {
		return true
	}
	return f.Bayer != "" && f.Channels < 3
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
	// Color separates one-shot-color frames from monochrome ones. A colour frame and a mono frame at
	// the same exposure/gain/temperature are NOT interchangeable — stacking them together, or
	// calibrating one with the other's master, produces nonsense — so a folder holding both a mono
	// session and an OSC session must not merge them into one set. False (the zero value) for every
	// all-mono scan, so existing set IDs, ordering and master names are unchanged.
	Color bool `json:"color,omitempty"`
}

// Set is a group of frames that share a SetKey.
type Set struct {
	Key                SetKey   `json:"key"`
	Frames             []*Frame `json:"-"`
	Count              int      `json:"count"`
	TotalIntegrationMs int64    `json:"total_integration_ms"`
}

// ColorModel is how a whole scan records colour, decided once in finalizeInventory and carried to
// the pipeline so the routing decision is made in exactly one place.
type ColorModel string

const (
	// ColorMono is the classic rig: a monochrome sensor behind a filter wheel, stacked per filter
	// and combined into LRGB. The default for anything with no colour evidence.
	ColorMono ColorModel = "mono"
	// ColorOSC is one-shot color: every light carries all three primaries (Bayer CFA, DSLR raw, or
	// an already-debayered RGB still). Stacked as a single RGB channel, no combine step.
	ColorOSC ColorModel = "osc"
	// ColorMixed is a folder holding both, which no single run can stack. The pipeline keeps the mono
	// frames and warns about the colour ones rather than guessing which the user meant.
	ColorMixed ColorModel = "mixed"
)

// Inventory is the full result of scanning a directory.
type Inventory struct {
	Root             string            `json:"root"`
	Frames           []*Frame          `json:"frames"`
	Sets             []Set             `json:"sets"`
	Videos           []*Frame          `json:"videos"`
	Warnings         []string          `json:"warnings"`
	ChannelDetection *ChannelDetection `json:"channel_detection,omitempty"`
	// ColorModel is the scan's overall colour verdict (see ColorModel). Computed in finalize from the
	// LIGHT frames only — calibration frames follow whatever rig shot them.
	ColorModel ColorModel `json:"color_model,omitempty"`
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

// ExcludeBayer removes undebayered Bayer-CFA frames from the inventory and rebuilds the sets,
// returning how many were removed. Kept for callers that specifically cannot handle a raw CFA mosaic;
// to drop every one-shot-color frame — including already-debayered RGB, which carries no Bayer
// pattern — use ExcludeColor.
func (inv *Inventory) ExcludeBayer() int {
	return inv.excludeFrames(func(fr *Frame) bool { return fr.Bayer != "" })
}

// ExcludeColor removes every one-shot-color frame (raw CFA *and* already-debayered RGB) and rebuilds
// the sets, returning how many were removed. This is what a mono-only run needs from a folder that
// mixes both: ExcludeBayer alone would leave a debayered colour frame behind to be stacked as if it
// were luminance.
func (inv *Inventory) ExcludeColor() int {
	return inv.excludeFrames(func(fr *Frame) bool { return fr.IsColor() })
}

// excludeFrames drops every frame matching drop and rebuilds the sets. Only the Inventory's slices
// are touched — Frame structs are never mutated — so it is safe on ScanCache-shared frames.
func (inv *Inventory) excludeFrames(drop func(*Frame) bool) int {
	kept := inv.Frames[:0:0]
	removed := 0
	for _, fr := range inv.Frames {
		if drop(fr) {
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
