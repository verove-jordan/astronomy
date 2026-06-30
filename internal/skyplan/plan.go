package skyplan

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/skycat"
)

// altSeriesStep is the sampling interval for the per-target altitude curve.
const altSeriesStep = 15 * time.Minute

// Planner scores catalog objects for imaging on a given night. It is safe for concurrent use; the
// catalog itself is cached inside skycat.
type Planner struct {
	catalogDir string
}

// New builds a Planner reading Siril's catalogs from catalogDir.
func New(catalogDir string) *Planner { return &Planner{catalogDir: catalogDir} }

// Plan scores every catalog object for the night bracketing prm.At, returning the ranked top-`Limit`
// targets plus tonight's darkness summary.
func (p *Planner) Plan(ctx context.Context, prm Params) (*Result, error) {
	cat, err := skycat.LoadCatalog(p.catalogDir)
	if err != nil {
		return nil, fmt.Errorf("load catalog: %w", err)
	}
	records := cat.Records()

	res := &Result{}
	if len(records) == 0 {
		res.Warnings = append(res.Warnings, "no catalog data found")
	}

	sunBelow := -18.0
	if prm.Twilight == "nautical" {
		sunBelow = -12.0
	}
	window := astro.NightWindow(prm.At, prm.Lat, prm.Lon, sunBelow)
	moon := astro.MoonNow(prm.At, prm.Lat, prm.Lon)
	night := computeNight(prm, window)
	res.Darkness = darknessInfo(window, moon, night, prm)
	if window.NoAstroDark {
		res.Warnings = append(res.Warnings, "no astronomical darkness tonight; using "+window.Kind+" twilight")
	}

	weights := prm.Weights
	if weights == (Weights{}) {
		weights = DefaultWeights()
		if prm.Mode == "visual" {
			weights = VisualWeights()
		}
	}
	if prm.Mode == "visual" && len(prm.Eyepieces) == 0 {
		res.Warnings = append(res.Warnings, "visual mode: no eyepieces configured; framing and magnification unavailable")
	}

	targets := make([]Target, 0, 512)
	for _, rec := range records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		t := scoreOne(rec, prm, window, moon, weights)
		if prm.TypeFilter != "" && t.Type != prm.TypeFilter {
			continue
		}
		if prm.CatalogFilter != "" && t.Catalog != prm.CatalogFilter {
			continue
		}
		targets = append(targets, t)
	}

	sort.Slice(targets, func(i, j int) bool {
		if targets[i].Score != targets[j].Score {
			return targets[i].Score > targets[j].Score
		}
		return targets[i].MaxAltDeg > targets[j].MaxAltDeg
	})
	if prm.Limit > 0 && len(targets) > prm.Limit {
		targets = targets[:prm.Limit]
	}
	for i := range targets {
		if targets[i].Flags.Visible {
			targets[i].AltSeries = altSeries(targets[i].RADeg, targets[i].DecDeg, prm, night.start, night.end)
		}
	}
	res.Targets = targets
	res.Count = len(targets)
	return res, nil
}

// NightContext builds the night-chart context (dusk/dawn, the Sun/Moon altitude curves and their
// rise/set, plus the Moon's phase and illumination) for the night bracketing `at` at the given site —
// the same DarknessInfo the tonight planner returns. It is reused by the events calendar to draw a
// per-event altitude chart on the same night, without re-implementing the ephemeris sampling.
func NightContext(at time.Time, lat, lon, elevationM float64, twilight string, loc *time.Location) DarknessInfo {
	prm := Params{At: at, Lat: lat, Lon: lon, ElevationM: elevationM, Twilight: twilight, Location: loc}
	sunBelow := -18.0
	if twilight == "nautical" {
		sunBelow = -12.0
	}
	window := astro.NightWindow(at, lat, lon, sunBelow)
	moon := astro.MoonNow(at, lat, lon)
	night := computeNight(prm, window)
	return darknessInfo(window, moon, night, prm)
}

