// Star-quality analysis for the gated post-finish repair (starfix.go). It compares what the stars' TRUE
// colours are — measured on the pre-stretch linear RGB base, before the stretch can burn them — against
// how the rendered final shows them (burnt to white / flattened to one hue / uniformly warm). The
// comparison is aggregate, not per-star: the final is edge-cropped relative to the base, so matching
// individual positions would be fragile; colourfulness statistics are crop-agnostic.
package pipeline

import (
	"fmt"
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

const (
	starReportMinStars  = 30   // need a real population before judging colour variety
	starTrueSatFloor    = 0.10 // a star with ≥ this linear saturation genuinely HAD colour worth keeping
	starTrueSpreadFloor = 0.03 // ...and the field's true colours were varied (not all one hue)
	starWarmFracMax     = 0.60 // fraction of warm cores beyond which the field reads "all orange"
	starMaxSample       = 400  // cap detected stars measured for the report
)

// starReport summarises star-core quality: the TRUE colours from the linear RGB base versus the rendered
// final's measured metrics.
type starReport struct {
	Detected      int
	TrueSat       float64 // median linear saturation of detected stars (how colourful they should be)
	TrueSpread    float64 // stddev of their normalised red−blue balance (how varied the true colours are)
	FinalBurnt    float64 // worst per-channel WhiteClip in the rendered final (fraction of blown pixels)
	FinalWarm     float64 // StarWarmFrac of the final
	FinalSpread   float64 // StarColorSpread of the final
	FinalSatFrac  float64 // StarSatFrac of the final (fraction of bright cores that are over-saturated colour discs)
	FinalBgChroma float64 // BgChroma of the final (background chroma mottle)
}

// analyzeStars measures star quality from the linear RGB base (rgbBasePath, true colour before the
// stretch) and the rendered final's already-measured metrics m. An unreadable base returns an error the
// caller treats as "cannot judge" (skip the repair).
func analyzeStars(rgbBasePath string, m finishMetrics) (starReport, error) {
	im, err := fits.ReadImage(rgbBasePath)
	if err != nil {
		return starReport{}, fmt.Errorf("analyze stars: read base: %w", err)
	}
	stars := postprocess.StarColors(im, starMaxSample)
	sat, spread := trueStarColorStats(stars)
	return starReport{
		Detected:      len(stars),
		TrueSat:       sat,
		TrueSpread:    spread,
		FinalBurnt:    maxWhiteClip(m),
		FinalWarm:     m.StarWarmFrac,
		FinalSpread:   m.StarColorSpread,
		FinalSatFrac:  m.StarSatFrac,
		FinalBgChroma: m.BgChroma,
	}, nil
}

// needsFix reports whether the finish has fixable burnt / flattened / uniformly-warm star cores.
func (r starReport) needsFix() bool {
	if r.Detected < starReportMinStars {
		return false
	}
	if r.FinalBurnt > whiteClipMax {
		return true // cores blown to white
	}
	if r.TrueSat > starTrueSatFloor && r.FinalSpread > 0 && r.FinalSpread < starColorSpreadMin {
		return true // stars had colour but the final flattened them to one hue / grey
	}
	if r.FinalWarm > starWarmFracMax && r.TrueSpread > starTrueSpreadFloor {
		return true // uniformly warm cores despite varied true colours (the "all orange" look)
	}
	if r.FinalSatFrac > starSatFracMax && r.TrueSat < 0.5*r.FinalSatFrac {
		return true // bright cores rendered as over-saturated colour discs the linear stars don't warrant
	}
	if r.FinalBgChroma > bgChromaMax {
		return true // purple-green background chroma mottle
	}
	return false
}

// trueStarColorStats returns the median colour saturation and the spread (stddev) of the normalised
// red−blue balance across detected stars.
func trueStarColorStats(stars []postprocess.StarColor) (medSat, spread float64) {
	if len(stars) == 0 {
		return 0, 0
	}
	sats := make([]float64, 0, len(stars))
	bals := make([]float64, 0, len(stars))
	for _, s := range stars {
		sats = append(sats, s.Sat())
		if sum := s.R + s.G + s.B; sum > 0 {
			bals = append(bals, (s.R-s.B)/sum)
		}
	}
	return medianOf(sats), stddevOf(bals)
}

// maxWhiteClip is the worst per-channel blown-highlight fraction in the finish.
func maxWhiteClip(m finishMetrics) float64 {
	worst := 0.0
	for _, wc := range m.WhiteClip {
		if wc > worst {
			worst = wc
		}
	}
	return worst
}

// isStarFixMode reports whether a mode uses the LRGB GIMP composite finish the repair re-enters.
func isStarFixMode(m mode.Mode) bool {
	switch m {
	case mode.Deepsky, mode.Nebula, mode.Livestack:
		return true
	default:
		return false
	}
}

func medianOf(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	n := len(c)
	if n%2 == 1 {
		return c[n/2]
	}
	return (c[n/2-1] + c[n/2]) / 2
}

func stddevOf(v []float64) float64 {
	if len(v) < 2 {
		return 0
	}
	var mean float64
	for _, x := range v {
		mean += x
	}
	mean /= float64(len(v))
	var varr float64
	for _, x := range v {
		d := x - mean
		varr += d * d
	}
	return math.Sqrt(varr / float64(len(v)))
}
