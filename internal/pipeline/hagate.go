package pipeline

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// The Ha layer is screened as PURE RED over the whole composite, so a stretched layer whose
// BACKGROUND is bright paints the entire sky red no matter how good the other channels are (task
// #355: a noise-dominated Ha master stretched to a ~0.7 background → a solid-red final). The belt
// (preset.HaBlackPoint) and the suspenders (the darker haBg stretch target) both assume a
// sky-dominated histogram; this gate measures the ACTUAL stretched layer and attenuates or drops
// the screen when that assumption failed.
const (
	// haLayerBgMax is the stretched-layer median up to which the screen runs at full strength (a
	// healthy layer's autostretch target is ≤0.14 — well below this).
	haLayerBgMax = 0.30
	// haLayerBgSkip is the median beyond which no attenuation can save the layer — skip the screen.
	haLayerBgSkip = 0.55
	// haStatBase names the measurement FITS the stretch script saves beside ha.tif (deleted after).
	haStatBase = "ha_stat"
	// oiiiStatBase is the OIII screen's counterpart, saved beside oiii.tif.
	oiiiStatBase = "oiii_stat"
	// siiStatBase is the SII screen's counterpart, saved beside sii.tif.
	siiStatBase = "sii_stat"
)

// haScreenGate maps the stretched Ha layer's median to a screen-opacity factor: 1 (full strength)
// below haLayerBgMax, fading linearly to 0 (skip the screen) at haLayerBgSkip.
func haScreenGate(median float64) float64 {
	switch {
	case median <= haLayerBgMax:
		return 1
	case median >= haLayerBgSkip:
		return 0
	default:
		return (haLayerBgSkip - median) / (haLayerBgSkip - haLayerBgMax)
	}
}

// gateHaScreen measures the ha_stat FITS the stretch script saved and returns the screen factor
// plus a human note ("" when the layer is healthy). An unreadable/missing measurement soft-fails
// to full strength — the gate must never break a run that today's pipeline completes.
//
// Production gates every emission layer through gateScreenStat directly, driven by the
// emissionScreens table; this named wrapper is what the Ha gate's own tests exercise.
func gateHaScreen(outDir string) (float64, string) {
	return gateScreenStat(outDir, haStatBase, "Ha")
}

// gateScreenStat implements the shared wash gate for one emission screen layer.
func gateScreenStat(outDir, statBase, line string) (float64, string) {
	path := filepath.Join(outDir, statBase+".fits")
	defer os.Remove(path)
	im, err := fits.ReadImage(path)
	if err != nil {
		return 1, ""
	}
	med := imgops.Percentile(imgops.Subsample(im.Pix[0], 200_000), 50)
	factor := haScreenGate(med)
	switch {
	case factor >= 1:
		return 1, ""
	case factor <= 0:
		return 0, fmt.Sprintf("%s layer background too bright after stretch (median %.2f) — %s screen skipped; check the %s master's calibration/normalization", line, med, line, line)
	default:
		return factor, fmt.Sprintf("%s layer background bright after stretch (median %.2f) — %s screen attenuated to %d%%", line, med, line, int(factor*100))
	}
}
