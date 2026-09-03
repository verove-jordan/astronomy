package nightscape

// horizon.go answers one question the sky/foreground split cannot answer from pixels alone: was
// there any ground in front of the camera at all?
//
// The mask in compose.go assumes every frame is a landscape — bright sky above, dark ground below,
// separated by a threshold. Point the same camera at the zenith and that assumption inverts: there
// is no ground, so the threshold cuts through the middle of the sky's own noise, the candidate mask
// shatters into tens of thousands of specks, and the flood fill concludes the frame is 100 percent
// foreground. The composite then shows a single unregistered frame instead of the stack.
//
// A phone records where it was aimed, and the geometry is not ambiguous: a frame whose lower edge
// sits forty degrees above the horizon cannot contain ground, whatever its histogram looks like.

import (
	"math"

	"github.com/verove-jordan/astronomy/internal/pointing"
	"github.com/verove-jordan/astronomy/internal/rawmeta"
)

const (
	// horizonMarginDeg is how far above the horizon the frame's lower edge must sit before the
	// foreground path is skipped. It covers the slop in a phone's reported tilt plus the drop a
	// typical roll puts on the lower corners — a couple of degrees between them.
	//
	// It used to be 10, which was defensive about ground creeping into a frame that geometry said
	// was clear. It no longer has to be: ground that does creep in is rejected by the stack's dark
	// floor, pixel by pixel, instead of by a mask that has to guess where the horizon is. Being
	// wrong towards "sky only" now costs far less than being wrong towards "landscape", which
	// invents a foreground out of the sky's own darker half.
	horizonMarginDeg = 2
	// frame35mmDiagonal is the 35 mm format diagonal, the reference FocalLength35mm is quoted against.
	frame35mmDiagonal = 43.267
)

// skyOnlyBgScale darkens the background target when there is no foreground in shot.
//
// targetBg is where the MEDIAN sky lands. Over a landscape that median takes in bright near-horizon
// sky and sits beside a lit foreground, so the look's 0.05 reads naturally. In a frame that is
// nothing but sky the median IS the dark sky between the stars, and a natural photograph puts that
// close to black. Measured on the zenith panel: 0.05 rendered the sky as mid-grey (sRGB 0.25, since
// the grade works in linear light and the export encodes); a quarter of that reads as a real night
// sky and still holds the band's dust lanes.
//
// It scales rather than replaces so the brightness knob keeps its meaning — asking for brighter
// still gets brighter.
const skyOnlyBgScale = 0.25

// skyOnlyTargetBg applies that scale, falling back to the look's own target when none was asked for.
func skyOnlyTargetBg(brightness, lookTarget float64) float64 {
	if brightness <= 0 {
		brightness = lookTarget
	}
	return brightness * skyOnlyBgScale
}

// horizonInFrame reports whether the camera's field of view reached down to the horizon. known is
// false when the frames carry no pointing or no focal length, in which case callers must keep the
// pixel-based mask — the answer is unavailable, not "no".
func horizonInFrame(frames []string) (inFrame, known bool) {
	if len(frames) == 0 {
		return false, false
	}
	m := rawmeta.Read(frames[len(frames)/2])
	p, ok := pointing.FromMeta(m)
	if !ok {
		return false, false
	}
	half, ok := halfFOVDeg(m)
	if !ok {
		return false, false
	}
	return p.AltDeg-half <= horizonMarginDeg, true
}