// scoreOne computes the full scored Target for one catalog record.
func scoreOne(rec skycat.Record, prm Params, window astro.DarkWindow, moon astro.MoonState, w Weights) Target {
	t := Target{
		Name:    rec.Name,
		Aliases: rec.Aliases,
		Catalog: rec.Source,
		Type:    deriveType(rec),
		RADeg:   rec.RADeg,
		DecDeg:  rec.DecDeg,
	}
	t.Composition = compositionFor(rec, t.Type)
	if rec.HasDiameter {
		t.SizeArcmin = rec.DiameterArcmin
	}
	if rec.HasMag {
		t.MagV = rec.MagV
	}

	altNow, azNow := astro.Horizontal(rec.RADeg, rec.DecDeg, prm.Lat, prm.Lon, prm.At)
	t.AltNowDeg = round1(altNow)
	t.AzNowDeg = round1(azNow)
	if app := astro.ApparentAltitude(altNow); app > 0 {
		t.AirmassNow = round2(astro.Airmass(app))
	}

	transit := astro.TransitTimeUTC(rec.RADeg, prm.Lon, midpoint(window))
	t.TransitUTCMs = transit.UnixMilli()
	t.TransitLocal = formatLocal(transit, prm.Location, "15:04")

	_, status := astro.HourAngleForAltitude(prm.MinAltDeg, prm.Lat, rec.DecDeg)
	t.Flags.Circumpolar = status == astro.AlwaysAbove

	if rec.HasMag && rec.HasDiameter {
		t.SurfaceBrightness = round1(surfaceBrightness(rec.MagV, math.Pi*(rec.DiameterArcmin/2)*(rec.DiameterArcmin/2)))
	}
	framingFOVMin, view := applyEyepiece(&t, prm, rec)
	if framingFOVMin > 0 && rec.HasDiameter && rec.DiameterArcmin > 0 {
		t.FovFillPct = round1(rec.DiameterArcmin / framingFOVMin * 100)
	}

	// Hard gate 1: the object never climbs above the minimum altitude from this latitude.
	if astro.TransitAltitude(prm.Lat, rec.DecDeg) < prm.MinAltDeg {
		t.Reason = fmt.Sprintf("Never climbs above %.0f° from your latitude.", prm.MinAltDeg)
		return t
	}

	t.MaxAltDeg = round1(astro.MaxAltitudeInWindow(rec.RADeg, rec.DecDeg, prm.Lat, prm.Lon, window))
	darkHrs := astro.HoursAboveAltInWindow(rec.RADeg, rec.DecDeg, prm.MinAltDeg, prm.Lat, prm.Lon, window)
	t.DarkHoursAboveMin = round1(darkHrs)

	// Hard gate 2: it is only high enough outside tonight's darkness.
	if darkHrs <= 0 {
		t.Reason = "Above your minimum altitude only outside tonight's darkness."
		return t
	}
	t.Flags.Visible = true

	sub := SubScores{
		MaxAlt:    altitudeScore(t.MaxAltDeg, prm.MinAltDeg),
		AltNow:    altitudeScore(altNow, prm.MinAltDeg),
		DarkHours: darkHoursScore(darkHrs, window.Hours()),
	}
	fr, frKnown := framingScore(rec.DiameterArcmin, framingFOVMin, rec.HasDiameter)
	sub.Framing, t.Flags.FramingKnown = fr, frKnown
	det, detKnown := detectabilityScore(rec.MagV, rec.DiameterArcmin, prm.Optics.ApertureMM, rec.HasMag && rec.HasDiameter)
	sens := moonSensitivity(t.Type, detKnown, t.SurfaceBrightness)
	if prm.Mode == "visual" {
		det, detKnown = visualDetectabilityScore(t.Type, rec.MagV, rec.DiameterArcmin, prm.Optics.ApertureMM, view, rec.HasMag, rec.HasDiameter)
		sens = moonSensitivityVisual(t.Type, rec.HasMag && rec.HasDiameter, t.SurfaceBrightness)
	}
	sub.Detectability, t.Flags.DetectabilityKnown = det, detKnown

	t.MoonSepDeg = round1(astro.AngularSeparation(rec.RADeg, rec.DecDeg, moon.RADeg, moon.DecDeg))
	sub.Moon = round2(moonScore(moon.Up, moon.IllumFraction, moon.AltDeg, t.MoonSepDeg, sens))
	sub.LightPollution = round2(lightPollutionScore(prm.SiteSQM, sens))

	t.SubScores = sub
	t.Score = composite(w, sub)
	t.Reason = buildReason(t, moon.Up, moon.IllumFraction)
	return t
}

