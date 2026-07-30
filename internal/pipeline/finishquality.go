// Finish-quality stamping: every run (supervised or not) measures its exported finish PNG with the
// supervisor's deterministic metrics and records the snapshot + threshold warnings on the result —
// so a warm cast, a magenta signal or burned stars is flagged in the run record, never discovered
// only by eye.
package pipeline

import (
	"fmt"
	"strings"

	"github.com/verove-jordan/astronomy/internal/postprocess"
)

// The objective colour/clipping thresholds — shared by the supervisor's scoreFinish penalties and
// the every-run warnings, so "what counts as a defect" is defined exactly once.
const (
	warmCastMax   = 0.015 // sky red-excess beyond this = warm/orange cast
	signalCastMax = 0.03  // |bright-signal green balance| beyond this = magenta/green cast
	greenCastMax  = 0.02  // per-channel median spread beyond this = green cast
	whiteClipMax  = 0.01  // fraction of pixels at 255 beyond this = blown highlights
	// starColorSpreadMin is the minimum red−blue spread of the bright star cores before the field reads
	// as "one colour" (or grey/white) rather than a natural mix of star hues — the star-colour-flattened
	// signal. Only meaningful once cores exist (StarWarmFrac's sampler found ≥20), so it is paired with a
	// clipping check in the warning below.
	starColorSpreadMin = 0.03
	// starSatFracMax is the fraction of bright cores allowed to be over-saturated colour discs before the
	// field reads as a carpet of solid blue/magenta blobs (the cluster failure); bgChromaMax is the mean
	// chroma of the darkest quarter allowed before the background reads as purple-green mottle.
	starSatFracMax = 0.30
	bgChromaMax    = 0.035
)

// stampFinishQuality measures the run's exported finish PNG, stamps the snapshot on res.Final and
// appends run warnings for threshold breaches. Soft-fail: no PNG or a decode error just leaves the
// result unstamped.
func stampFinishQuality(res *Result) {
	if res == nil || res.Final == nil {
		return
	}
	png := ""
	for _, o := range res.Final.Outputs {
		if strings.HasSuffix(o, ".png") {
			png = o
			break
		}
	}
	if png == "" {
		return
	}
	m, err := measureFinish(png)
	if err != nil {
		return
	}
	res.Final.Quality = &postprocess.FinishQuality{
		BlackClip:       m.BlackClip,
		WhiteClip:       m.WhiteClip,
		Median:          m.Median,
		Background:      m.Background,
		GreenCast:       m.GreenCast,
		WarmCast:        m.WarmCast,
		SignalCast:      m.SignalCast,
		StarWarmFrac:    m.StarWarmFrac,
		StarColorSpread: m.StarColorSpread,
		StarSatFrac:     m.StarSatFrac,
		BgChroma:        m.BgChroma,
	}
	res.Warnings = append(res.Warnings, finishQualityWarnings(m)...)
}

// finishQualityWarnings translates threshold breaches into human-readable run warnings.
func finishQualityWarnings(m finishMetrics) []string {
	var w []string
	if m.WarmCast > warmCastMax {
		w = append(w, fmt.Sprintf("finish quality: warm sky cast (%.3f > %.3f) — colour calibration likely fell back; check plate-solve/SPCC", m.WarmCast, warmCastMax))
	}
	if absf(m.SignalCast) > signalCastMax {
		tint := "magenta/pink"
		if m.SignalCast > 0 {
			tint = "green"
		}
		w = append(w, fmt.Sprintf("finish quality: %s cast in the bright signal (%.3f) — channel balance is off", tint, m.SignalCast))
	}
	if m.GreenCast > greenCastMax {
		w = append(w, fmt.Sprintf("finish quality: green background cast (%.3f > %.3f)", m.GreenCast, greenCastMax))
	}
	for c, wc := range m.WhiteClip {
		if wc > whiteClipMax {
			w = append(w, fmt.Sprintf("finish quality: blown highlights (%.2f%% of channel %s at white) — star cores or a galaxy nucleus burning; more StretchHeadroom / a lower CoreHighlightCeil can recover them", wc*100, [3]string{"R", "G", "B"}[c]))
			break
		}
	}
	// A bright-core population with almost no red−blue variety = star colours flattened to one hue or to
	// grey/white (spread is 0 when the sampler found no cores, so a positive-but-tiny spread is the tell).
	if m.StarColorSpread > 0 && m.StarColorSpread < starColorSpreadMin {
		w = append(w, fmt.Sprintf("finish quality: star colours flattened (spread %.3f < %.3f) — bright cores reading as one colour/grey", m.StarColorSpread, starColorSpreadMin))
	}
	// Over-saturated bright cores = solid colour discs (a dense star field / cluster painted blue/magenta).
	if m.StarSatFrac > starSatFracMax {
		w = append(w, fmt.Sprintf("finish quality: star cores over-saturated (%.0f%% of bright cores are colour discs > %.0f%%) — raise star_desat", m.StarSatFrac*100, starSatFracMax*100))
	}
	// Coloured noise in the darkest quarter = purple-green background mottle (shallow colour subs stretched hard).
	if m.BgChroma > bgChromaMax {
		w = append(w, fmt.Sprintf("finish quality: background chroma mottle (%.3f > %.3f) — raise chroma_blur / colour denoise", m.BgChroma, bgChromaMax))
	}
	return w
}
