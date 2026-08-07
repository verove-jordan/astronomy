package scene3d

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/annotate"
)

// DepthSource says how far a star's distance can be trusted. It rides in the binary record's flags
// so the viewer can filter on it, and the counts are reported in the manifest — a scene that is
// mostly guesses must be able to say so.
type DepthSource uint8

const (
	DepthUnknown   DepthSource = iota // no catalogue entry and no usable colour → not placed at all
	DepthMeasured                     // the catalogue's own parallax (Gaia DR3 / Tycho-2 via ATHYG)
	DepthEstimated                    // spectroscopic parallax from this frame's colour and magnitude
)

func (d DepthSource) String() string {
	switch d {
	case DepthMeasured:
		return "measured"
	case DepthEstimated:
		return "estimated"
	default:
		return "unknown"
	}
}

const (
	// hexFloor mirrors starHex's lift toward white in internal/annotate: it writes
	// floor + (1-floor)·(v/max) per channel, so the raw channel ratios are recoverable by inverting
	// exactly that. Keeping the two in step is what makes the colour index a measurement rather than a
	// guess; a change there without a change here would silently skew every estimated distance.
	hexFloor = 0.45
	// hexChannelEps guards the ratio against a channel that quantised to the floor. A very red star can
	// land at fb = 0 in 8 bits, and 1/0 is not a colour.
	hexChannelEps = 0.02

	// minCalibPairs / maxCalibRMS gate the colour calibration. Below the count the fit is noise;
	// above the RMS (in B−V magnitudes) the frame's colours simply do not track the catalogue's, which
	// happens on a narrowband or badly colour-calibrated stack. Either way estimation is switched off
	// for the run and the manifest says why, rather than shipping fiction.
	minCalibPairs = 20
	maxCalibRMS   = 0.35

	// minDistPc / maxDistPc bound a believable photometric distance. Inside 1 pc there are no stars
	// but the Sun, and past 50 kpc a Milky Way field star is a measurement artefact.
	minDistPc = 1
	maxDistPc = 50_000
)

// zamsTable is the main-sequence colour–magnitude relation: B−V against absolute V magnitude, from
// O5 to M5. Interpolated, and extrapolation is refused (a colour outside the table is not a main
// sequence star).
//
// Deliberately a STANDARD table rather than a fit to the frame's own identified stars: any real
// field mixes dwarfs with giants, so fitting it would bake the giant contamination into the relation
// and make every estimate worse in a way that looks self-consistent. The cost of the standard table
// is the method's known failure — a red giant is intrinsically far brighter than a red dwarf of the
// same colour, so it is placed several times too close. That is why estimated stars are flagged.
var zamsTable = [...]struct{ ci, absMag float64 }{
	{-0.33, -5.70}, {-0.30, -4.00}, {-0.24, -2.60}, {-0.16, -1.20}, {-0.09, -0.25},
	{0.00, 0.65}, {0.07, 1.30}, {0.14, 1.95}, {0.23, 2.35}, {0.31, 2.70},
	{0.38, 3.10}, {0.43, 3.50}, {0.53, 4.00}, {0.59, 4.40}, {0.63, 4.83},
	{0.66, 5.10}, {0.74, 5.50}, {0.82, 5.90}, {0.92, 6.35}, {1.15, 7.35},
	{1.30, 8.00}, {1.41, 8.80}, {1.48, 9.60}, {1.52, 10.40}, {1.61, 12.30},
}

// zamsAbsMag interpolates the main-sequence absolute magnitude for a colour index, refusing colours
// outside the tabulated range.
func zamsAbsMag(ci float64) (float64, bool) {
	if ci < zamsTable[0].ci || ci > zamsTable[len(zamsTable)-1].ci {
		return 0, false
	}
	i := sort.Search(len(zamsTable), func(i int) bool { return zamsTable[i].ci >= ci })
	if i == 0 {
		return zamsTable[0].absMag, true
	}
	lo, hi := zamsTable[i-1], zamsTable[i]
	t := (ci - lo.ci) / (hi.ci - lo.ci)
	return lo.absMag + t*(hi.absMag-lo.absMag), true
}

// colourProxy recovers an instrumental colour index from a star's rendered hex, by inverting the
// per-channel normalisation annotate applied. Returns 2.5·log10(R/B) — redder is larger, which is
// the same sense as B−V, so the calibration below is a straight line rather than a reflection.
func colourProxy(hex string) (float64, bool) {
	r, g, b, ok := parseHex(hex)
	if !ok {
		return 0, false
	}
	// A grey pixel carries no colour: a mono master renders every star white, and calibrating on that
	// would fit pure noise.
	if r == g && g == b {
		return 0, false
	}
	raw := func(c float64) float64 {
		v := (c - hexFloor) / (1 - hexFloor)
		return math.Max(hexChannelEps, math.Min(1, v))
	}
	return 2.5 * math.Log10(raw(r)/raw(b)), true
}

// parseHex reads "#rrggbb" into three [0,1] channel values.
func parseHex(s string) (r, g, b float64, ok bool) {
	if len(s) != 7 || s[0] != '#' {
		return 0, 0, 0, false
	}
	var v [3]float64
	for i := 0; i < 3; i++ {
		hi, ok1 := hexDigit(s[1+2*i])
		lo, ok2 := hexDigit(s[2+2*i])
		if !ok1 || !ok2 {
			return 0, 0, 0, false
		}
		v[i] = float64(hi*16+lo) / 255
	}
	return v[0], v[1], v[2], true
}

func hexDigit(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10, true
	}
	return 0, false
}

// Photometric reports whether estimated distances were available for a run and how well they hold
// up. It is shipped in the manifest so the viewer can warn — or default the estimated layer off —
// instead of silently drawing a bad fit.
type Photometric struct {
	Calibrated bool    `json:"calibrated"`
	Reason     string  `json:"reason,omitempty"` // why not, when Calibrated is false
	Pairs      int     `json:"pairs"`            // identified stars with both a colour and a catalogue B−V
	Slope      float64 `json:"slope,omitempty"`
	Intercept  float64 `json:"intercept,omitempty"`
	RMS        float64 `json:"rms,omitempty"` // scatter of the colour fit, in B−V magnitudes
	// HoldoutN/HoldoutMedianRatio/HoldoutScatterDex grade the whole ladder against the stars that DO
	// have a measured parallax: each one's photometric distance is computed without ever consulting its
	// own catalogue distance, then compared to it. A median ratio near 1 means the estimates land in
	// the right place; the scatter (in decades of distance) is how far a single one may be trusted.
	HoldoutN           int     `json:"holdout_n,omitempty"`
	HoldoutMedianRatio float64 `json:"holdout_median_ratio,omitempty"`
	HoldoutScatterDex  float64 `json:"holdout_scatter_dex,omitempty"`
}

// colourFit is the frame's own colour → B−V calibration.
type colourFit struct {
	slope, intercept float64
	ok               bool
}

// fitColour calibrates this frame's rendered colours against the catalogue's B−V, using the stars
// identified in this very image. Every stack has its own colour balance — filters, SPCC, the stretch
// — so a hard-coded conversion would be wrong for all of them; fitting in-frame is what makes the
// colour index a measurement of THIS picture.
//
// Robust by 2σ clipping rather than a plain least squares: a mis-identified star or a blended pair
// is a large outlier, and one of those can tilt a line through a few hundred points.
func fitColour(points []annotate.Point) (colourFit, Photometric) {
	type pair struct{ h, ci float64 }
	var pairs []pair
	for _, p := range points {
		if p.Star == nil || p.Star.CI == nil {
			continue
		}
		h, ok := colourProxy(p.Hex)
		if !ok {
			continue
		}
		pairs = append(pairs, pair{h, *p.Star.CI})
	}
	ph := Photometric{Pairs: len(pairs)}
	if len(pairs) < minCalibPairs {
		ph.Reason = "not enough identified stars with a colour (mono or shallow catalogue)"
		return colourFit{}, ph
	}

	keep := make([]bool, len(pairs))
	for i := range keep {
		keep[i] = true
	}
	var slope, intercept, rms float64
	for pass := 0; pass < 3; pass++ {
		var n, sx, sy, sxx, sxy float64
		for i, p := range pairs {
			if !keep[i] {
				continue
			}
			n, sx, sy, sxx, sxy = n+1, sx+p.h, sy+p.ci, sxx+p.h*p.h, sxy+p.h*p.ci
		}
		den := n*sxx - sx*sx
		if n < minCalibPairs || den == 0 {
			ph.Reason = "the frame's colours do not track the catalogue"
			return colourFit{}, ph
		}
		slope = (n*sxy - sx*sy) / den
		intercept = (sy - slope*sx) / n
		var ss float64
		for i, p := range pairs {
			if !keep[i] {
				continue
			}
			d := p.ci - (slope*p.h + intercept)
			ss += d * d
		}
		rms = math.Sqrt(ss / n)
		if pass == 2 || rms == 0 {
			break
		}
		for i, p := range pairs {
			keep[i] = math.Abs(p.ci-(slope*p.h+intercept)) <= 2*rms
		}
	}

	ph.Slope, ph.Intercept, ph.RMS = slope, intercept, rms
	if rms > maxCalibRMS {
		ph.Reason = "the frame's colours do not track the catalogue closely enough to estimate distance"
		return colourFit{}, ph
	}
	ph.Calibrated = true
	return colourFit{slope: slope, intercept: intercept, ok: true}, ph
}

// colourIndex returns the B−V this frame's fitted relation implies for a rendered star colour. It is
// an estimate — but one calibrated against the catalogued stars in this very image, which is what
// makes it better than any fixed conversion could be. ok=false when the frame never calibrated or
// the colour is unusable (a mono master renders every star white).
func (f colourFit) colourIndex(hex string) (float64, bool) {
	if !f.ok {
		return 0, false
	}
	h, ok := colourProxy(hex)
	if !ok {
		return 0, false
	}
	return f.slope*h + f.intercept, true
}

// distance estimates one star's distance from its colour and apparent magnitude — the classical
// spectroscopic parallax, d = 10^((m − M + 5)/5). Interstellar extinction is NOT corrected: it both
// reddens (pushing M fainter) and dims (pushing d farther), so along a dusty line of sight the two
// partly cancel and the residual lands inside the method's own scatter. Reported, not modelled.
func (f colourFit) distance(p annotate.Point) (float64, bool) {
	if !f.ok || !hasMag(p) {
		return 0, false
	}
	h, ok := colourProxy(p.Hex)
	if !ok {
		return 0, false
	}
	absMag, ok := zamsAbsMag(f.slope*h + f.intercept)
	if !ok {
		return 0, false
	}
	d := math.Pow(10, (p.Mag-absMag+5)/5)
	if !(d >= minDistPc && d <= maxDistPc) {
		return 0, false
	}
	return d, true
}

// hasMag reports whether a plotted star carries a real estimated magnitude (annotate parks the
// unanchored ones on noMagSentinel = 99).
func hasMag(p annotate.Point) bool { return p.Mag > -30 && p.Mag < 40 }

// resolveDepth decides one star's distance: a measured parallax when the catalogue has one,
// otherwise the photometric estimate, otherwise nothing.
func resolveDepth(p annotate.Point, f colourFit) (float64, DepthSource) {
	if p.Star != nil && p.Star.DistPc > 0 && p.Star.DistPc <= maxDistPc {
		return p.Star.DistPc, DepthMeasured
	}
	if d, ok := f.distance(p); ok {
		return d, DepthEstimated
	}
	return 0, DepthUnknown
}

// gradeHoldout scores the estimate against the stars whose distance is actually known. Each one's
// photometric distance is computed from its colour and magnitude alone — its catalogue distance is
// never an input — so the comparison measures the ladder, not itself.
func gradeHoldout(points []annotate.Point, f colourFit, ph *Photometric) {
	if !f.ok {
		return
	}
	var ratios []float64
	for _, p := range points {
		if p.Star == nil || p.Star.DistPc <= 0 {
			continue
		}
		d, ok := f.distance(p)
		if !ok {
			continue
		}
		ratios = append(ratios, d/p.Star.DistPc)
	}
	if len(ratios) < minCalibPairs {
		return
	}
	sort.Float64s(ratios)
	ph.HoldoutN = len(ratios)
	ph.HoldoutMedianRatio = ratios[len(ratios)/2]
	// Scatter as a half-width in decades: the 16th–84th percentile span of log10(ratio), which is the
	// ±1σ range for a lognormal spread and is not moved by the tail of badly-classified giants.
	lo := math.Log10(ratios[len(ratios)*16/100])
	hi := math.Log10(ratios[len(ratios)*84/100])
	ph.HoldoutScatterDex = (hi - lo) / 2
}
