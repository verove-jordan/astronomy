package skylog

import (
	"math"
	"sort"
)

// The rolled-up record of a night, denormalized onto the session so the logbook list can draw one
// line per session without joining or aggregating.

// Stat is the spread of one metric over the session. N is how many samples actually carried a value,
// so a median computed from two readings out of twelve is not mistaken for a solid one.
type Stat struct {
	Min    float64 `json:"min"`
	Median float64 `json:"median"`
	Max    float64 `json:"max"`
	N      int     `json:"n"`
}

// Summary is what the logbook shows at a glance and what a later stacking decision is made from.
type Summary struct {
	Samples int   `json:"samples"`
	FirstMs int64 `json:"first_ms"`
	LastMs  int64 `json:"last_ms"`

	Cloud        Stat `json:"cloud_pct"`
	CloudLow     Stat `json:"cloud_low"`
	CloudMid     Stat `json:"cloud_mid"`
	CloudHigh    Stat `json:"cloud_high"`
	Seeing       Stat `json:"seeing_arcsec"`
	Transparency Stat `json:"transparency"`
	Humidity     Stat `json:"humidity_pct"`
	DewSpread    Stat `json:"dew_spread_c"`
	Temp         Stat `json:"temp_c"`
	Wind         Stat `json:"wind_kmh"`
	Gust         Stat `json:"gust_kmh"`
	Precip       Stat `json:"precip_pct"`
	AOD          Stat `json:"aod"`
	Verdict      Stat `json:"verdict"`

	// Moon over the session. The maxima are what matter: a Moon that rose at 03:00 ruined the last
	// hour even though its median altitude was negative.
	MoonIllumMax      float64 `json:"moon_illum_max"`
	MoonAltMaxDeg     float64 `json:"moon_alt_max_deg"`
	MoonUp            bool    `json:"moon_up"`
	MoonSepMinDeg     float64 `json:"moon_sep_min_deg"`
	MoonPhaseAngleDeg float64 `json:"moon_phase_angle_deg"` // at the midpoint of the session

	TargetValid      bool    `json:"target_valid"`
	TargetAltMinDeg  float64 `json:"target_alt_min_deg"`
	TargetAltMaxDeg  float64 `json:"target_alt_max_deg"`
	TargetAirmassMin float64 `json:"target_airmass_min"`

	SQM    float64 `json:"sqm"`
	Bortle int     `json:"bortle"`

	DewRiskWorst string  `json:"dew_risk_worst"`
	KpMax        float64 `json:"kp_max"`
	AuroraMax    string  `json:"aurora_max"`

	// How many samples came from each Source. A session whose rows are all "unavailable" has an empty
	// weather record, and the UI must say so instead of drawing a flat zero line.
	SourceCounts map[string]int `json:"source_counts"`
}

// dewRiskRank and auroraRank order the two worded scales so "worst over the night" is well defined.
var dewRiskRank = map[string]int{"low": 1, "moderate": 2, "high": 3}
var auroraRank = map[string]int{"unlikely": 1, "possible": 2, "likely": 3}

