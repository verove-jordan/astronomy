package weather

import "math"

// Flags a NightOutlook can carry. They are locale-neutral keys: the frontend translates them, the way
// the events calendar translates its score-factor keys.
const (
	FlagFogRisk        = "fog_risk"
	FlagFrost          = "frost"
	FlagAboveInversion = "above_inversion"
	FlagBeyondHorizon  = "beyond_horizon"
)

const (
	// lowCloudExtraPenalty is how much extra a night is docked for low cloud on top of the flat total
	// the hourly verdict already applies. Stratus is a hard stop; cirrus at the same coverage only
	// costs transparency, so the two must not weigh the same when ranking sites.
	lowCloudExtraPenalty = 0.35
	// deckClearanceM is how far above the estimated deck top a site must stand before we credit it with
	// being in the clear. Boundary-layer depth is a model diagnostic, not a cloud-top measurement, so
	// the margin absorbs the error rather than handing out optimistic bonuses.
	deckClearanceM = 150
	// deckCloudLowPct is the mean low-cloud cover that makes a stratus deck plausible in the first place.
	deckCloudLowPct = 60
	// frostTempC is the minimum temperature at which frost on the optics becomes likely.
	frostTempC = 0.5
)

// Fog forms on a still, saturated night when the air cools to its dew point near the ground.
const (
	fogSpreadC     = 1.5
	fogWindKmh     = 8
	fogHumidityPct = 95
)

// NightOutlook aggregates an hourly forecast over one night into a single verdict, so a ranking can
// compare sites on "how good is this whole night here" rather than on one instant — which is what a
// forecast timeline can honestly support.
type NightOutlook struct {
	StartMs      int64    `json:"start_ms"`
	EndMs        int64    `json:"end_ms"`
	SampleHours  int      `json:"sample_hours"` // forecast hours inside the night; 0 = no data
	Score        float64  `json:"score"`        // 0..100, moonlight-weighted
	CloudPct     float64  `json:"cloud_pct"`
	CloudLowPct  float64  `json:"cloud_low_pct"`
	CloudHighPct float64  `json:"cloud_high_pct"`
	ClearHours   float64  `json:"clear_hours"`
	Best         *Window  `json:"best,omitempty"`
	SeeingArcsec float64  `json:"seeing_arcsec"` // 0 = unknown
	SeeingSource string   `json:"seeing_source,omitempty"`
	Transparency float64  `json:"transparency"` // 0..1; 0 = unknown
	DewRisk      string   `json:"dew_risk"`
	MinTempC     float64  `json:"min_temp_c"`
	WindKmh      float64  `json:"wind_kmh"`             // worst hourly wind in the window
	ElevationM   float64  `json:"elevation_m"`          // terrain height the forecast was computed for
	DeckTopM     float64  `json:"deck_top_m,omitempty"` // deck top this outlook was judged against (0 = undecidable)
	Flags        []string `json:"flags,omitempty"`
}

// Known reports whether the outlook carries real forecast data. A site whose night is past the model's
// horizon, or whose fetch failed, must fall back to a weather-free ranking rather than score zero.
func (o NightOutlook) Known() bool { return o.SampleHours > 0 }

// NightInputs parameterizes one night aggregation.
type NightInputs struct {
	StartMs, EndMs int64
	// Moon returns the moonlight factor (0..1, 1 = no interference) for an hour, so hours the Moon
	// spoils count for less. nil weights every hour equally.
	Moon func(tMs int64) float64
	// SiteElevationM is the spot's real elevation and DeckTopM the estimated top of the low-cloud deck
	// over the surrounding lowland (see DeckTop). Together they decide whether this site stands above
	// the cloud rather than under it. 0 means unknown, and unknown never earns the bonus.
	SiteElevationM float64
	DeckTopM       float64
}

// DeckTop estimates the top of a low-cloud deck, in metres above sea level, from the elevation of the
// surrounding lowland and the depth of the boundary layer that caps the deck. Returns 0 when too few
// hours report a boundary-layer depth — the caller must then treat the inversion case as undecidable.
//
// This is the piece that lets a 1200 m spot stop being punished for the stratus sitting on the plain
// below it, which is the single most common reason a mountain site beats a nearer one in winter.
func DeckTop(lowlandFloorM float64, hours []Hour) float64 {
	if lowlandFloorM <= 0 || len(hours) == 0 {
		return 0
	}
	var sum float64
	var known int
	for _, h := range hours {
		if h.BLHeightM > 0 {
			sum += h.BLHeightM
			known++
		}
	}
	if known*2 < len(hours) { // mostly unknown → the mean says nothing
		return 0
	}
	return lowlandFloorM + sum/float64(known)
}

// ScoreNight aggregates the hours falling inside [StartMs,EndMs] into one outlook.
func ScoreNight(hours []Hour, in NightInputs) NightOutlook {
	out := NightOutlook{StartMs: in.StartMs, EndMs: in.EndMs}

	rng := hoursInRange(hours, in.StartMs, in.EndMs)
	if len(rng) == 0 {
		out.Flags = []string{FlagBeyondHorizon}
		return out
	}

	aboveDeck := aboveCloudDeck(rng, in)
	adj := make([]Hour, len(rng))
	for i, h := range rng {
		adj[i] = nightAdjust(h, aboveDeck)
	}

	out.SampleHours = len(adj)
	out.Score = weightedVerdict(adj, in.Moon)
	out.Best = BestWindow(adj, in.StartMs, in.EndMs)
	summarize(&out, adj)

	if aboveDeck {
		out.Flags = append(out.Flags, FlagAboveInversion)
	}
	if fogRisk(adj) {
		out.Flags = append(out.Flags, FlagFogRisk)
	}
	if out.MinTempC <= frostTempC {
		out.Flags = append(out.Flags, FlagFrost)
	}
	return out
}

