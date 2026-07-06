package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/verove-jordan/astronomy/internal/darksky"
	"github.com/verove-jordan/astronomy/internal/lightpollution"
)

// registerConditionTools exposes site conditions: light pollution, dark-site search, weather, and the
// offline light-pollution atlas (status is read-only; building it is mutating).
func registerConditionTools(r *Registry, d Deps) {
	r.Add(Tool{
		Name: "light_pollution", Category: "conditions",
		Description: "Sky brightness (SQM) and Bortle class at a location. Defaults to the user's site.",
		Schema: objectSchema(nil, map[string]any{
			"lat": numProp("latitude (default = user setup)"), "lon": numProp("longitude (default = user setup)"),
		}),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) { return lightPollutionAt(ctx, d, args) },
	})
	r.Add(Tool{
		Name: "dark_sites", Category: "conditions",
		Description: "Find the best observing sites near a location — ranked by sky darkness and how open the horizon is (accounting for terrain and nearby trees/forest, with the low southern sky weighted most). Reports straight-line and by-road driving distance/time. Defaults to the user's site.",
		Schema: objectSchema(nil, map[string]any{
			"lat": numProp("latitude (default = user setup)"), "lon": numProp("longitude (default = user setup)"),
			"radius_deg": numProp("search radius in degrees (default 3, max 6)"),
			"max_bortle": intProp("keep only sites at or below this Bortle (default 4)"),
		}),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) { return darkSites(ctx, d, args) },
	})
	r.Add(Tool{
		Name: "weather", Category: "conditions",
		Description: "Astronomy weather forecast for a site: cloud cover, seeing, transparency and an overall observability verdict per hour, plus the best clear window tonight. Defaults to the user's site.",
		Schema: objectSchema(nil, map[string]any{
			"lat": numProp("latitude (default = user setup)"), "lon": numProp("longitude (default = user setup)"),
		}),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) { return weatherAt(ctx, d, args) },
	})
	r.Add(Tool{
		Name: "atlas_status", Category: "conditions",
		Description: "Status of the offline light-pollution atlas (idle/building/done + coverage).",
		Schema:      objectSchema(nil, map[string]any{}),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			return jsonResult(d.LightPollution.BuildStateNow())
		},
	})
	r.Add(Tool{
		Name: "build_atlas", Category: "conditions", Mutating: true,
		Description: "Build/download the offline light-pollution atlas for a region so dark-site and light-pollution queries work offline (a background download).",
		Schema: objectSchema(nil, map[string]any{
			"region": strProp("france|europe|world (default france)"),
			"year":   intProp("VIIRS data year (default 2023)"),
		}),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) { return buildAtlas(d, args) },
	})
}

func lightPollutionAt(ctx context.Context, d Deps, args json.RawMessage) (string, error) {
	var in siteArgs
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	lat, lon := in.latLon(d)
	site, warn := d.LightPollution.At(ctx, lat, lon)
	out := map[string]any{"lat": lat, "lon": lon, "sqm": site.SQM, "bortle": site.Bortle, "source": site.Source}
	if warn != "" {
		out["warning"] = warn
	}
	return jsonResult(out)
}

func darkSites(ctx context.Context, d Deps, args json.RawMessage) (string, error) {
	var in struct {
		siteArgs
		RadiusDeg float64 `json:"radius_deg"`
		MaxBortle int     `json:"max_bortle"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	lat, lon := in.siteArgs.latLon(d)
	radius := in.RadiusDeg
	if radius <= 0 {
		radius = 3
	}
	if radius > 6 {
		radius = 6
	}
	q := darksky.Query{
		Bbox:      lightpollution.Bbox{MinLat: lat - radius, MinLon: lon - radius, MaxLat: lat + radius, MaxLon: lon + radius},
		MaxBortle: in.MaxBortle, Limit: 12, Horizon: true, ObsLat: lat, ObsLon: lon, ObsSet: true,
	}
	res := d.DarkSky.Find(ctx, q)
	rows := make([]map[string]any, 0, len(res.Candidates))
	for _, c := range res.Candidates {
		row := map[string]any{
			"lat": c.Lat, "lon": c.Lon, "sqm": c.SQM, "bortle": c.Bortle,
			"distance_km": round1(c.DistanceKm), "score": c.Score,
		}
		if c.DriveKm > 0 {
			row["drive_km"] = c.DriveKm
			row["drive_min"] = c.DriveMin
		}
		if c.Horizon != nil {
			row["openness_pct"] = c.Horizon.OpennessPct
			row["max_obstruction_deg"] = c.Horizon.MaxObstructionDeg
			row["south_obstruction_deg"] = c.Horizon.SouthObstructionDeg
			row["south_openness_pct"] = c.Horizon.SouthOpennessPct
			if c.Horizon.CanopyM > 0 {
				row["canopy_m"] = c.Horizon.CanopyM
			}
		}
		rows = append(rows, row)
	}
	return jsonResult(map[string]any{"count": len(rows), "candidates": rows, "warnings": res.Warnings})
}

func weatherAt(ctx context.Context, d Deps, args json.RawMessage) (string, error) {
	var in siteArgs
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	lat, lon := in.latLon(d)
	fc, warn := d.Weather.Forecast(ctx, lat, lon)
	hours := make([]map[string]any, 0, 12)
	for i, h := range fc.Hours {
		if i%2 != 0 || len(hours) >= 12 { // downsample to ~every 2h, first ~24h
			continue
		}
		hours = append(hours, map[string]any{
			"t_ms": h.TMs, "cloud_pct": round1(h.CloudPct), "seeing": round1(h.SeeingArcsec),
			"transparency": round1(h.Transparency), "dew_risk": h.DewRisk, "verdict": round1(h.Verdict),
		})
	}
	out := map[string]any{"lat": lat, "lon": lon, "issued_ms": fc.IssuedMs, "hours": hours, "sources": fc.Sources}
	if fc.Best != nil {
		out["best_window"] = map[string]any{"start_ms": fc.Best.StartMs, "end_ms": fc.Best.EndMs, "verdict": round1(fc.Best.Verdict)}
	}
	if warn != "" {
		out["warning"] = warn
	}
	return jsonResult(out)
}

func buildAtlas(d Deps, args json.RawMessage) (string, error) {
	var in struct {
		Region string `json:"region"`
		Year   int    `json:"year"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	if in.Region == "" {
		in.Region = "france"
	}
	if in.Year == 0 {
		in.Year = 2023
	}
	bounds, err := lightpollution.ResolveBounds(in.Region, "")
	if err != nil {
		return "", fmt.Errorf("resolve region: %w", err)
	}
	if err := d.LightPollution.StartBuild(bounds, in.Year); err != nil {
		return "", err
	}
	return jsonResult(map[string]any{"started": true, "region": in.Region, "year": in.Year})
}
