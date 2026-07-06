package agent

import (
	"context"
	"encoding/json"
	"time"

	"github.com/verove-jordan/astronomy/internal/align"
	"github.com/verove-jordan/astronomy/internal/polaralign"
	"github.com/verove-jordan/astronomy/internal/skyevents"
	"github.com/verove-jordan/astronomy/internal/skyplan"
)

// registerSkyTools exposes session planning: tonight's best targets, the event calendar, polar-scope
// readout and the GoTo alignment star sequence (all read-only).
func registerSkyTools(r *Registry, d Deps) {
	r.Add(Tool{
		Name: "sky_targets", Category: "sky",
		Description: "Rank the best deep-sky targets to image tonight for the user's site and optics (altitude, dark hours, framing, light pollution). Defaults location to the user's setup.",
		Schema: objectSchema(nil, map[string]any{
			"lat": numProp("observer latitude (default = user setup)"),
			"lon": numProp("observer longitude (default = user setup)"),
			"at":  strProp("RFC3339 time (default now)"),
		}),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) { return skyTargets(ctx, d, args) },
	})
	r.Add(Tool{
		Name: "sky_events", Category: "sky",
		Description: "Upcoming astronomical events for the site over the next month (eclipses, conjunctions, meteor showers, comets, ISS passes) with visibility scores.",
		Schema: objectSchema(nil, map[string]any{
			"lat":  numProp("latitude (default = user setup)"),
			"lon":  numProp("longitude (default = user setup)"),
			"days": intProp("horizon in days (default 30)"),
		}),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) { return skyEvents(ctx, d, args) },
	})
	r.Add(Tool{
		Name: "polar_align", Category: "sky",
		Description: "Polar-scope readout for the site now: pole star, hour angle, clock position and offset — for polar-aligning an equatorial mount.",
		Schema: objectSchema(nil, map[string]any{
			"lat": numProp("latitude (default = user setup)"),
			"lon": numProp("longitude (default = user setup)"),
		}),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) { return polarAlign(d, args) },
	})
	r.Add(Tool{
		Name: "goto_align", Category: "sky",
		Description: "Best GoTo alignment stars (in order) for the site now, for a given mount profile (eq-generic, synscan-eq, celestron-eq, altaz-generic, synscan-altaz, celestron-altaz).",
		Schema: objectSchema(nil, map[string]any{
			"lat":     numProp("latitude (default = user setup)"),
			"lon":     numProp("longitude (default = user setup)"),
			"profile": strProp("mount profile key (default eq-generic)"),
			"count":   intProp("number of stars (default per profile)"),
		}),
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) { return gotoAlign(d, args) },
	})
}

// siteArgs is the common lat/lon/at input for site tools.
type siteArgs struct {
	Lat *float64 `json:"lat"`
	Lon *float64 `json:"lon"`
	At  string   `json:"at"`
}

func (s siteArgs) latLon(d Deps) (float64, float64) {
	lat, lon := d.Cfg.LatDeg, d.Cfg.LonDeg
	if s.Lat != nil {
		lat = *s.Lat
	}
	if s.Lon != nil {
		lon = *s.Lon
	}
	return lat, lon
}

func (s siteArgs) when() time.Time {
	if s.At != "" {
		if t, err := time.Parse(time.RFC3339, s.At); err == nil {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

func opticsOf(d Deps) skyplan.Optics {
	c := d.Cfg
	return skyplan.Optics{
		FocalMM: c.FocalLenMM, ApertureMM: c.ApertureMM, PixelUm: c.PixelSizeUm,
		SensorWpx: c.SensorWpx, SensorHpx: c.SensorHpx, BarlowX: c.BarlowX,
	}
}

func skyTargets(ctx context.Context, d Deps, args json.RawMessage) (string, error) {
	var in siteArgs
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	lat, lon := in.latLon(d)
	prm := skyplan.Params{
		At: in.when(), Lat: lat, Lon: lon, ElevationM: d.Cfg.ElevationM,
		MinAltDeg: 30, Twilight: "astro", Limit: 15, Location: d.Cfg.Location(), Optics: opticsOf(d),
	}
	site, _ := d.LightPollution.At(ctx, lat, lon)
	prm.SiteSQM = site.SQM
	res, err := d.Planner.Plan(ctx, prm)
	if err != nil {
		return "", err
	}
	rows := make([]map[string]any, 0, len(res.Targets))
	for _, t := range res.Targets {
		rows = append(rows, map[string]any{
			"name": t.Name, "catalog": t.Catalog, "type": t.Type, "mag": t.MagV,
			"alt_now": round1(t.AltNowDeg), "max_alt": round1(t.MaxAltDeg), "dark_hours": round1(t.DarkHoursAboveMin),
			"moon_sep": round1(t.MoonSepDeg), "score": t.Score, "reason": t.Reason,
		})
	}
	return jsonResult(map[string]any{"site": map[string]any{"lat": lat, "lon": lon, "sqm": site.SQM, "bortle": site.Bortle}, "count": len(rows), "targets": rows})
}

func skyEvents(ctx context.Context, d Deps, args json.RawMessage) (string, error) {
	var in struct {
		siteArgs
		Days int `json:"days"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	lat, lon := in.siteArgs.latLon(d)
	if in.Days <= 0 || in.Days > 400 {
		in.Days = 30
	}
	from := time.Now().UTC()
	prm := skyevents.Params{
		From: from, To: from.AddDate(0, 0, in.Days), Lat: lat, Lon: lon,
		ElevationM: d.Cfg.ElevationM, Twilight: "astro", Location: d.Cfg.Location(), Optics: opticsOf(d),
	}
	res, err := d.Events.Compute(ctx, prm)
	if err != nil {
		return "", err
	}
	rows := make([]map[string]any, 0, len(res.Events))
	for _, e := range res.Events {
		rows = append(rows, map[string]any{
			"title": e.Title, "kind": e.Kind, "subtype": e.Subtype,
			"peak_ms": e.PeakUTCMs, "score": e.Score, "magnitude": e.Magnitude,
		})
	}
	return jsonResult(map[string]any{"count": len(rows), "events": rows})
}

func polarAlign(d Deps, args json.RawMessage) (string, error) {
	var in siteArgs
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	lat, lon := in.latLon(d)
	return jsonResult(polaralign.Compute(in.when(), lat, lon))
}

func gotoAlign(d Deps, args json.RawMessage) (string, error) {
	var in struct {
		siteArgs
		Profile string `json:"profile"`
		Count   int    `json:"count"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return "", err
	}
	lat, lon := in.siteArgs.latLon(d)
	profileKey := in.Profile
	if profileKey == "" {
		profileKey = "eq-generic"
	}
	res := align.Plan(align.Params{At: in.siteArgs.when(), Lat: lat, Lon: lon}, align.Lookup(profileKey), in.Count, nil, nil)
	stars := make([]map[string]any, 0, len(res.Stars))
	for _, st := range res.Stars {
		stars = append(stars, map[string]any{
			"name": st.Name, "mag": st.Mag, "alt": round1(st.AltDeg), "az": round1(st.AzDeg),
			"compass": st.Compass, "order": st.Order, "status": st.Status,
		})
	}
	return jsonResult(map[string]any{
		"profile": res.Profile, "mount_type": res.MountType, "quality": res.QualityScore,
		"stars": stars, "warnings": res.Warnings,
	})
}

func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}
