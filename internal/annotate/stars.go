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
	// Stars are the detected peaks inside the final crop, in final-image pixels, BRIGHTEST FIRST —
	// the individual stars behind Count, so the overlay can plot them and reveal fainter ones as you
	// zoom. Capped at maxPlottedStars: when len(Stars) < Count the list is the brightest slice, which
	// the UI can detect and say so. Unlike Labels these need no astrometric solution, so they are
	// present even on a run whose plate-solve failed.
	Stars []Point `json:"stars,omitempty"`
}

// Point is one detected star on the final image: where it is, how big it measured, what colour it
// is, and how bright. Enough for the overlay to draw a marker that looks like the star it marks and
// to answer "what is this?" on hover, without a second request. Integer pixels because the detector
// works on whole pixels and the mapping to final coordinates is a crop plus an optional row flip.
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
	// Rpx is the star's half-max radius in final-image pixels (≥1). Markers scale to this so a
	// bloated bright star gets a big ring and a faint pinprick a small one.
	Rpx float64 `json:"r_px,omitempty"`
	// Hex is the star's own colour sampled from the linear master, normalised to full brightness so
	// it stays legible as an outline ("#a8c8ff" for a hot blue star, "#ffcc99" for a cool one).
	Hex string `json:"hex,omitempty"`
	// Mag is the estimated apparent magnitude, or noMagSentinel when the frame could not be
	// photometrically anchored. Estimated — see magnitudeZeroPoint.
	Mag float64 `json:"mag,omitempty"`
	// RADeg/DecDeg are where this peak actually points, obtained by running the label chain backwards
	// (fromFinal → fileToWcs → PixToSky). Present on every star of a solved run, not only the
	// identified ones — an anonymous detection still has a real line of sight, which is what the 3D
	// field map places it along. Absent (both zero) on an unsolved run.
	RADeg  float64 `json:"ra_deg,omitempty"`
	DecDeg float64 `json:"dec_deg,omitempty"`
	// Star is the catalogue entry this peak was identified as, when one projects onto it. Present on
	// far more stars than Labels is: the label list is capped and separation-filtered so the image
	// stays readable, but hovering any marker should still answer "what is this?".
	Star *StarInfo `json:"star,omitempty"`
}

// StarInfo is everything the star catalogue knows about one identified star. It rides on both the
// text Labels and the plotted Points so the viewer renders one hover card either way.
//
// The three optional pointers are the fields where 0 is itself a measurement — an A0 star really
// has B−V = 0, and a star really can have zero radial velocity — so "absent" cannot be encoded as
// zero without inventing data.
type StarInfo struct {
	Name      string `json:"name,omitempty"`
	Secondary string `json:"secondary,omitempty"`
	// Mag is the catalogue's MEASURED V magnitude, unlike Point.Mag which is estimated from this
	// frame's pixels.
	Mag    float64  `json:"mag,omitempty"`
	RADeg  float64  `json:"ra_deg,omitempty"`
	DecDeg float64  `json:"dec_deg,omitempty"`
	DistPc float64  `json:"dist_pc,omitempty"` // 0 = unknown; no star sits at 0 pc
	AbsMag *float64 `json:"absmag,omitempty"`  // absolute V magnitude — intrinsic brightness
	CI     *float64 `json:"ci,omitempty"`      // B−V colour index — the star's colour, so its temperature
	RVKmS  *float64 `json:"rv_km_s,omitempty"` // radial velocity, km/s; positive = receding
	Spect  string   `json:"spect,omitempty"`   // MK spectral type, e.g. "G2 V"
	Con    string   `json:"con,omitempty"`     // 3-letter IAU constellation
	// Proper motion in mas/yr, PMRA carrying the cos δ factor (Hipparcos convention). With DistPc it
	// becomes a tangential velocity, and with RVKmS a full space velocity — which is what the 3D field
	// map's motion vectors are built from.
	PMRA  float64 `json:"pm_ra,omitempty"`
	PMDec float64 `json:"pm_dec,omitempty"`
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
	// Master→PNG row order, decided against the delivered pixels rather than the ROWORDER card (which
	// describes how the FITS was written, not what the finish did). RowOrder is
	// "measured" | "measured_overrode_card" | "roworder_card" (the fallback when the measurement is
	// inconclusive); RowFlip is the answer used; RowMatched/RowTried are the evidence behind it.
	RowOrder   string `json:"row_order,omitempty"`
	RowFlip    bool   `json:"row_flip,omitempty"`
	RowMatched int    `json:"row_matched,omitempty"`
	RowTried   int    `json:"row_tried,omitempty"`
	// MagZeroPoint anchors instrumental brightness to apparent magnitude, fitted from the catalogue
	// stars identified in this very frame. 0 = not anchored, and then no star carries a magnitude.
	MagZeroPoint float64 `json:"mag_zero_point,omitempty"`
	// Frame is the final image's anchoring on the sky. Present only on a solved run.
	Frame *Frame `json:"frame,omitempty"`
	// StarCatalog names which catalogue supplied the identifications: "athyg" (the downloaded deep
	// one, ~2.5 million stars) or "embedded" (the built-in magnitude-9 extract). Identified counts
	// how many plotted stars it actually resolved — the honest measure of how much a field is named.
	StarCatalog string `json:"star_catalog,omitempty"`
	Identified  int    `json:"identified,omitempty"`
}

// Frame anchors the final image on the sky: the sky positions of its centre and of the midpoints of
// its far x and y edges. Three points fix the image's orientation, its field of view AND its parity
// (the handedness of the two edge vectors), which is everything needed to rebuild the picture's
// geometry — without knowing anything about WCS rotation, the two empirical flips or the finish
// crop. Same argument that puts Extent in final-image pixels rather than sky angles: the
// orientation is only knowable here, where the validated solution and the mapping live, so it is
// settled here once instead of being re-derived by every consumer.
//
// Deliberately edge midpoints rather than a one-pixel step: a long baseline keeps the derived axes
// free of the rounding a 1 px difference of two nearly-equal angles would carry.
type Frame struct {
	CenterRA  float64 `json:"center_ra"`
	CenterDec float64 `json:"center_dec"`
	XEdgeRA   float64 `json:"x_edge_ra"` // sky position of (Wf-1, (Hf-1)/2)
	XEdgeDec  float64 `json:"x_edge_dec"`
	YEdgeRA   float64 `json:"y_edge_ra"` // sky position of ((Wf-1)/2, Hf-1)
	YEdgeDec  float64 `json:"y_edge_dec"`
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
	// Morphology is the catalogued Hubble class ("SA(s)b", "E+4"), carried because it is what separates
	// a galaxy that is a thin DISC — whose projected axis ratio therefore gives a real inclination —
	// from a spheroid, whose projection determines nothing about its three-dimensional shape.
	Morphology string `json:"morphology,omitempty"`
	// MinorAxis is the catalogued minor axis in arcminutes. Extent already carries the footprint in
	// pixels, but the ANGULAR axis ratio is what the inclination is derived from, and recovering it
	// from pixels would fold in the projection's own distortion.
	MinorAxis float64 `json:"minor_axis_arcmin,omitempty"`
	// Star carries the catalogue's astrophysics for a kind=="star" label (nil for DSOs).
	Star *StarInfo `json:"star,omitempty"`
	// Extent is the object's catalogued footprint already projected into FINAL-IMAGE PIXELS, so the
	// renderer can outline it without knowing anything about WCS rotation, parity or the finish crop.
	// Absent for stars and for DSOs with no catalogued size.
	Extent *Extent `json:"extent,omitempty"`
}

// Extent is one DSO's elliptical footprint on the final image: semi-axes in final-image pixels and
// the major axis's angle in image space (radians, from +x toward +y — i.e. ready for canvas
// `ellipse()`). Pixels rather than sky angles because the sky→image rotation is only knowable here,
// where the validated WCS and the crop mapping live.
type Extent struct {
	RXpx     float64 `json:"rx_px"`
	RYpx     float64 `json:"ry_px"`
	AngleRad float64 `json:"angle_rad"`
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
