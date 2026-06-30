package lightpollution

import (
	"math"
	"net/url"
	"strconv"
	"strings"
)

// Sky-brightness units used throughout this package:
//   - SQM  : zenith night-sky brightness in mag/arcsec² (HIGHER = darker). ~22.0 is pristine.
//   - Bortle: the 1 (pristine) … 9 (inner-city) visual classification, derived from SQM.
// A calibrated provider should return SQM (or Bortle) directly; the radiance/luminance helpers below
// are coarse fallbacks for providers/atlases that expose a raw raster value instead.

// pristineSQM is the darkest natural sky brightness; values are capped here so noise can't exceed it.
const pristineSQM = 22.0

// sqmToBortle maps a sky-brightness (mag/arcsec²) to the Bortle class (1 darkest … 9 brightest), using
// the widely-used SQM thresholds.
func sqmToBortle(sqm float64) int {
	switch {
	case sqm >= 21.99:
		return 1
	case sqm >= 21.89:
		return 2
	case sqm >= 21.69:
		return 3
	case sqm >= 21.25:
		return 4
	case sqm >= 20.49:
		return 5
	case sqm >= 19.50:
		return 6
	case sqm >= 18.94:
		return 7
	case sqm >= 18.38:
		return 8
	default:
		return 9
	}
}

// bortleToSQM returns a representative SQM for a Bortle class — the inverse used when a provider
// reports Bortle rather than a brightness.
func bortleToSQM(b int) float64 {
	switch {
	case b <= 1:
		return 22.0
	case b == 2:
		return 21.94
	case b == 3:
		return 21.79
	case b == 4:
		return 21.47
	case b == 5:
		return 20.87
	case b == 6:
		return 20.00
	case b == 7:
		return 19.22
	case b == 8:
		return 18.66
	default:
		return 18.00
	}
}

// radianceToSQM is a COARSE empirical map from VIIRS upward radiance (nW/cm²/sr) to zenith sky
// brightness (mag/arcsec²). A calibrated API should return SQM directly; this only handles bare-number
// or radiance-unit responses. It is anchored near 22.0 over pristine skies and falls to ~16.5 over a
// bright city, monotonically.
func radianceToSQM(r float64) float64 {
	if r < 0.02 {
		r = 0.02
	}
	return clampf(20.7-1.8*math.Log10(r), 16.5, pristineSQM)
}

// luminanceToSQM converts a sky luminance in mcd/m² to mag/arcsec²: S = -2.5·log10(L_cd / 1.08e5).
func luminanceToSQM(mcd float64) float64 {
	cd := mcd / 1000.0
	if cd <= 0 {
		return pristineSQM
	}
	return clampf(-2.5*math.Log10(cd/108000.0), 16.0, pristineSQM)
}

// valueToSQM converts a raw atlas/raster value of the named unit to SQM.
func valueToSQM(v float64, unit string) float64 {
	switch strings.ToLower(unit) {
	case "", "sqm", "mag", "magarcsec2":
		return v
	case "bortle":
		return bortleToSQM(int(math.Round(v)))
	case "mcd", "mcd_m2", "luminance":
		return luminanceToSQM(v)
	default: // "radiance", "viirs", anything else
		return radianceToSQM(v)
	}
}

// expandPointURL fills a point-query URL template ({lat} {lon} {key}).
func expandPointURL(tmpl string, lat, lon float64, key string) string {
	return strings.NewReplacer(
		"{lat}", strconv.FormatFloat(lat, 'f', 6, 64),
		"{lon}", strconv.FormatFloat(lon, 'f', 6, 64),
		"{key}", url.QueryEscape(key),
	).Replace(tmpl)
}

// expandTileURL fills a tile URL template ({z} {x} {y} {key}).
func expandTileURL(tmpl string, z, x, y int, key string) string {
	return strings.NewReplacer(
		"{z}", strconv.Itoa(z),
		"{x}", strconv.Itoa(x),
		"{y}", strconv.Itoa(y),
		"{key}", url.QueryEscape(key),
	).Replace(tmpl)
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }

func clampf(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}
