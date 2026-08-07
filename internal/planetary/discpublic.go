package planetary

import "github.com/verove-jordan/astronomy/internal/fits"

// discpublic.go exports the limb-circle fit so other packages can LOCATE a bright body's disc
// without re-implementing the RANSAC + Kåsa machinery in disc.go. The Sun is a strictly easier
// target than the Moon (no terminator — every boundary point is a limb point), so the same fit
// works, but see the precision note on FitDisc before using it for registration.

// DiscFit is a fitted limb circle in FULL-resolution pixel coordinates, with the quality figures a
// caller needs to decide how much to trust it.
type DiscFit struct {
	CX       float64 `json:"cx"`
	CY       float64 `json:"cy"`
	R        float64 `json:"r"`
	Inliers  int     `json:"inliers"`
	ArcDeg   float64 `json:"arc_deg"`   // how much of the limb voted for this circle
	ResidMAD float64 `json:"resid_mad"` // median absolute limb-distance residual, full px
}

// FitDisc fits the limb circle of the bright body on the image's first plane. ok=false means "no
// confident disc" and the caller must skip, never guess.
//
// PRECISION: the fit runs on a discDown× box-downsample, so expect ~±1 full-px radius error. That is
// ample to locate a disc, to reject a frame with no visible limb, and to CLUSTER frames by image
// scale (a 1 px error on a ~600 px radius is 0.17%, well inside any sane grouping tolerance). It is
// NOT enough to register frames against each other: 0.1% of scale error is half a pixel of smear at
// the limb, injected into every frame. Sub-pixel registration uses internal/solar's radial-profile
// fit instead, seeded from this one.
func FitDisc(im *fits.Image) (DiscFit, bool) {
	f, ok := fitLunarDisc(im)
	if !ok {
		return DiscFit{}, false
	}
	return DiscFit{CX: f.CX, CY: f.CY, R: f.R, Inliers: f.Inliers, ArcDeg: f.ArcDeg, ResidMAD: f.ResidMAD}, true
}
