package skypano

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/pointing"
)

// frame35mmDiagonal is the 35 mm format diagonal, which FocalLength35mm is quoted against.
const frame35mmDiagonal = 43.267

// FocalPixels converts a 35 mm-equivalent focal length into pixels for a frame of the given size.
// The equivalence is defined on the DIAGONAL, so the sensor's diagonal in pixels is what scales.
func FocalPixels(focal35mm float64, w, h int) float64 {
	if focal35mm <= 0 || w <= 0 || h <= 0 {
		return 0
	}
	return focal35mm * math.Hypot(float64(w), float64(h)) / frame35mmDiagonal
}

// PriorCamera builds the starting camera for a frame from what the phone recorded: where it was
// aimed, when, and from where. w and h are the dimensions of the image AS STORED — before EXIF
// rotation — because that is the array the pixels actually live in.
//
// The orientation code ties the two together. A phone held in portrait stores its pixels landscape
// and leaves the rotation to EXIF, so the image's "up" runs along a stored AXIS rather than along
// stored -y. Getting that mapping wrong rotates the panel by 90 degrees, which no refinement
// recovers.
//
// rowsBottomUp says whether the array's FIRST row is the bottom of the picture. FITS says it is, and
// that is what Siril writes, so anything read back through internal/fits needs true here. This is
// not a detail that degrades gracefully: a flip mirrors every asterism, and quad codes are not
// reflection-invariant, so the solve returns pure chance rather than a slightly worse answer.
// Measured on a real panel — 81 inliers unflipped against 578 flipped.
func PriorCamera(f pointing.Frame, orientation, w, h int, rowsBottomUp bool) (Camera, bool) {
	axis, right, up, ok := f.Basis()
	if !ok || w <= 0 || h <= 0 {
		return Camera{}, false
	}
	camRight, camDown, ok := storedAxes(orientation, right, up)
	if !ok {
		return Camera{}, false
	}
	if rowsBottomUp {
		camDown = neg3(camDown)
	}
	return Camera{
		R:  SetRotation(camRight, camDown, axis),
		Cx: float64(w) / 2,
		Cy: float64(h) / 2,
	}, true
}

// storedAxes maps the DISPLAYED image's right/up onto the STORED array's +x/+y directions, for the
// four unmirrored EXIF orientations. A mirrored raw is refused rather than guessed at: it would
// flip the sky's handedness, and a solver handed a mirrored prior converges to a confident nonsense.
func storedAxes(orientation int, right, up [3]float64) (storedX, storedY [3]float64, ok bool) {
	switch orientation {
	case 0, 1: // stored as displayed: +x is right, +y is down
		return right, neg3(up), true
	case 3: // rotated 180
		return neg3(right), up, true
	case 6: // portrait, rotated 90 clockwise to display: stored +x runs DOWN the picture
		return neg3(up), neg3(right), true
	case 8: // portrait, rotated 90 anticlockwise
		return up, right, true
	}
	return storedX, storedY, false
}
