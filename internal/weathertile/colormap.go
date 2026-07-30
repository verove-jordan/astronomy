// Package weathertile server-renders the animated weather overlay (cloud/humidity/rain forecast) into
// 256×256 PNG map tiles from the internal/weather cube, so the browser composites plain tiles instead of
// running a per-pixel bicubic in JS. The sampling + colour maps are a faithful port of the retired client
// renderer (frontend/src/composables/useWeatherGridLayer.ts + utils/weather.ts), so a tile looks identical
// to the old overlay.
package weathertile

import "math"

// rgba is a straight (non-premultiplied) colour: channels 0..255, alpha 0..1 — matching the browser
// ImageData the client produced.
type rgba struct {
	r, g, b uint8
	a       float64
}

func clamp01(x float64) float64 {
	if x < 0 {
		return 0
	}
	if x > 1 {
		return 1
	}
	return x
}

func lerpU8(a, b uint8, t float64) uint8 {
	return uint8(math.Round(float64(a) + (float64(b)-float64(a))*t))
}

// rampStop is one control point of a piecewise-linear colour ramp keyed on the metric value (%).
type rampStop struct {
	at      float64
	r, g, b uint8
	a       float64
}

// rampAt linearly interpolates a ramp at value v (clamped to the stop range) — the Go port of
// utils/weather.ts `rampRGBA`.
func rampAt(stops []rampStop, v float64) rgba {
	if v <= stops[0].at {
		s := stops[0]
		return rgba{s.r, s.g, s.b, s.a}
	}
	last := stops[len(stops)-1]
	if v >= last.at {
		return rgba{last.r, last.g, last.b, last.a}
	}
	for i := 1; i < len(stops); i++ {
		if v <= stops[i].at {
			a, b := stops[i-1], stops[i]
			t := (v - a.at) / (b.at - a.at)
			return rgba{lerpU8(a.r, b.r, t), lerpU8(a.g, b.g, t), lerpU8(a.b, b.b, t), a.a + (b.a-a.a)*t}
		}
	}
	return rgba{last.r, last.g, last.b, last.a}
}

// band is one cloud altitude layer: a flat colour with a perceptual (gamma) alpha curve on cover % — the
// Go port of utils/weather.ts `bandColor`.
type band struct {
	metric   string
	r, g, b  uint8
	maxAlpha float64
	gamma    float64
}

func (bd band) at(pct float64) rgba {
	return rgba{bd.r, bd.g, bd.b, bd.maxAlpha * math.Pow(clamp01(pct/100), bd.gamma)}
}

// cloudBands are composited bottom→top (high cirrus veil → mid deck → dense low blanket), matching
// utils/weather.ts so the low deck covers the thin cirrus.
var cloudBands = []band{
	{"clouds_high", 190, 215, 235, 0.45, 1.15},
	{"clouds_mid", 205, 215, 228, 0.70, 1.10},
	{"clouds_low", 236, 240, 246, 0.95, 1.05},
}

// cloudsTotal is the single-cover fallback (cube without bands): luminance rises 210→250 with cover.
func cloudsTotal(pct float64) rgba {
	t := math.Pow(clamp01(pct/100), 1.1)
	lum := uint8(math.Round(210 + 40*t))
	return rgba{lum, lum, lum, 0.95 * t}
}

var humidityRamp = []rampStop{
	{50, 80, 190, 120, 0.25}, {70, 150, 205, 80, 0.42}, {85, 240, 195, 60, 0.55},
	{93, 240, 130, 55, 0.64}, {100, 232, 70, 70, 0.72},
}

var precipRamp = []rampStop{
	{0, 60, 170, 230, 0}, {12, 60, 170, 230, 0.35}, {40, 45, 115, 230, 0.62},
	{70, 80, 70, 220, 0.8}, {100, 125, 45, 200, 0.9},
}

// dewSpreadRamp keys on temperature−dew-point (°C), INVERSE of the % ramps: a small spread means
// saturated air (fog forming, dew on the optics) → strong teal; ≥8 °C is dry → fully transparent.
var dewSpreadRamp = []rampStop{
	{0, 45, 190, 200, 0.80}, {2, 70, 185, 205, 0.62}, {4, 120, 190, 215, 0.38},
	{6, 170, 200, 225, 0.18}, {8, 200, 215, 235, 0},
}

// singleRamp returns the per-value colour function for a non-band metric ("" for an unknown metric).
// The standalone altitude-band metrics reuse the cloudBands colours so a band overlay matches its
// contribution inside the composite "clouds" render.
func singleRamp(metric string) func(float64) rgba {
	switch metric {
	case "humidity":
		return func(v float64) rgba { return rampAt(humidityRamp, v) }
	case "precip":
		return func(v float64) rgba { return rampAt(precipRamp, v) }
	case "dewspread":
		return func(v float64) rgba { return rampAt(dewSpreadRamp, v) }
	case "clouds":
		return cloudsTotal
	case "clouds_low", "clouds_mid", "clouds_high":
		for _, bd := range cloudBands {
			if bd.metric == metric {
				return bd.at
			}
		}
		return nil
	default:
		return nil
	}
}