// nightAdjust recomputes one hour's verdict for site ranking: low cloud counts for more than the flat
// total does, and a site standing above the deck does not pay for the cloud below it.
func nightAdjust(h Hour, aboveDeck bool) Hour {
	if aboveDeck {
		h.CloudPct = math.Max(h.CloudMid, h.CloudHigh)
		h.CloudLow = 0
	}
	v := hourVerdict(h) / 100
	v *= 1 - lowCloudExtraPenalty*clampf(h.CloudLow/100, 0, 1)
	h.Verdict = round1(clampf(v, 0, 1) * 100)
	return h
}

// aboveCloudDeck reports whether the site stands above the low-cloud deck — the autumn/winter
// inversion where the plain lies under stratus while a summit is in the clear. It is deliberately
// conservative: without elevation or a deck-top estimate it says no rather than inventing a bonus,
// and it requires an actual deck to be forecast before crediting the site with rising above one.
func aboveCloudDeck(hours []Hour, in NightInputs) bool {
	if in.SiteElevationM <= 0 || in.DeckTopM <= 0 {
		return false
	}
	if in.SiteElevationM < in.DeckTopM+deckClearanceM {
		return false
	}
	var sumLow float64
	for _, h := range hours {
		sumLow += h.CloudLow
	}
	return sumLow/float64(len(hours)) >= deckCloudLowPct
}

// weightedVerdict is the moonlight-weighted mean of the hourly verdicts.
func weightedVerdict(hours []Hour, moon func(int64) float64) float64 {
	var sum, weight float64
	for _, h := range hours {
		w := 1.0
		if moon != nil {
			w = clampf(moon(h.TMs), 0, 1)
		}
		sum += w * h.Verdict
		weight += w
	}
	if weight <= 0 { // Moon up and full all night — fall back to an unweighted mean
		return meanVerdict(hours)
	}
	return round1(sum / weight)
}

func meanVerdict(hours []Hour) float64 {
	var sum float64
	for _, h := range hours {
		sum += h.Verdict
	}
	return round1(sum / float64(len(hours)))
}

// summarize fills the descriptive fields: means for the cloud layers and transparency, worst case for
// wind and dew, and the median of the known seeing samples.
func summarize(out *NightOutlook, hours []Hour) {
	var cloud, low, high float64
	var transp, transpN float64
	var seeing []float64
	minTemp := math.Inf(1)
	worstDew := "low"

	for _, h := range hours {
		cloud += h.CloudPct
		low += h.CloudLow
		high += h.CloudHigh
		if h.Transparency > 0 {
			transp += h.Transparency
			transpN++
		}
		if h.SeeingArcsec > 0 {
			seeing = append(seeing, h.SeeingArcsec)
			out.SeeingSource = h.SeeingSource
		}
		if h.TempC < minTemp {
			minTemp = h.TempC
		}
		out.WindKmh = math.Max(out.WindKmh, h.WindKmh)
		if h.Verdict >= goodVerdict {
			out.ClearHours++
		}
		worstDew = worseDewRisk(worstDew, h.DewRisk)
	}

	n := float64(len(hours))
	out.CloudPct = round1(cloud / n)
	out.CloudLowPct = round1(low / n)
	out.CloudHighPct = round1(high / n)
	if transpN > 0 {
		out.Transparency = round2(transp / transpN)
	}
	out.SeeingArcsec = round1(median(seeing))
	out.MinTempC = round1(minTemp)
	out.DewRisk = worstDew
}

// fogRisk reports whether any hour is still, saturated and at its dew point — radiation fog forming at
// ground level, which no cloud-cover field predicts.
func fogRisk(hours []Hour) bool {
	for _, h := range hours {
		if h.HumidityPct >= fogHumidityPct && h.DewSpreadC <= fogSpreadC && h.WindKmh <= fogWindKmh {
			return true
		}
	}
	return false
}

func hoursInRange(hours []Hour, startMs, endMs int64) []Hour {
	var out []Hour
	for _, h := range hours {
		if h.TMs >= startMs && h.TMs <= endMs {
			out = append(out, h)
		}
	}
	return out
}

var dewRiskRank = map[string]int{"": 0, "low": 1, "moderate": 2, "high": 3}

func worseDewRisk(a, b string) string {
	if dewRiskRank[b] > dewRiskRank[a] {
		return b
	}
	return a
}

// median returns the middle value of xs (0 for an empty slice). xs is sorted in place.
func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	for i := 1; i < len(xs); i++ { // insertion sort: the caller never passes more than a night of hours
		for j := i; j > 0 && xs[j] < xs[j-1]; j-- {
			xs[j], xs[j-1] = xs[j-1], xs[j]
		}
	}
	mid := len(xs) / 2
	if len(xs)%2 == 1 {
		return xs[mid]
	}
	return (xs[mid-1] + xs[mid]) / 2
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }
