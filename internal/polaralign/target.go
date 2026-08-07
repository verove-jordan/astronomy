package polaralign

import (
	"time"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// Turning the correction into a mark on the live image.
//
// Numbers in arcminutes are the right answer and the wrong interface: at the mount, in the dark, with
// one hand on a bolt, "lower it by eight arcminutes" is not actionable. What is actionable is a spot on
// the screen and "put the crosshairs on that".
//
// The camera is bolted to the mount, so whatever rotation the two adjusters apply to the polar axis
// they apply to the camera as well. If R is the rotation that puts the axis on the pole, the optical
// axis goes from C to R·C — so R·C is the piece of sky that will be in the middle of the frame once the
// adjustment is done, and drawing it on the CURRENT frame turns the whole procedure into driving one
// marker into the crosshairs. Both bolts at once, no arithmetic, no sign to get backwards.
//
// It is R·C and not R⁻¹·C: the mount carries the camera with it. Getting that backwards puts the marker
// on the diametrically wrong side, and the user chases it away from alignment — which is why
// TestTarget_MovesEastForAnEastwardAxis asserts the compass direction and not just the distance.

// Frame is one plate-solved image: the solution, its size, and when it was taken.
type Frame struct {
	WCS      fits.WCS
	WidthPx  int
	HeightPx int
	// At is the mid-exposure instant. It is used for the sidereal angle, so borrowing "now" from a
	// frame taken thirty seconds ago costs seven arcminutes.
	At time.Time
}

// centrePix is the pixel the crosshairs sit on.
func (f Frame) centrePix() (x, y float64) {
	return float64(f.WidthPx) / 2, float64(f.HeightPx) / 2
}

// Target is where to aim, in sky and in pixels.
type Target struct {
	// RADeg/DecDeg is J2000, matching the plate solution.
	RADeg  float64 `json:"ra_deg"`
	DecDeg float64 `json:"dec_deg"`
	// X/Y is the 0-based pixel in the FITS axis frame — x along axis 1, y along axis 2. Whether axis 2
	// runs down the displayed image or up it depends on the row order of the file that was solved, so
	// the caller that knows about ROWORDER owns that flip, not this package.
	X float64 `json:"x"`
	Y float64 `json:"y"`
	// NX/NY are the same point as a fraction of the frame, which is what an overlay drawn over a
	// downsampled preview actually needs.
	NX float64 `json:"nx"`
	NY float64 `json:"ny"`
	// OffsetPx is how far the marker sits from the crosshairs.
	OffsetPx float64 `json:"offset_px"`
	// OffFrame is set when the marker falls outside the image, which is the NORMAL case on the first
	// measurement: a degree of error with a one-degree field puts it off the edge every time. The UI
	// shows an arrow and the numbers until it comes into view.
	OffFrame bool `json:"off_frame"`
	// OffsetArcmin is the same distance on the sky, which stays meaningful when the marker is off frame.
	OffsetArcmin float64 `json:"offset_arcmin"`
}

// Target computes the mark for one solved frame. ok is false when the frame carries no usable solution
// or the target lands more than 90° away, which means something is badly wrong upstream.
func (c Correction) Target(f Frame, opt FitOptions) (Target, bool) {
	if f.WidthPx <= 0 || f.HeightPx <= 0 || f.At.IsZero() {
		return Target{}, false
	}
	// Where the tube is mechanically pointed, where it will be pointed, and what that is called in the
	// catalogue coordinates the WCS speaks.
	here := frameCentreDir(f, c.site, opt)
	there := c.rotation().apply(here)
	raJ2000, decJ2000 := skyFromDir(there, c.site, f.At, opt)

	return targetPixel(f, raJ2000, decJ2000, angleBetween(here, there))
}
