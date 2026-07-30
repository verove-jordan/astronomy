package annotate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// starsFileName is the persisted annotation beside the run's outputs.
const starsFileName = "stars.json"

// Result is one run's star annotation: the star count on the linear master (windowed to the final
// image) plus name labels in FINAL-image pixel coordinates. It is persisted as
// <runDir>/stars.json and returned verbatim by the API.
type Result struct {
	Version    int     `json:"version"`
	Engine     string  `json:"engine,omitempty"`
	ComputedAt string  `json:"computed_at"`
	SourceFits string  `json:"source_fits"` // run-relative linear master the count/labels used
	Count      int     `json:"count"`
	Image      Dims    `json:"image"` // final image dims — the labels' coordinate space
	Solved     bool    `json:"solved"`
	Solve      Solve   `json:"solve"`
	Labels     []Label `json:"labels"`
}

// Dims is the final image's pixel size.
type Dims struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Solve records how (and whether) the astrometric solution was obtained and validated.
type Solve struct {
	Method      string  `json:"method"` // pipeline | cached | resolved | none
	Flip        bool    `json:"flip,omitempty"`
	Matched     int     `json:"matched,omitempty"`
	Tried       int     `json:"tried,omitempty"`
	CenterRA    float64 `json:"center_ra,omitempty"`
	CenterDec   float64 `json:"center_dec,omitempty"`
	RadiusDeg   float64 `json:"radius_deg,omitempty"`
	ScaleArcsec float64 `json:"scale_arcsec_px,omitempty"`
	Reason      string  `json:"reason,omitempty"` // set when Solved=false
}

// noMagSentinel ranks DSO labels without a catalogued magnitude after everything else.
const noMagSentinel = 99

// Label is one named object positioned on the final image.
type Label struct {
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Name      string  `json:"name"`
	Secondary string  `json:"secondary,omitempty"`
	Kind      string  `json:"kind"` // star | dso
	Type      string  `json:"type,omitempty"`
	Mag       float64 `json:"mag"`
	Diameter  float64 `json:"diameter_arcmin,omitempty"`
}

// Load reads a previously computed <runDir>/stars.json. ok=false when absent or unreadable.
func Load(runDir string) (*Result, bool) {
	b, err := os.ReadFile(filepath.Join(runDir, starsFileName))
	if err != nil {
		return nil, false
	}
	var r Result
	if json.Unmarshal(b, &r) != nil {
		return nil, false
	}
	return &r, true
}

// write persists the annotation atomically (temp + rename) so a concurrent reader never sees a
// torn file.
func (r *Result) write(runDir string) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal stars.json: %w", err)
	}
	tmp, err := os.CreateTemp(runDir, ".stars-*.json")
	if err != nil {
		return fmt.Errorf("write stars.json: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("write stars.json: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write stars.json: %w", err)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return fmt.Errorf("write stars.json: %w", err)
	}
	return os.Rename(tmp.Name(), filepath.Join(runDir, starsFileName))
}
