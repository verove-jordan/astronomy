package skyplan

import (
	"math"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
)

// WxSample is one hourly weather-observability sample, adapted by the API layer from the site
// forecast (internal/weather) so the planner never depends on provider types. Verdict mirrors
// weather.Hour.Verdict: the hour's overall 0–100 observability blend (clouds, seeing, transparency,
// wind, dew, …).
type WxSample struct {
	TMs     int64
	Verdict float64
}

// wxWindowSlackMs widens the night window by one hour on each side when matching hourly samples, so
// a dusk/dawn falling between two forecast hours still picks up its bracketing samples.
const wxWindowSlackMs = int64(time.Hour / time.Millisecond)

// liveFactor is the weather multiplier for one target: the mean hourly Verdict (as 0–1) over the
// night-window hours where the target sits above the minimum altitude — the hours an observer would
// actually spend on it. ok=false when no usable hour is covered (target never up in the window, or
// the forecast horizon does not reach the night) — then no live score exists for the target.
func liveFactor(raDeg, decDeg float64, prm Params, nightStart, nightEnd time.Time, wx []WxSample) (float64, bool) {
	lo := nightStart.UnixMilli() - wxWindowSlackMs
	hi := nightEnd.UnixMilli() + wxWindowSlackMs
	sum, n := 0.0, 0
	for _, s := range wx {
		if s.TMs < lo || s.TMs > hi {
			continue
		}
		alt, _ := astro.Horizontal(raDeg, decDeg, prm.Lat, prm.Lon, time.UnixMilli(s.TMs).UTC())
		if alt < prm.MinAltDeg {
			continue
		}
		sum += clamp01(s.Verdict / 100)
		n++
	}
	if n == 0 {
		return 0, false
	}
	return sum / float64(n), true
}

// applyLiveScores fills each target's weather-aware ScoreLive (clear-sky Score × liveFactor) when
// hourly weather is available. A target whose usable hours fall outside the forecast keeps a nil
// ScoreLive — the UI shows the clear-sky score alone rather than a fabricated live one.
func applyLiveScores(targets []Target, prm Params, night nightCtx) {
	if len(prm.WxHours) == 0 {
		return
	}
	for i := range targets {
		f, ok := liveFactor(targets[i].RADeg, targets[i].DecDeg, prm, night.start, night.end, prm.WxHours)
		if !ok {
			continue
		}
		live := int(math.Round(float64(targets[i].Score) * f))
		targets[i].ScoreLive = &live
		targets[i].SubScores.Weather = round2(f)
	}
}
