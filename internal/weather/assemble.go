package weather

import "sort"

// assembleForecast merges the source feeds onto Open-Meteo's hourly timeline and computes the derived
// metrics (dew spread/risk, transparency, per-hour verdict). 7Timer's 3-hourly seeing/transparency are
// forward-filled to the matching hours; air-quality AOD is matched by timestamp.
func assembleForecast(lat, lon float64, om omResponse, aq aqResponse, aqOK bool, st stResponse, stOK bool) SiteForecast {
	aod := aodByTime(aq, aqOK)
	stVals := sevenTimerSeries(st, stOK)

	hours := make([]Hour, 0, len(om.Hourly.Time))
	for i := range om.Hourly.Time {
		h, ok := omHour(om.Hourly, i)
		if !ok {
			continue
		}
		h.AOD = aod[h.TMs]

		// Seeing precedence: the wind-shear index derived from this same hourly response wins, because
		// it is hourly and at model resolution; 7Timer's 3-hourly 10 km index only fills the gap when
		// the pressure levels are missing.
		seeing, transp := stVals.at(h.TMs)
		if h.SeeingArcsec == 0 && seeing > 0 {
			h.SeeingArcsec, h.SeeingSource = seeing, SeeingSourceSevenTimer
		}
		switch {
		case transp > 0:
			h.Transparency = transp
		case h.AOD > 0:
			h.Transparency = transparencyFromAOD(h.AOD)
		}
		h.Verdict = hourVerdict(h)
		hours = append(hours, h)
	}

	f := SiteForecast{Lat: lat, Lon: lon, IssuedMs: nowMs(), Hours: hours, Sources: []string{"Open-Meteo"}}
	if aqOK {
		f.Sources = append(f.Sources, "Open-Meteo Air Quality")
	}
	if stOK {
		f.Sources = append(f.Sources, "7Timer! ASTRO")
	}
	return f
}

// omHour builds one Hour from index i of an Open-Meteo hourly block: everything the response itself
// supplies, plus the metrics derivable from it alone (dew spread and risk, the wind-shear seeing
// index). Feeds that arrive separately — 7Timer, air quality — are layered on by the caller. Shared
// by the per-site forecast and the night scan so both read one response the same way.
func omHour(h omHourly, i int) (Hour, bool) {
	if i >= len(h.Time) {
		return Hour{}, false
	}
	ms, ok := parseOMTime(h.Time[i])
	if !ok {
		return Hour{}, false
	}
	at := func(s []float64) float64 {
		if i < len(s) {
			return s[i]
		}
		return 0
	}
	out := Hour{
		TMs:         ms,
		CloudPct:    at(h.CloudCover),
		CloudLow:    at(h.CloudCoverLow),
		CloudMid:    at(h.CloudCoverMid),
		CloudHigh:   at(h.CloudCoverHigh),
		HumidityPct: at(h.RelativeHumidity2m),
		DewPointC:   at(h.DewPoint2m),
		TempC:       at(h.Temperature2m),
		WindKmh:     at(h.WindSpeed10m),
		GustKmh:     at(h.WindGusts10m),
		Jet300Kmh:   at(h.WindSpeed300hPa),
		CAPE:        at(h.CAPE),
		LiftedIndex: at(h.LiftedIndex),
		VisibilityM: at(h.Visibility),
		PrecipPct:   at(h.PrecipitationProbability),
		BLHeightM:   at(h.BoundaryLayerHeight),
	}
	out.DewSpreadC = round1(out.TempC - out.DewPointC)
	out.DewRisk = dewRisk(out.DewSpreadC)
	if s := derivedSeeing(shear{
		jetKmh:     out.Jet300Kmh,
		w500Kmh:    at(h.WindSpeed500hPa),
		w850Kmh:    at(h.WindSpeed850hPa),
		surfaceKmh: out.WindKmh,
		blHeightM:  out.BLHeightM,
	}); s > 0 {
		out.SeeingArcsec, out.SeeingSource = s, SeeingSourceDerived
	}
	return out, true
}

func aodByTime(aq aqResponse, ok bool) map[int64]float64 {
	out := map[int64]float64{}
	if !ok {
		return out
	}
	for i, ts := range aq.Hourly.Time {
		if ms, good := parseOMTime(ts); good && i < len(aq.Hourly.AerosolOpticalDepth) {
			out[ms] = aq.Hourly.AerosolOpticalDepth[i]
		}
	}
	return out
}

// stSeries holds 7Timer seeing/transparency samples (3-hourly), forward-filled by at().
type stSeries struct {
	ms     []int64
	seeing []float64
	transp []float64
}

func sevenTimerSeries(st stResponse, ok bool) stSeries {
	var s stSeries
	if !ok {
		return s
	}
	type pt struct {
		ms             int64
		seeing, transp float64
	}
	var pts []pt
	for _, d := range st.Dataseries {
		if ms, good := stTimeMs(st.Init, d.Timepoint); good {
			pts = append(pts, pt{ms, seeingArcsec(d.Seeing), transparencyScore(d.Transparency)})
		}
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].ms < pts[j].ms })
	for _, p := range pts {
		s.ms = append(s.ms, p.ms)
		s.seeing = append(s.seeing, p.seeing)
		s.transp = append(s.transp, p.transp)
	}
	return s
}

// at returns the most recent 7Timer sample at or before ms (forward-fill); zero/zero when none.
func (s stSeries) at(ms int64) (seeing, transp float64) {
	for i, t := range s.ms {
		if t <= ms {
			seeing, transp = s.seeing[i], s.transp[i]
		} else {
			break
		}
	}
	return seeing, transp
}

// assembleGrid turns the multi-point Open-Meteo response into the animated cube: one float frame per
// timestep per layer, cells in the same row-major order the grid points were requested in.
func assembleGrid(resp []omResponse, nx, ny int, bbox [4]float64, layers []string) Grid {
	g := Grid{BBox: bbox, Nx: nx, Ny: ny, Layers: map[string][][]float32{}, IssuedMs: nowMs()}
	if len(resp) == 0 {
		return g
	}
	for _, ts := range resp[0].Hourly.Time {
		if ms, ok := parseOMTime(ts); ok {
			g.Timesteps = append(g.Timesteps, ms)
		}
	}
	nt := len(g.Timesteps)
	cells := nx * ny
	for _, layer := range layers {
		frames := make([][]float32, nt)
		for t := range frames {
			frames[t] = make([]float32, cells)
		}
		for c := 0; c < cells && c < len(resp); c++ {
			series := gridSeries(resp[c].Hourly, layer)
			for t := 0; t < nt && t < len(series); t++ {
				frames[t][c] = float32(series[t])
			}
		}
		g.Layers[layer] = frames
	}
	return g
}