// groundPrior returns, for the frame as it is STORED (before EXIF rotation), a mask of the pixels
// that lie below the horizon.
//
// The camera knows where the horizon is. The optical axis sits at altitude alt, the frame spans
// halfFOV either side of it, so in tangent units the horizon lies at -tan(alt)/tan(halfFOV) of the
// half-height below centre — a line, not a guess. Checked against the sea-horizon panel: predicted
// at 0.70 of the frame width, and that is exactly where the town's lights sit.
//
// This matters because the pixels cannot answer it. Thresholding calls the darker half of the image
// "ground", and over a Milky Way the darker half is sky; the sky is not even one connected region
// above the threshold, since the dust lanes cut it apart, so no amount of flood-filling recovers it.
//
// ok is false when the pointing, the focal length or the orientation is unavailable, or when the
// horizon falls outside the frame entirely — callers then keep the luminance-only mask.
func groundPrior(frames []string, w, h int) ([]bool, bool) {
	if len(frames) == 0 {
		return nil, false
	}
	m := rawmeta.Read(frames[len(frames)/2])
	p, ok := pointing.FromMeta(m)
	if !ok {
		return nil, false
	}
	axis, skyLow, ok := displayUpAxis(m.Orientation)
	if !ok {
		return nil, false
	}
	half, ok := halfFOVAlong(m, axis)
	if !ok {
		return nil, false
	}
	// Distance from the frame centre to the horizon, as a fraction of the half-extent, measured
	// along the display's up direction. Beyond the frame means no horizon in shot.
	frac := math.Tan(p.AltDeg*math.Pi/180) / math.Tan(half*math.Pi/180)
	if math.Abs(frac) >= 1 {
		return nil, false
	}
	if !skyLow {
		frac = -frac
	}

	extent := w
	if axis == axisY {
		extent = h
	}
	cut := int(math.Round((0.5 + frac/2) * float64(extent)))

	ground := make([]bool, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := x
			if axis == axisY {
				v = y
			}
			// skyLow means the sky sits at low coordinates along this axis.
			if (skyLow && v > cut) || (!skyLow && v < cut) {
				ground[y*w+x] = true
			}
		}
	}
	return ground, true
}

const (
	axisX = 0
	axisY = 1
)

// displayUpAxis maps an EXIF orientation onto the STORED axis the display's "up" runs along, and
// whether the sky therefore sits at low coordinates on that axis. Only the four unmirrored codes
// are handled; a mirrored raw returns false rather than a coin-flip.
func displayUpAxis(orientation int) (axis int, skyLow bool, ok bool) {
	switch orientation {
	case 1: // as stored: up is decreasing y
		return axisY, true, true
	case 3: // 180: up is increasing y
		return axisY, false, true
	case 6: // portrait, rotated 90 CW to display: the 0th COLUMN is the visual top
		return axisX, true, true
	case 8: // portrait, rotated 90 CCW: the last column is the visual top
		return axisX, false, true
	}
	return 0, false, false
}

// halfFOVAlong returns half the field of view along one STORED axis.
func halfFOVAlong(m rawmeta.Meta, axis int) (float64, bool) {
	long, ok := halfFOVDeg(m)
	if !ok {
		return 0, false
	}
	if (axis == axisX) == (m.Width >= m.Height) {
		return long, true // this axis is the sensor's long side
	}
	return halfFOVShortDeg(m)
}

// halfFOVShortDeg is the counterpart of halfFOVDeg for the frame's short axis.
func halfFOVShortDeg(m rawmeta.Meta) (float64, bool) {
	if m.FocalLength35mm <= 0 || m.Width <= 0 || m.Height <= 0 {
		return 0, false
	}
	long, short := float64(m.Width), float64(m.Height)
	if short > long {
		long, short = short, long
	}
	shortSide := frame35mmDiagonal * short / math.Hypot(long, short)
	return math.Atan(shortSide/(2*float64(m.FocalLength35mm))) * 180 / math.Pi, true
}

// halfFOVDeg returns half the field of view along the frame's LONG axis. The long axis is used
// deliberately: it is the larger angle, so the frame's lower edge is placed as low as it could
// possibly be, and the answer errs towards "the horizon might be in shot" — which keeps the
// foreground mask switched on. Being wrong in that direction costs a mask that does nothing; being
// wrong the other way discards the ground.
func halfFOVDeg(m rawmeta.Meta) (float64, bool) {
	if m.FocalLength35mm <= 0 || m.Width <= 0 || m.Height <= 0 {
		return 0, false
	}
	long, short := float64(m.Width), float64(m.Height)
	if short > long {
		long, short = short, long
	}
	// Scale the 35 mm diagonal down to this sensor's aspect ratio to recover the long side, since
	// FocalLength35mm is quoted against the diagonal, not against either edge.
	longSide := frame35mmDiagonal * long / math.Hypot(long, short)
	return math.Atan(longSide/(2*float64(m.FocalLength35mm))) * 180 / math.Pi, true
}