// Summarize rolls samples up. It is pure and total: an empty slice yields a zero Summary rather than
// a panic, because a session can end before its first sample is due.
//
// Which zeros count is decided per metric, following weather.Hour's own documentation. Only seeing,
// transparency and AOD are documented there as "0 = the feed did not supply it"; for everything else
// zero is a real reading (0% cloud is a perfect night, 0 °C is a normal winter one) and dropping it
// would quietly bias the record toward warmer, cloudier numbers.
func Summarize(samples []Sample) Summary {
	sum := Summary{Samples: len(samples), SourceCounts: map[string]int{}}
	if len(samples) == 0 {
		return sum
	}
	sum.FirstMs, sum.LastMs = samples[0].AtMs, samples[len(samples)-1].AtMs

	var cloud, cloudLow, cloudMid, cloudHigh, seeing, transp, humid []float64
	var dewSpread, temp, wind, gust, precip, aod, verdict []float64

	// Target altitude is legitimately negative (a target below the horizon at the start of the run), so
	// "nothing seen yet" cannot be encoded as a negative sentinel — it needs its own flag.
	seenTarget := false
	worstDew, bestAurora := 0, 0

	for i, s := range samples {
		sum.SourceCounts[s.Source]++
		withWeather := s.Source != SourceUnavailable

		if withWeather {
			cloud = append(cloud, s.CloudPct)
			cloudLow = append(cloudLow, s.CloudLow)
			cloudMid = append(cloudMid, s.CloudMid)
			cloudHigh = append(cloudHigh, s.CloudHigh)
			humid = append(humid, s.HumidityPct)
			dewSpread = append(dewSpread, s.DewSpreadC)
			temp = append(temp, s.TempC)
			wind = append(wind, s.WindKmh)
			gust = append(gust, s.GustKmh)
			precip = append(precip, s.PrecipPct)
			verdict = append(verdict, s.Verdict)
		}
		// Documented as 0 = unknown even when the hour itself is present.
		if s.SeeingArcsec > 0 {
			seeing = append(seeing, s.SeeingArcsec)
		}
		if s.Transparency > 0 {
			transp = append(transp, s.Transparency)
		}
		if s.AOD > 0 {
			aod = append(aod, s.AOD)
		}

		if s.MoonIllum > sum.MoonIllumMax {
			sum.MoonIllumMax = s.MoonIllum
		}
		if i == 0 || s.MoonAltDeg > sum.MoonAltMaxDeg {
			sum.MoonAltMaxDeg = s.MoonAltDeg
		}
		if s.MoonAltDeg > 0 {
			sum.MoonUp = true
		}
		if s.TargetValid {
			if !seenTarget {
				seenTarget = true
				sum.TargetValid = true
				sum.MoonSepMinDeg = s.MoonSepDeg
				sum.TargetAltMinDeg, sum.TargetAltMaxDeg = s.TargetAltDeg, s.TargetAltDeg
			}
			sum.MoonSepMinDeg = math.Min(sum.MoonSepMinDeg, s.MoonSepDeg)
			sum.TargetAltMinDeg = math.Min(sum.TargetAltMinDeg, s.TargetAltDeg)
			sum.TargetAltMaxDeg = math.Max(sum.TargetAltMaxDeg, s.TargetAltDeg)
			// Airmass 0 means "below the horizon", which is not a best case to minimize toward.
			if s.TargetAirmass > 0 && (sum.TargetAirmassMin == 0 || s.TargetAirmass < sum.TargetAirmassMin) {
				sum.TargetAirmassMin = s.TargetAirmass
			}
		}
		if s.SQM > 0 {
			sum.SQM, sum.Bortle = s.SQM, s.Bortle
		}
		if s.KpMax > sum.KpMax {
			sum.KpMax = s.KpMax
		}
		if r := dewRiskRank[s.DewRisk]; r > worstDew {
			worstDew, sum.DewRiskWorst = r, s.DewRisk
		}
		if r := auroraRank[s.Aurora]; r > bestAurora {
			bestAurora, sum.AuroraMax = r, s.Aurora
		}
	}

	// The phase barely moves over one night, so the midpoint sample represents the whole session.
	sum.MoonPhaseAngleDeg = samples[len(samples)/2].MoonPhaseAngleDeg

	sum.Cloud, sum.CloudLow, sum.CloudMid, sum.CloudHigh = statOf(cloud), statOf(cloudLow), statOf(cloudMid), statOf(cloudHigh)
	sum.Seeing, sum.Transparency, sum.Humidity = statOf(seeing), statOf(transp), statOf(humid)
	sum.DewSpread, sum.Temp, sum.Wind, sum.Gust = statOf(dewSpread), statOf(temp), statOf(wind), statOf(gust)
	sum.Precip, sum.AOD, sum.Verdict = statOf(precip), statOf(aod), statOf(verdict)
	return sum
}

// statOf reduces the collected readings of one metric. The input is copied before sorting so the
// caller's slice ordering (which is chronological) survives.
func statOf(vals []float64) Stat {
	if len(vals) == 0 {
		return Stat{}
	}
	s := make([]float64, len(vals))
	copy(s, vals)
	sort.Float64s(s)

	med := s[len(s)/2]
	if len(s)%2 == 0 {
		med = (s[len(s)/2-1] + s[len(s)/2]) / 2
	}
	return Stat{Min: s[0], Median: med, Max: s[len(s)-1], N: len(s)}
}
