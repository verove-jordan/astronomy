// Package optics detects optical defects (dust "donuts", blotches, blobs) in master flats,
// runs quality-control on flats, and repairs residual flat-correction defects that survive into
// calibrated light frames. It is a pure-Go, dependency-light sibling of the calibration pipeline:
// every exported entry point soft-fails (returns an error or Notes rather than panicking) so a
// caller can safely ignore a failure and fall back to the standard calibration path.
//
// The detector works on a downsampled, mono "flat plane" (see LoadFlatPlane): a Bayer master is
// collapsed to a mono superpixel image, an RGB master is averaged across channels, and the result
// is mean-pooled so its long axis is <=1024px. All geometry the detector reports (centroids,
// bounding boxes, areas, equivalent diameters) is mapped back to FULL-RESOLUTION sensor pixels via
// the linear `scale` factor LoadFlatPlane returns, so downstream repair addresses the real light
// frames. A defect's repair profile (Shape) is kept at detection scale and re-sampled onto the
// full-res bbox when measuring/repairing.
package optics

// Detection & QC thresholds. These are deliberately conservative: the detector is meant to flag
// obvious dust shadows and grade a flat, not to catch every faint gradient.
const (
	// defectMinDepth is the minimum fractional dip (1 - plane/smooth) for a pixel to be considered
	// part of a defect. 1.5% is comfortably above flat-field shot noise on a stacked master.
	defectMinDepth = 0.015
	// defectMinAreaPx is the minimum connected-component size (in DETECTION pixels) to keep — smaller
	// blobs are hot/cold pixels or noise, not dust shadows.
	defectMinAreaPx = 9
	// defectMaxAreaFrac caps a defect at 2% of the plane; anything larger is vignetting or a lamp
	// gradient the smooth model failed to absorb, not a discrete defect. Reused by RepairFrames as
	// the "too large to touch" guard against the light frame.
	defectMaxAreaFrac = 0.02

	// saturBad: more than 2% of raw-flat pixels at/above 98% of full scale means the flat is clipped
	// and cannot be used to divide out the optical response.
	saturBad = 0.02

	// Level (median exposure as a fraction of full scale) bands. Warn outside [0.15,0.85]; fail
	// outside [0.08,0.92] — too dim starves the correction of SNR, too bright risks non-linearity.
	levelWarnLo = 0.15
	levelWarnHi = 0.85
	levelBadLo  = 0.08
	levelBadHi  = 0.92

	// vignetteWarn: corner falloff deeper than 50% relative to the center is an aggressive optical
	// train that will amplify calibration noise in the corners.
	vignetteWarn = 0.5
	// defectScoreWarn: integrated defect burden Sum(meanDepth*areaPx)/totalPx above which we warn.
	defectScoreWarn = 5e-4
	// deepDefectWarn: any single defect deeper than 5% warrants a warning even if the burden is low.
	deepDefectWarn = 0.05
)

// FlatQC is the quality-control verdict for a master flat. Level/VignetteDepth/DeadFrac are fractions
// in [0,1]; TileMin/TileMax are the extremes of (local tile mean / global median).
type FlatQC struct {
	Level         float64  `json:"level"`
	SaturFrac     float64  `json:"satur_frac"`
	VignetteDepth float64  `json:"vignette_depth"`
	TileMin       float64  `json:"tile_min"`
	TileMax       float64  `json:"tile_max"`
	DeadFrac      float64  `json:"dead_frac"`
	Status        string   `json:"status"` // ""|"ok"|"warn"|"bad"
	Notes         []string `json:"notes,omitempty"`
}

// Defect is one detected optical defect. CX/CY, X0..Y1 and AreaPx are in FULL-RESOLUTION sensor
// pixels (mapped from detection scale via LoadFlatPlane's `scale`). Depth is the deepest fractional
// dip inside the defect, MeanDepth its average dip. Donut marks a hollow (dust-shadow) defect. Shape
// is the normalized (d/Depth in [0,1]) deviation kernel at DETECTION scale, re-sampled onto the
// full-res bbox by RepairFrames/MeasureResiduals; it is not serialized.
type Defect struct {
	CX        int       `json:"cx"`
	CY        int       `json:"cy"`
	X0        int       `json:"x0"`
	Y0        int       `json:"y0"`
	X1        int       `json:"x1"`
	Y1        int       `json:"y1"`
	AreaPx    int       `json:"area_px"`
	Depth     float64   `json:"depth"`
	MeanDepth float64   `json:"mean_depth"`
	Donut     bool      `json:"donut"`
	Shape     []float32 `json:"-"`
}

// clampInt constrains v to [lo,hi].
func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// clampF constrains v to [lo,hi].
func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
