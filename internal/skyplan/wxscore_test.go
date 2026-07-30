package skyplan

import (
	"testing"
	"time"
)

// TestLiveFactor_Scenarios pins the live-score contract: the factor is the mean hourly verdict over
// the hours the target is actually usable (in the night window AND above the minimum altitude), and
// no usable coverage yields ok=false — never a fabricated factor.
func TestLiveFactor_Scenarios(t *testing.T) {
	// Fixed night: 2026-01-15 21:00→05:00 UTC at lat 45N, lon 0. A Dec=+80° target is circumpolar
	// there (altitude stays ≈ 35–55°, always above the 30° floor); a Dec=−30° target peaks at 15°
	// and is never usable.
	nightStart := time.Date(2026, 1, 15, 21, 0, 0, 0, time.UTC)
	nightEnd := time.Date(2026, 1, 16, 5, 0, 0, 0, time.UTC)
	prm := Params{Lat: 45, Lon: 0, MinAltDeg: 30}

	hourly := func(verdict float64, hours ...int) []WxSample {
		out := make([]WxSample, 0, len(hours))
		for _, h := range hours {
			out = append(out, WxSample{TMs: nightStart.Add(time.Duration(h) * time.Hour).UnixMilli(), Verdict: verdict})
		}
		return out
	}

	tests := []struct {
		name       string
		raDeg, dec float64
		wx         []WxSample
		wantOK     bool
		wantFactor float64
		tol        float64
	}{
		{"clear night is factor 1", 40, 80, hourly(100, 0, 2, 4, 6, 8), true, 1.0, 0.001},
		{"half-cloudy night halves", 40, 80, hourly(50, 0, 2, 4, 6, 8), true, 0.5, 0.001},
		{"forecast outside the night → no live score", 40, 80, hourly(100, -30, -28, 30), false, 0, 0},
		{"target never above min alt → no live score", 40, -30, hourly(100, 0, 2, 4, 6, 8), false, 0, 0},
		{"empty forecast → no live score", 40, 80, nil, false, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, ok := liveFactor(tt.raDeg, tt.dec, prm, nightStart, nightEnd, tt.wx)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v (factor %v)", ok, tt.wantOK, f)
			}
			if ok && (f < tt.wantFactor-tt.tol || f > tt.wantFactor+tt.tol) {
				t.Fatalf("factor = %v, want %v ± %v", f, tt.wantFactor, tt.tol)
			}
		})
	}
}

// TestApplyLiveScores_FillsAndSkips pins the Target-level wiring: covered targets gain
// ScoreLive = round(Score × factor) + the weather sub-score, uncovered targets stay nil, and an
// empty forecast leaves everything untouched.
func TestApplyLiveScores_FillsAndSkips(t *testing.T) {
	nightStart := time.Date(2026, 1, 15, 21, 0, 0, 0, time.UTC)
	nightEnd := time.Date(2026, 1, 16, 5, 0, 0, 0, time.UTC)
	prm := Params{Lat: 45, Lon: 0, MinAltDeg: 30}
	night := nightCtx{start: nightStart, end: nightEnd}
	wx := []WxSample{
		{TMs: nightStart.Add(1 * time.Hour).UnixMilli(), Verdict: 60},
		{TMs: nightStart.Add(3 * time.Hour).UnixMilli(), Verdict: 60},
	}

	targets := []Target{
		{Name: "circumpolar", RADeg: 40, DecDeg: 80, Score: 90},
		{Name: "never-up", RADeg: 40, DecDeg: -30, Score: 80},
	}
	prm.WxHours = wx
	applyLiveScores(targets, prm, night)

	if targets[0].ScoreLive == nil {
		t.Fatal("covered target: ScoreLive is nil")
	}
	if got, want := *targets[0].ScoreLive, 54; got != want { // 90 × 0.60
		t.Fatalf("ScoreLive = %d, want %d", got, want)
	}
	if targets[0].SubScores.Weather != 0.6 {
		t.Fatalf("weather sub-score = %v, want 0.6", targets[0].SubScores.Weather)
	}
	if targets[1].ScoreLive != nil {
		t.Fatal("never-usable target: ScoreLive should stay nil")
	}

	untouched := []Target{{Name: "x", RADeg: 40, DecDeg: 80, Score: 90}}
	applyLiveScores(untouched, Params{Lat: 45, Lon: 0, MinAltDeg: 30}, night)
	if untouched[0].ScoreLive != nil {
		t.Fatal("empty forecast must leave ScoreLive nil")
	}
}
