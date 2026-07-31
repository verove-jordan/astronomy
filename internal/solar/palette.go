package solar

import (
	"sort"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// palette.go colours a finished mono solar image.
//
// There is no "true" colour here to recover: an Hα capture is monochromatic by construction — the
// etalon passes 0.6 Å around 656.3 nm and nothing else — so any colour is a chosen rendering of one
// channel. These are the renderings the solar community actually uses, so an image made here looks
// like what people expect a solar image to look like.

// Palette names.
const (
	PaletteGold     = "gold"     // the classic Hα orange-gold
	PaletteNeutral  = "neutral"  // white light: neutral to faintly warm
	PaletteMono     = "mono"     // untouched greyscale
	PaletteInverted = "inverted" // inverted greyscale, which shows filaments best
)

// Palettes lists the available renderings, in display order.
func Palettes() []string { return []string{PaletteGold, PaletteNeutral, PaletteMono, PaletteInverted} }

// IsPalette reports whether name is a known palette.
func IsPalette(name string) bool {
	i := sort.SearchStrings([]string{PaletteGold, PaletteInverted, PaletteMono, PaletteNeutral}, name)
	all := []string{PaletteGold, PaletteInverted, PaletteMono, PaletteNeutral}
	return i < len(all) && all[i] == name
}

// colourStop is one control point of a palette ramp.
type colourStop struct{ t, r, g, b float64 }

// paletteRamps are the control points each palette interpolates through.
var paletteRamps = map[string][]colourStop{
	// Gold runs deep red through orange to a barely-warm white. The highlights stop short of pure
	// white so plage and flares keep their tint instead of clipping to a flat blank.
	PaletteGold: {
		{0.00, 0.00, 0.00, 0.00},
		{0.25, 0.42, 0.06, 0.01},
		{0.55, 0.85, 0.34, 0.04},
		{0.82, 1.00, 0.72, 0.28},
		{1.00, 1.00, 0.96, 0.80},
	},
	// White light is left close to neutral with a slight warmth, which is how the photosphere reads
	// through a solar film.
	PaletteNeutral: {
		{0.00, 0.00, 0.00, 0.00},
		{0.50, 0.52, 0.50, 0.46},
		{1.00, 1.00, 0.99, 0.94},
	},
}

// applyPalette maps a finished 0..1 mono plane onto RGB.
func applyPalette(p []float32, w, h int, o FinishOptions) *fits.Image {
	name := strings.ToLower(strings.TrimSpace(o.Palette))
	out := fits.NewImage(w, h, 3)
	switch name {
	case PaletteMono, "":
		for c := 0; c < 3; c++ {
			copy(out.Pix[c], p)
		}
		return out
	case PaletteInverted:
		for i, v := range p {
			g := 1 - v
			out.Pix[0][i], out.Pix[1][i], out.Pix[2][i] = g, g, g
		}
		return out
	}
	ramp, ok := paletteRamps[name]
	if !ok {
		ramp = paletteRamps[PaletteGold]
	}
	sat := o.Saturation
	if sat <= 0 {
		sat = 1
	}
	for i, v := range p {
		r, g, b := sampleRamp(ramp, float64(v))
		if sat != 1 {
			// Pull towards or away from the pixel's own luminance, so saturation never changes how
			// bright the image is — only how much colour it carries.
			lum := 0.299*r + 0.587*g + 0.114*b
			r, g, b = lum+(r-lum)*sat, lum+(g-lum)*sat, lum+(b-lum)*sat
		}
		out.Pix[0][i] = float32(clampF(r, 0, 1))
		out.Pix[1][i] = float32(clampF(g, 0, 1))
		out.Pix[2][i] = float32(clampF(b, 0, 1))
	}
	return out
}

// sampleRamp linearly interpolates a ramp at t.
func sampleRamp(ramp []colourStop, t float64) (r, g, b float64) {
	t = clampF(t, 0, 1)
	for i := 1; i < len(ramp); i++ {
		if t <= ramp[i].t {
			a, c := ramp[i-1], ramp[i]
			span := c.t - a.t
			if span <= 1e-9 {
				return c.r, c.g, c.b
			}
			f := (t - a.t) / span
			return a.r + f*(c.r-a.r), a.g + f*(c.g-a.g), a.b + f*(c.b-a.b)
		}
	}
	last := ramp[len(ramp)-1]
	return last.r, last.g, last.b
}

// PaletteFor picks the default palette for a band.
func PaletteFor(b Band) string {
	if b == BandWhiteLight {
		return PaletteNeutral
	}
	return PaletteGold
}
