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
	SourceHeuristic = "heuristic" // inferred from exposure + pixel statistics
	SourceExtension = "extension" // from the file extension (videos)
)

// Frame is one capture file plus the metadata extracted from it.
type Frame struct {
	Path        string    `json:"path"`
	Type        FrameType `json:"type"`
	Filter      string    `json:"filter,omitempty"`
	ExposureMs  int64     `json:"exposure_ms"`
	Gain        int64     `json:"gain"`
	Offset      int64     `json:"offset"`
	TempMilliC  int64     `json:"temp_milli_c"`
	HasTemp     bool      `json:"has_temp"`
	BinX        int       `json:"bin_x"`
	BinY        int       `json:"bin_y"`
	Width       int       `json:"width"`
	Height      int       `json:"height"`
	Object      string    `json:"object,omitempty"`
	Instrument  string    `json:"instrument,omitempty"`
	Telescope   string    `json:"telescope,omitempty"`
	DateObs     string    `json:"date_obs,omitempty"`
	DateObsMs   int64     `json:"date_obs_ms,omitempty"`
	ClassSource string    `json:"class_source"`
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
	TempBucket int       `json:"temp_bucket_c"`
	Bin        int       `json:"bin"`
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
	Root     string   `json:"root"`
	Frames   []*Frame `json:"frames"`
	Sets     []Set    `json:"sets"`
	Videos   []*Frame `json:"videos"`
	Warnings []string `json:"warnings"`
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
