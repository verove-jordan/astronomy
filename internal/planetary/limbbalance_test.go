package planetary

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestLimbBalance pins the tone contract: sky and terminator-zone pixels stay EXACTLY untouched,
// the bright limb's base level is compressed, and RELATIVE local contrast (texture amplitude over
// base) survives — the whole point versus a plain highlight shoulder on pixel values.
func TestLimbBalance(t *testing.T) {
	const w, h = 512, 512
	dir := t.TempDir()
	illum := func(x int) float64 { // lit right half: linear 0.2 → 0.9; left half: sky
		if x < w/2 {
			return 0
		}
		return 0.2 + 0.7*float64(x-w/2)/float64(w/2-1)
	}
	val := func(x, y int) float32 {
		il := illum(x)
		if il == 0 {
			return 0.001
		}
		return float32(il * (1 + 0.04*math.Sin(2*math.Pi*float64(x)/16)))
	}
	im := fits.NewImage(w, h, 1)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.Pix[0][y*w+x] = val(x, y)
		}
	}
	base := filepath.Join(dir, "moon_mono")
	if err := im.WriteFITS(base + ".fits"); err != nil {
		t.Fatal(err)
	}

	_, _, _, _, outBase, note := limbBalance("", "", "", "", base, 0.55)
	if note == "" || outBase == base {
		t.Fatalf("expected an applied limb balance, got base=%q note=%q", outBase, note)
	}
	out, err := fits.ReadImage(outBase + ".fits")
	if err != nil {
		t.Fatal(err)
	}

	if got := out.Pix[0][100*w+50]; got != 0.001 {
		t.Fatalf("sky pixel changed: %v", got)
	}
	if got, want := out.Pix[0][100*w+(w/2+12)], im.Pix[0][100*w+(w/2+12)]; got != want {
		t.Fatalf("terminator-zone pixel changed: %v vs %v", got, want)
	}
	// The whole contract in one invariant: out/in must be a SMOOTH per-pixel gain across the limb —
	// linear in x over one texture period (the base-gradient ramp), never oscillating with the sine
	// phase (a value-domain shoulder would flatten the peaks and leave phase-correlated residuals).
	const x0, x1, y = w - 40, w - 24, 200
	n := float64(x1 - x0)
	var sx, sy, sxx, sxy float64
	ratios := make([]float64, 0, x1-x0)
	for x := x0; x < x1; x++ {
		r := float64(out.Pix[0][y*w+x]) / float64(im.Pix[0][y*w+x])
		ratios = append(ratios, r)
		fx := float64(x - x0)
		sx, sy, sxx, sxy = sx+fx, sy+r, sxx+fx*fx, sxy+fx*r
	}
	rMean := sy / n
	if rMean > 0.93 {
		t.Fatalf("bright-limb base not compressed: mean gain %.4f", rMean)
	}
	slope := (n*sxy - sx*sy) / (n*sxx - sx*sx)
	icept := (sy - slope*sx) / n
	for i, r := range ratios {
		if resid := math.Abs(r - (icept + slope*float64(i))); resid/rMean > 0.005 {
			t.Fatalf("gain oscillates with texture phase (residual %.4f at %d): detail is being reshaped", resid, i)
		}
	}

	r2, g2, b2, l2, m2, n2 := limbBalance("", "", "", "", base, 0)
	if n2 != "" || m2 != base || r2 != "" || g2 != "" || b2 != "" || l2 != "" {
		t.Fatal("strength 0 must be a strict no-op")
	}
}
