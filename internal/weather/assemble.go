package weather

import "sort"

// assembleForecast merges the source feeds onto Open-Meteo's hourly timeline and computes the derived
// metrics (dew spread/risk, transparency, per-hour verdict). 7Timer's 3-hourly seeing/transparency are
// forward-filled to the matching hours; air-quality AOD is matched by timestamp.
func assembleForecast(lat, lon float64, om omResponse, aq aqResponse, aqOK bool, st stResponse, stOK bool) SiteForecast {
	aod := aodByTime(aq, aqOK)
	stVals := sevenTimerSeries(st, stOK)

	hours := make([]Hour, 0, len(om.Hourly.Time))
	for i, ts := range om.Hourly.Time {
		ms, ok := parseOMTime(ts)
		if !ok {
			continue
		}
		at := func(s []float64) float64 {
			if i < len(s) {
				return s[i]
			}
			return 0
		}
		h := Hour{
			TMs:         ms,
			CloudPct:    at(om.Hourly.CloudCover),
			CloudLow:    at(om.Hourly.CloudCoverLow),
			CloudMid:    at(om.Hourly.CloudCoverMid),
			CloudHigh:   at(om.Hourly.CloudCoverHigh),
			HumidityPct: at(om.Hourly.RelativeHumidity2m),
			DewPointC:   at(om.Hourly.DewPoint2m),
			TempC:       at(om.Hourly.Temperature2m),
			WindKmh:     at(om.Hourly.WindSpeed10m),
			GustKmh:     at(om.Hourly.WindGusts10m),
			Jet300Kmh:   at(om.Hourly.WindSpeed300hPa),
			CAPE:        at(om.Hourly.CAPE),
			LiftedIndex: at(om.Hourly.LiftedIndex),
			VisibilityM: at(om.Hourly.Visibility),
			PrecipPct:   at(om.Hourly.PrecipitationProbability),
			AOD:         aod[ms],
		}
		h.DewSpreadC = round1(h.TempC - h.DewPointC)
		h.DewRisk = dewRisk(h.DewSpreadC)

		seeing, transp := stVals.at(ms)
		h.SeeingArcsec = seeing
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
