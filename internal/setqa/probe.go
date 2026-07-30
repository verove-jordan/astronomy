package setqa

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
	"github.com/verove-jordan/astronomy/internal/inspect"
)

// Tile grid geometry. 16×12 is resolution-independent (~291×293 px tiles on the ASI1600's
// 4656×3520) and fine enough that a border halo spans several tiles.
const (
	tileCols      = 16
	tileRows      = 12
	tileSampleMax = 512 // pixels sampled per tile for the P30 estimate
	borderRing    = 2   // outer tile ring width per side
	centerHalf    = 2   // central block half-size (4×4 tiles)
)

// FrameProbe is the spatial background measurement of one frame (one entry per channel plane).
type FrameProbe struct {
	Path     string
	Channels []ChannelProbe
}

// ChannelProbe measures one channel plane's background shape.
type ChannelProbe struct {
	Channel     string     // "" mono; R|G|B for color frames
	Background  float64    // P30 of the tile backgrounds — the sky level
	NoiseSigma  float64    // 1.4826·MAD of the plane-fit residuals (tile-to-tile scatter)
	GradPct     float64    // fitted-plane corner-to-corner amplitude, % of Background
	GradSigma   float64    // same amplitude in NoiseSigma units
	Border      [4]float64 // signed asymmetric border excess (σ): left, right, top, bottom
	WorstBorder string     // side with the largest positive excess
	BorderSigma float64    // that excess (0 when no side is positive)
	BorderPct   float64    // that excess as % of Background
}

var borderNames = [4]string{"left", "right", "top", "bottom"}

func measureFrame(load func(string) (*fits.Image, error), path string) (FrameProbe, error) {
	im, err := load(path)
	if err != nil {
		return FrameProbe{}, err
	}
	probe := MeasureImage(im)
	probe.Path = path
	return probe, nil
}

// MeasureImage probes every channel plane of an in-memory image (pure — unit-testable).
// Mono frames yield one unnamed channel: their filter identity comes from the SetKey.
func MeasureImage(im *fits.Image) FrameProbe {
	names := []string{""}
	if im.C >= 3 {
		names = []string{"R", "G", "B"}
	}
	probe := FrameProbe{}
	for c := 0; c < min(im.C, 3); c++ {
		probe.Channels = append(probe.Channels, measurePlane(im.Pix[c], im.W, im.H, names[c]))
	}
	return probe
}

func measurePlane(pix []float32, w, h int, channel string) ChannelProbe {
	tiles := tileGrid(pix, w, h)
	bg := math.Max(percentile64(tiles, 30), 1e-9)
	fit := fitPlaneRobust(tiles)
	noise := math.Max(fit.sigma, 1e-8)
	gradAmp := 2 * (math.Abs(fit.ax) + math.Abs(fit.ay))
	cp := ChannelProbe{
		Channel:    channel,
		Background: bg,
		NoiseSigma: noise,
		GradPct:    100 * gradAmp / bg,
		GradSigma:  gradAmp / noise,
	}

	// Border asymmetry on the RAW tile map: per side, the outer ring's median minus the central
	// block's, minus the OPPOSITE side's positive excess — vignetting (all borders darker) and
	// symmetric moon/LP glow cancel out, a one-sided halo scores fully.
	center := centerMedian(tiles)
	var excess [4]float64
	for side := range excess {
		excess[side] = (sideMedian(tiles, side) - center) / noise
	}
	opposite := [4]int{1, 0, 3, 2}
	for side := range excess {
		asym := excess[side] - math.Max(excess[opposite[side]], 0)
		cp.Border[side] = asym
		if asym > cp.BorderSigma {
			cp.BorderSigma = asym
			cp.WorstBorder = borderNames[side]
		}
	}
	cp.BorderPct = 100 * cp.BorderSigma * noise / bg
	return cp
}

// tileGrid returns the row-major tileRows×tileCols map of per-tile P30 backgrounds. P30 of a
// stride subsample rejects stars and most of an object core while tracking the sky level.
func tileGrid(pix []float32, w, h int) []float64 {
	tiles := make([]float64, tileRows*tileCols)
	for r := 0; r < tileRows; r++ {
		y0, y1 := r*h/tileRows, (r+1)*h/tileRows
		for c := 0; c < tileCols; c++ {
			x0, x1 := c*w/tileCols, (c+1)*w/tileCols
			tiles[r*tileCols+c] = tileBackground(pix, w, x0, x1, y0, y1)
		}
	}
	return tiles
}

func tileBackground(pix []float32, w, x0, x1, y0, y1 int) float64 {
	stride := max((x1-x0)*(y1-y0)/tileSampleMax, 1)
	sample := make([]float32, 0, tileSampleMax+8)
	idx := 0
	for y := y0; y < y1; y++ {
		row := y * w
		for x := x0; x < x1; x++ {
			if idx%stride == 0 {
				sample = append(sample, pix[row+x])
			}
			idx++
		}
	}
	return imgops.Percentile(sample, 30)
}

func sideMedian(tiles []float64, side int) float64 {
	var vals []float64
	for r := 0; r < tileRows; r++ {
		for c := 0; c < tileCols; c++ {
			in := false
			switch side {
			case 0:
				in = c < borderRing
			case 1:
				in = c >= tileCols-borderRing
			case 2:
				in = r < borderRing
			case 3:
				in = r >= tileRows-borderRing
			}
			if in {
				vals = append(vals, tiles[r*tileCols+c])
			}
		}
	}
	return median64(vals)
}

func centerMedian(tiles []float64) float64 {
	var vals []float64
	for r := tileRows/2 - centerHalf; r < tileRows/2+centerHalf; r++ {
		for c := tileCols/2 - centerHalf; c < tileCols/2+centerHalf; c++ {
			vals = append(vals, tiles[r*tileCols+c])
		}
	}
	return median64(vals)
}

// sampleFrames picks n evenly-spaced frames in chronological order (DateObsMs, path tiebreak),
// so a mid-session artifact onset is still sampled.
func sampleFrames(set inspect.Set, n int) []*inspect.Frame {
	frames := append([]*inspect.Frame(nil), set.Frames...)
	sort.Slice(frames, func(i, j int) bool {
		if frames[i].DateObsMs != frames[j].DateObsMs {
			return frames[i].DateObsMs < frames[j].DateObsMs
		}
		return frames[i].Path < frames[j].Path
	})
	if n >= len(frames) {
		return frames
	}
	out := make([]*inspect.Frame, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, frames[i*(len(frames)-1)/(n-1)])
	}
	return out
}