// applyEyepiece returns the framing field (arcmin) to score against and the chosen eyepiece view. In
// visual mode it selects the best eyepiece for the record and stamps it onto the target, returning the
// eyepiece true field; in camera mode (or when no eyepiece fits) it returns the sensor field and leaves
// the target's visual fields zero.
func applyEyepiece(t *Target, prm Params, rec skycat.Record) (framingFOVMin float64, view EyepieceView) {
	framingFOVMin = prm.Optics.FOVMinArcmin()
	if prm.Mode != "visual" {
		return framingFOVMin, EyepieceView{}
	}
	v, ok := chooseEyepiece(prm.Optics, prm.Eyepieces, rec.DiameterArcmin, rec.HasDiameter)
	if !ok {
		return framingFOVMin, EyepieceView{}
	}
	t.ChosenEyepiece = v.Label
	t.EyepieceFocalMM = v.FocalMM
	t.MagX = round1(v.MagX)
	t.TrueFOVDeg = round2(v.TrueFOVDeg)
	t.ExitPupilMM = round2(v.ExitPupilMM)
	return v.TrueFOVMinArcmin(), v
}

func darknessInfo(w astro.DarkWindow, moon astro.MoonState, night nightCtx, prm Params) DarknessInfo {
	loc := prm.Location
	di := DarknessInfo{
		Kind:         w.Kind,
		NoAstroDark:  w.NoAstroDark,
		DuskUTCMs:    w.Start.UnixMilli(),
		DawnUTCMs:    w.End.UnixMilli(),
		DuskLocal:    formatLocal(w.Start, loc, "2006-01-02 15:04"),
		DawnLocal:    formatLocal(w.End, loc, "2006-01-02 15:04"),
		DarkHours:    round1(w.Hours()),
		NightStartMs: night.start.UnixMilli(),
		NightEndMs:   night.end.UnixMilli(),
		SunSeries:    night.sunSeries,
		MoonSeries:   night.moonSeries,
		Moon: MoonInfo{
			IllumFraction: round2(moon.IllumFraction),
			AltNowDeg:     round1(moon.AltDeg),
			UpNow:         moon.Up,
			Phase:         moonPhaseName(astro.MoonPhaseAngle(prm.At)),
		},
	}
	if night.hasSunSet {
		di.Sun.SetUTCMs, di.Sun.SetLocal = night.sunSet.UnixMilli(), formatLocal(night.sunSet, loc, "15:04")
	}
	if night.hasSunRise {
		di.Sun.RiseUTCMs, di.Sun.RiseLocal = night.sunRise.UnixMilli(), formatLocal(night.sunRise, loc, "15:04")
	}
	if night.hasMoonRise {
		di.Moon.RiseUTCMs, di.Moon.RiseLocal = night.moonRise.UnixMilli(), formatLocal(night.moonRise, loc, "15:04")
	}
	if night.hasMoonSet {
		di.Moon.SetUTCMs, di.Moon.SetLocal = night.moonSet.UnixMilli(), formatLocal(night.moonSet, loc, "15:04")
	}
	return di
}

func altSeries(ra, dec float64, prm Params, start, end time.Time) []AltSample {
	var out []AltSample
	for t := start; !t.After(end); t = t.Add(altSeriesStep) {
		alt, _ := astro.Horizontal(ra, dec, prm.Lat, prm.Lon, t)
		out = append(out, AltSample{TMs: t.UnixMilli(), AltDeg: round1(alt)})
	}
	return out
}

// moonPhaseName buckets a phase angle (0=new … 180=full …) into one of eight named phases that match
// the frontend i18n keys.
func moonPhaseName(angleDeg float64) string {
	names := []string{
		"new", "waxing_crescent", "first_quarter", "waxing_gibbous",
		"full", "waning_gibbous", "last_quarter", "waning_crescent",
	}
	idx := int(math.Mod(angleDeg+22.5, 360) / 45)
	if idx < 0 || idx >= len(names) {
		idx = 0
	}
	return names[idx]
}

func midpoint(w astro.DarkWindow) time.Time {
	return w.Start.Add(w.End.Sub(w.Start) / 2)
}

func formatLocal(t time.Time, loc *time.Location, layout string) string {
	if loc != nil {
		return t.In(loc).Format(layout)
	}
	return t.UTC().Format(layout)
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }
func round2(x float64) float64 { return math.Round(x*100) / 100 }
