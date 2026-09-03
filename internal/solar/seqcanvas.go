package solar

import (
	"fmt"
	"math"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// seqcanvas.go lays the finished phases out as one picture.
//
// The panels are composited by MAX rather than by feathering or averaging, and that choice is the
// reason the result has no seams at all. Every panel is a disc on black sky; wherever two panels
// overlap, one of them is sky, and the maximum of a value and the sky is that value. Averaging would
// halve the aureole in every overlap and draw a visible lozenge where the square rasters cross;
// feathering would need a mask per panel and would still fade the glow. Max is what addDiscGlow and
// blendProminences already use for the same reason, on the same kind of data.

// SequenceLayout places the panels along a straight line.
type SequenceLayout struct {
	// AngleDeg is the line's rise as it is seen, degrees: positive climbs to the right.
	AngleDeg float64
	// Spacing is the centre-to-centre step in solar DIAMETERS, so it means the same thing whatever
	// radius the panels end up at. Below 1 the discs overlap.
	Spacing float64
	// MaxEdge caps the long edge in pixels. The panels are shrunk to fit rather than the finished
	// canvas being resampled, which would cost a second interpolation on every pixel.
	MaxEdge int
}

// DefaultSequenceLayout is the arrangement of the classic eclipse progression poster.
func DefaultSequenceLayout() SequenceLayout {
	return SequenceLayout{AngleDeg: 22, Spacing: 1.35, MaxEdge: 16000}
}

// SequenceCanvas is the solved geometry of a sequence: how big each panel may be, how big the sheet
// is, and where each panel's centre lands on it.
type SequenceCanvas struct {
	Radius  float64      `json:"radius_px"`
	Side    int          `json:"panel_side_px"`
	Width   int          `json:"width_px"`
	Height  int          `json:"height_px"`
	Centres [][2]float64 `json:"-"`
	Shrunk  bool         `json:"shrunk"`
}

// PlanSequenceCanvas sizes the sheet for n panels of the given solar radius, shrinking the panels if
// the sheet would otherwise run past MaxEdge.
func PlanSequenceCanvas(n int, radius, margin float64, lay SequenceLayout) (SequenceCanvas, error) {
	if n <= 0 {
		return SequenceCanvas{}, fmt.Errorf("plan sequence canvas: no panels")
	}
	if radius <= 0 {
		return SequenceCanvas{}, fmt.Errorf("plan sequence canvas: solar radius %.1f px is not usable", radius)
	}
	if lay.Spacing <= 0 {
		lay.Spacing = DefaultSequenceLayout().Spacing
	}
	c := solveCanvas(n, radius, margin, lay)
	if lay.MaxEdge > 0 {
		if long := maxInt(c.Width, c.Height); long > lay.MaxEdge {
			shrunk := radius * float64(lay.MaxEdge) / float64(long)
			c = solveCanvas(n, shrunk, margin, lay)
			c.Shrunk = true
		}
	}
	return c, nil
}

// solveCanvas is the geometry proper: one step vector, n centres along it, and the bounding box of
// the panel squares those centres carry.
func solveCanvas(n int, radius, margin float64, lay SequenceLayout) SequenceCanvas {
	side := CanonicalSide(radius, margin, 1)
	rad := lay.AngleDeg * math.Pi / 180
	// Rows run downward, so a line that climbs to the right steps in NEGATIVE y.
	stepX := lay.Spacing * 2 * radius * math.Cos(rad)
	stepY := -lay.Spacing * 2 * radius * math.Sin(rad)

	half := float64(n-1) / 2
	spanX, spanY := math.Abs(float64(n-1)*stepX), math.Abs(float64(n-1)*stepY)
	w := int(spanX) + side
	h := int(spanY) + side

	centres := make([][2]float64, n)
	for i := range centres {
		k := float64(i) - half
		centres[i] = [2]float64{float64(w-1)/2 + k*stepX, float64(h-1)/2 + k*stepY}
	}
	return SequenceCanvas{Radius: radius, Side: side, Width: w, Height: h, Centres: centres}
}

// RenderSequence composites the panels onto the planned sheet. Panels must all be Side x Side and
// carry the same channel count; a panel that is not is skipped and named in the notes rather than
// silently shifting everything after it.
func RenderSequence(panels []*fits.Image, c SequenceCanvas) (*fits.Image, []string) {
	var notes []string
	channels := 1
	for _, p := range panels {
		if p != nil && p.C > channels {
			channels = p.C
		}
	}
	out := fits.NewImage(c.Width, c.Height, channels)
	for i, p := range panels {
		if i >= len(c.Centres) {
			break
		}
		if p == nil || p.W != c.Side || p.H != c.Side {
			notes = append(notes, fmt.Sprintf("panel %d is not %dx%d and was left out of the sheet", i+1, c.Side, c.Side))
			continue
		}
		blitMax(out, p, c.Centres[i])
	}
	return out, notes
}

// blitMax copies one panel in, keeping whichever of the two is brighter.
func blitMax(dst, src *fits.Image, centre [2]float64) {
	x0 := int(math.Round(centre[0] - float64(src.W-1)/2))
	y0 := int(math.Round(centre[1] - float64(src.H-1)/2))
	for y := 0; y < src.H; y++ {
		dy := y0 + y
		if dy < 0 || dy >= dst.H {
			continue
		}
		for x := 0; x < src.W; x++ {
			dx := x0 + x
			if dx < 0 || dx >= dst.W {
				continue
			}
			for ch := 0; ch < dst.C; ch++ {
				sc := ch
				if sc >= src.C {
					sc = src.C - 1
				}
				if v := src.Pix[sc][y*src.W+x]; v > dst.Pix[ch][dy*dst.W+dx] {
					dst.Pix[ch][dy*dst.W+dx] = v
				}
			}
		}
	}
}

// maxInt is spelled out rather than using the builtin so this file does not shadow `max` for the
// whole package.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
