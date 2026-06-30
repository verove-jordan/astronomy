package weather

import "math"

// seeingMidpoints maps a 7Timer seeing index (1..8) to a representative arcsecond value.
var seeingMidpoints = []float64{0.4, 0.6, 0.85, 1.1, 1.35, 1.75, 2.25, 2.75}

func seeingArcsec(idx int) float64 {
	if idx < 1 || idx > len(seeingMidpoints) {
		return 0
	}
	return seeingMidpoints[idx-1]
}

// transparencyScore maps a 7Timer transparency index (1 = best … 8 = worst) to a 0..1 score (1 = pristine).
func transparencyScore(idx int) float64 {
	if idx < 1 || idx > 8 {
		return 0
	}
	return clampf(1.0-float64(idx-1)/8.0, 0.1, 1)
}

// transparencyFromAOD derives a 0..1 transparency score from aerosol optical depth (550 nm), used when
// 7Timer is unavailable: AOD ≲ 0.05 pristine, ≳ 0.6 poor (wildfire smoke / Saharan dust).
func transparencyFromAOD(aod float64) float64 {
	if aod <= 0 {
		return 0
	}
	return clampf(1.0-(aod-0.05)/0.6, 0.1, 1)
}

// dewRisk classifies the temperature − dew-point spread (°C) into optics-fogging risk.
func dewRisk(spreadC float64) string {
	switch {
	case spreadC >= 5:
		return "low"
	case spreadC >= 3:
		return "moderate"
	default:
		return "high"
	}
}

// hourVerdict scores one hour's observability 0..100. Cloud cover dominates (you cannot image through
// cloud); transparency, seeing and humidity modulate the rest. Unknown metrics (zero) are skipped so a
// missing feed never invents a penalty.
func hourVerdict(h Hour) float64 {
	v := 1 - clampf(h.CloudPct/100, 0, 1)
	if h.Transparency > 0 {
		v *= 0.5 + 0.5*h.Transparency
	}
	if h.SeeingArcsec > 0 {
		v *= clampf(1-(h.SeeingArcsec-0.6)/3.0, 0.3, 1) // 0.6″ excellent … 3.6″+ poor
	}
	if h.HumidityPct > 0 {
		v *= clampf(1-(h.HumidityPct-80)/40, 0.4, 1) // RH > 80% starts to bite
	}
	if h.DewRisk == "high" {
		v *= 0.85
	}
	return round1(clampf(v, 0, 1) * 100)
}

// BestWindow finds the contiguous run of hours within [startMs,endMs] (the dark window) whose verdict
// stays "good", maximizing total verdict — the slot to actually go out. Falls back to the single best
// hour when nothing is clearly good, and returns nil when no hours fall in the window.
func BestWindow(hours []Hour, startMs, endMs int64) *Window {
	const goodVerdict = 60.0
	const maxGapMs = 2 * 3600 * 1000

	var rng []Hour
	for _, h := range hours {
		if h.TMs >= startMs && h.TMs <= endMs {
			rng = append(rng, h)
		}
	}
	if len(rng) == 0 {
		return nil
	}

	bestI, bestJ, bestSum := -1, -1, 0.0
	for i := 0; i < len(rng); {
		if rng[i].Verdict < goodVerdict {
			i++
			continue
		}
		j, sum := i, 0.0
		for j < len(rng) && rng[j].Verdict >= goodVerdict && (j == i || rng[j].TMs-rng[j-1].TMs <= maxGapMs) {
			sum += rng[j].Verdict
			j++
		}
		if sum > bestSum {
			bestSum, bestI, bestJ = sum, i, j-1
		}
		i = j
	}

	if bestI < 0 {
		best := 0
		for k := range rng {
			if rng[k].Verdict > rng[best].Verdict {
				best = k
			}
		}
		return &Window{StartMs: rng[best].TMs, EndMs: rng[best].TMs, Verdict: rng[best].Verdict}
	}
	n := float64(bestJ - bestI + 1)
	return &Window{StartMs: rng[bestI].TMs, EndMs: rng[bestJ].TMs, Verdict: round1(bestSum / n)}
}

// auroraChance is a rough aurora-visibility hint from the forecast Kp and the site's latitude: the
// auroral oval reaches lower latitudes as Kp rises.
func auroraChance(kp, lat float64) string {
	threshold := 66.0 - 2.0*kp // |lat| above which aurora becomes plausible
	switch {
	case math.Abs(lat) >= threshold+3:
		return "likely"
	case math.Abs(lat) >= threshold:
		return "possible"
	default:
		return "unlikely"
	}
}

func clampf(x, lo, hi float64) float64 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }
