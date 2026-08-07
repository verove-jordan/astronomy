package scene3d

import (
	"fmt"
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/annotate"
)

// renderHex reproduces annotate's starHex: the per-channel lift toward white that colourProxy has to
// invert. Copied deliberately rather than imported — it is unexported there, and pinning the
// contract in a test is what catches the two drifting apart.
func renderHex(r, g, b float64) string {
	max := math.Max(r, math.Max(g, b))
	if max <= 0 {
		return ""
	}
	scale := func(v float64) int {
		u := hexFloor + (1-hexFloor)*(v/max)
		return int(math.Round(math.Min(1, math.Max(0, u)) * 255))
	}
	return fmt.Sprintf("#%02x%02x%02x", scale(r), scale(g), scale(b))
}

func TestColourProxy_InvertsTheRenderedHex(t *testing.T) {
	tests := []struct {
		name    string
		r, g, b float64
		want    float64
	}{
		{"hot blue star", 0.4, 0.7, 1.0, 2.5 * math.Log10(0.4/1.0)},
		{"solar", 1.0, 0.95, 0.85, 2.5 * math.Log10(1.0/0.85)},
		{"cool red star", 1.0, 0.6, 0.3, 2.5 * math.Log10(1.0/0.3)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := colourProxy(renderHex(tt.r, tt.g, tt.b))
			require.True(t, ok)
			// The tolerance is the 8-bit quantisation the hex round trip costs, not slack in the maths.
			assert.InDelta(t, tt.want, got, 0.03)
		})
	}
}

func TestColourProxy_RejectsColourlessInput(t *testing.T) {
	for _, hex := range []string{"", "#ffffff", "#808080", "not-a-colour", "#12345"} {
		_, ok := colourProxy(hex)
		assert.False(t, ok, "a mono or malformed colour must not calibrate: %q", hex)
	}
}

func TestZamsAbsMag(t *testing.T) {
	tests := []struct {
		name string
		ci   float64
		want float64
		ok   bool
	}{
		{"exact table entry (the Sun)", 0.63, 4.83, true},
		{"interpolated", 0.615, (4.40 + 4.83) / 2, true},
		{"bluest tabulated", -0.33, -5.70, true},
		{"reddest tabulated", 1.61, 12.30, true},
		{"bluer than any main-sequence star", -0.6, 0, false},
		{"redder than any main-sequence star", 2.5, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := zamsAbsMag(tt.ci)
			require.Equal(t, tt.ok, ok)
			if tt.ok {
				assert.InDelta(t, tt.want, got, 0.3)
			}
		})
	}
}

// syntheticField builds a star field where the truth is known: each star gets a real distance, a
// real B−V, the apparent magnitude those two imply, and the colour the finish would have rendered.
// `identified` fixes how many carry a catalogue entry.
func syntheticField(t *testing.T, n, identified int, seed int64) []annotate.Point {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	out := make([]annotate.Point, 0, n)
	for i := 0; i < n; i++ {
		ci := -0.2 + rng.Float64()*1.6
		absMag, ok := zamsAbsMag(ci)
		if !ok {
			continue
		}
		distPc := math.Pow(10, 1.5+rng.Float64()*2) // 32 pc … 3.2 kpc
		// The frame's rendered colour tracks B−V through some arbitrary instrumental relation; the
		// calibration's whole job is to discover it, so the test must not use the identity.
		const gain, offset = 0.72, 0.31
		h := (ci - offset) / gain
		red := math.Min(1, math.Pow(10, h/2.5)*0.35)
		blue := 0.35
		p := annotate.Point{
			X: rng.Intn(3720), Y: rng.Intn(2790),
			Mag: absMag + 5*math.Log10(distPc) - 5,
			Hex: renderHex(red, (red+blue)/2, blue),
		}
		if i < identified {
			ciCopy, absCopy := ci, absMag
			p.Star = &annotate.StarInfo{
				Name: fmt.Sprintf("TYC 1-%d-1", i), DistPc: distPc, CI: &ciCopy, AbsMag: &absCopy,
			}
		}
		out = append(out, p)
	}
	return out
}

func TestFitColour_DiscoversTheFramesOwnRelation(t *testing.T) {
	points := syntheticField(t, 400, 200, 1)
	fit, ph := fitColour(points)

	require.True(t, ph.Calibrated, "reason: %s", ph.Reason)
	require.True(t, fit.ok)
	assert.Greater(t, ph.Pairs, minCalibPairs)
	// The synthetic relation is ci = 0.72·h + 0.31; the fit has to find it, not assume it.
	assert.InDelta(t, 0.72, fit.slope, 0.05, "slope")
	assert.InDelta(t, 0.31, fit.intercept, 0.05, "intercept")
	assert.Less(t, ph.RMS, maxCalibRMS)
}

func TestFitColour_RefusesWhatItCannotCalibrate(t *testing.T) {
	t.Run("too few identified stars", func(t *testing.T) {
		_, ph := fitColour(syntheticField(t, 400, 5, 2))
		assert.False(t, ph.Calibrated)
		assert.NotEmpty(t, ph.Reason)
	})
	t.Run("colours that do not track the catalogue", func(t *testing.T) {
		points := syntheticField(t, 400, 300, 3)
		rng := rand.New(rand.NewSource(4))
		for i := range points {
			if points[i].Star != nil {
				scrambled := -0.2 + rng.Float64()*1.6 // colour and catalogue now unrelated
				points[i].Star.CI = &scrambled
			}
		}
		_, ph := fitColour(points)
		assert.False(t, ph.Calibrated)
		assert.NotEmpty(t, ph.Reason)
	})
	t.Run("a mono stack has no colour at all", func(t *testing.T) {
		points := syntheticField(t, 400, 300, 5)
		for i := range points {
			points[i].Hex = "#ffffff"
		}
		_, ph := fitColour(points)
		assert.False(t, ph.Calibrated)
	})
}

// TestPhotometricDistance_HoldoutQualityGate is the gate the plan puts the whole estimated layer
// behind: distances derived from colour and brightness alone must recover the distances the
// catalogue measured. The estimate is computed without ever consulting the star's own parallax, so
// this measures the ladder rather than itself.
func TestPhotometricDistance_HoldoutQualityGate(t *testing.T) {
	points := syntheticField(t, 600, 400, 7)
	fit, ph := fitColour(points)
	require.True(t, ph.Calibrated)
	gradeHoldout(points, fit, &ph)

	require.Greater(t, ph.HoldoutN, minCalibPairs)
	assert.InDelta(t, 1.0, ph.HoldoutMedianRatio, 0.25, "estimated distances must land on the measured ones")
	assert.Less(t, ph.HoldoutScatterDex, 0.2, "spread of a single estimate, in decades")
}

func TestResolveDepth_PrefersAMeasuredParallax(t *testing.T) {
	points := syntheticField(t, 400, 200, 11)
	fit, ph := fitColour(points)
	require.True(t, ph.Calibrated)

	var measured, estimated, unknown int
	for _, p := range points {
		d, src := resolveDepth(p, fit)
		switch src {
		case DepthMeasured:
			measured++
			// A measured star must carry the catalogue's own number, never the estimate.
			assert.Equal(t, p.Star.DistPc, d)
		case DepthEstimated:
			estimated++
			assert.Greater(t, d, 0.0)
			assert.Nil(t, p.Star)
		default:
			unknown++
		}
	}
	assert.Equal(t, 200, measured)
	assert.Greater(t, estimated, 0)
	assert.Equal(t, len(points), measured+estimated+unknown)
}

func TestResolveDepth_WithoutCalibrationOnlyTheCatalogueCounts(t *testing.T) {
	points := syntheticField(t, 100, 50, 13)
	for _, p := range points {
		d, src := resolveDepth(p, colourFit{})
		if p.Star != nil {
			assert.Equal(t, DepthMeasured, src)
			assert.Equal(t, p.Star.DistPc, d)
			continue
		}
		assert.Equal(t, DepthUnknown, src, "an uncalibrated frame must not invent a distance")
	}
}

func TestTableDistance(t *testing.T) {
	tests := []struct {
		name   string
		names  []string
		wantPc float64
	}{
		{"Messier primary", []string{"M42"}, 412},
		{"resolved through an alias", []string{"NGC1976"}, 412},
		{"normalised spacing", []string{"M 42"}, 412},
		{"the Pleiades are close", []string{"M45"}, 136},
		{"M81 and M82 are separately catalogued", []string{"M82"}, 3526000},
		{"unknown object", []string{"NGC9999999"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tableDistance(tt.names...)
			if tt.wantPc == 0 {
				assert.Zero(t, got)
				return
			}
			assert.InDelta(t, tt.wantPc, got, tt.wantPc*0.02)
		})
	}
}

// M81 and M82 are the pair the 3D view exists to separate: about 90 kpc apart at 3.5 Mpc, which is
// invisible in the photograph and obvious in depth.
func TestTableDistance_SeparatesTheBodePair(t *testing.T) {
	m81, m82 := tableDistance("M81"), tableDistance("M82")
	require.Greater(t, m81, 0.0)
	require.Greater(t, m82, 0.0)
	assert.Greater(t, m81-m82, 50_000.0, "the two must not collapse onto one plane")
}
