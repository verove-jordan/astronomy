package scene3d

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/annotate"
)

// identityBasis is a scene frame aligned with the equatorial axes, so a velocity's scene components
// can be read directly against hand-computed ICRS ones.
func identityBasis() basis {
	return basis{X: vec3{1, 0, 0}, Y: vec3{0, 1, 0}, Z: vec3{0, 0, 1}, TanHalfW: 0.01, TanHalfH: 0.01}
}

func TestSpaceVelocity_TangentialFromProperMotion(t *testing.T) {
	// A star on the equator at RA 0, 100 pc away, moving 100 mas/yr due East and nothing else.
	// v = 4.74047 × 0.1″/yr × 100 pc = 47.4 km/s, and East at (RA 0, Dec 0) is +y in ICRS.
	info := &annotate.StarInfo{RADeg: 0, DecDeg: 0, DistPc: 100, PMRA: 100}
	v, ok := spaceVelocity(info, identityBasis())
	require.True(t, ok)
	assert.InDelta(t, 47.4047, v.length(), 0.01, "tangential speed")
	assert.InDelta(t, 0, v.X, 1e-9)
	assert.InDelta(t, 47.4047, v.Y, 0.01, "East is +y at RA 0 on the equator")
	assert.InDelta(t, 0, v.Z, 1e-9)
}

func TestSpaceVelocity_ScalesWithDistance(t *testing.T) {
	// The same angular motion at ten times the distance is ten times the speed — which is exactly why
	// proper motion alone is not a velocity.
	near := &annotate.StarInfo{RADeg: 0, DecDeg: 0, DistPc: 100, PMRA: 100}
	far := &annotate.StarInfo{RADeg: 0, DecDeg: 0, DistPc: 1000, PMRA: 100}
	vn, ok1 := spaceVelocity(near, identityBasis())
	vf, ok2 := spaceVelocity(far, identityBasis())
	require.True(t, ok1)
	require.True(t, ok2)
	assert.InDelta(t, 10, vf.length()/vn.length(), 1e-9)
}

func TestSpaceVelocity_RadialRunsAlongTheLineOfSight(t *testing.T) {
	rv := 30.0
	info := &annotate.StarInfo{RADeg: 0, DecDeg: 0, DistPc: 100, RVKmS: &rv}
	v, ok := spaceVelocity(info, identityBasis())
	require.True(t, ok)
	// At RA 0, Dec 0 the line of sight is +x, and a positive radial velocity recedes along it.
	assert.InDelta(t, 30, v.X, 1e-9)
	assert.InDelta(t, 0, v.Y, 1e-9)
	assert.InDelta(t, 0, v.Z, 1e-9)
}

func TestSpaceVelocity_NorthAtThePole(t *testing.T) {
	// North at Dec +90 points back down the −x axis for a star at RA 0 — the check that the local
	// triad is built the right way round rather than merely being orthogonal.
	info := &annotate.StarInfo{RADeg: 0, DecDeg: 90, DistPc: 100, PMDec: 100}
	v, ok := spaceVelocity(info, identityBasis())
	require.True(t, ok)
	assert.InDelta(t, -47.4047, v.X, 0.01)
}

func TestSpaceVelocity_CombinesBothParts(t *testing.T) {
	rv := 40.0
	info := &annotate.StarInfo{RADeg: 0, DecDeg: 0, DistPc: 100, PMRA: 100, RVKmS: &rv}
	v, ok := spaceVelocity(info, identityBasis())
	require.True(t, ok)
	// Tangential and radial are perpendicular, so the speed is the hypotenuse.
	assert.InDelta(t, math.Hypot(47.4047, 40), v.length(), 0.01)
}

func TestSpaceVelocity_RefusesWhatItCannotCompute(t *testing.T) {
	tests := []struct {
		name string
		info *annotate.StarInfo
	}{
		{"no catalogue entry", nil},
		{"no distance — an angle per year is not a speed", &annotate.StarInfo{PMRA: 100}},
		{"nothing measured at all", &annotate.StarInfo{DistPc: 100}},
		{"a speed no star reaches", &annotate.StarInfo{DistPc: 100000, PMRA: 100000}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := spaceVelocity(tt.info, identityBasis())
			assert.False(t, ok)
		})
	}
}

func TestSpaceVelocity_RotatesIntoTheSceneBasis(t *testing.T) {
	// The same star through a rotated frame must keep its SPEED and only change components — the
	// property that stops an arrow pointing somewhere the star is not.
	rv := 25.0
	info := &annotate.StarInfo{RADeg: 83.8, DecDeg: -5.4, DistPc: 400, PMRA: 12, PMDec: -7, RVKmS: &rv}
	plain, ok := spaceVelocity(info, identityBasis())
	require.True(t, ok)

	cam := rotatedCamera(83.8, -5.4, 33, false, 2400, 1800)
	b, err := newBasis(cam.frame())
	require.NoError(t, err)
	rotated, ok := spaceVelocity(info, b)
	require.True(t, ok)

	assert.InDelta(t, plain.length(), rotated.length(), 1e-9, "a rotation cannot change a speed")
	// The star sits at the field centre, so its radial velocity must land almost entirely on +Z.
	assert.InDelta(t, 25, rotated.Z, 0.5, "receding means moving away down the optical axis")
}

// --- colour ----------------------------------------------------------------------------------------

func TestBlackbodyRGB_KnownStars(t *testing.T) {
	tests := []struct {
		name            string
		kelvin          float64
		wantRedOverBlue string // "warmer" = R > B, "cooler" = B > R, "neutral" = within 8%
	}{
		{"Betelgeuse-class M star", 3600, "warmer"},
		{"K star", 4500, "warmer"},
		{"the Sun", 5772, "neutral"},
		{"Vega-class A star", 9600, "cooler"},
		{"Rigel-class B star", 12100, "cooler"},
		{"O star", 30000, "cooler"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, _, b := blackbodyRGB(tt.kelvin)
			ratio := float64(r) / float64(b)
			switch tt.wantRedOverBlue {
			case "warmer":
				assert.Greater(t, ratio, 1.08, "#%02x%02x%02x should read warm", r, 0, b)
			case "cooler":
				assert.Less(t, ratio, 0.92, "should read cool")
			default:
				assert.InDelta(t, 1, ratio, 0.12, "the Sun is very nearly white")
			}
		})
	}
}

// TestBlackbodyRGB_HueOnly pins that the colour carries no brightness: how bright a star is drawn
// comes from its magnitude and its distance from the camera, and folding luminosity in here as well
// would count it twice.
func TestBlackbodyRGB_HueOnly(t *testing.T) {
	for _, k := range []float64{3000, 5772, 20000} {
		r, g, b := blackbodyRGB(k)
		assert.Equal(t, uint8(255), max3(r, g, b), "%.0f K must be normalised to full brightness", k)
	}
}

func max3(a, b, c uint8) uint8 {
	m := a
	if b > m {
		m = b
	}
	if c > m {
		m = c
	}
	return m
}

func TestBVToTemperature(t *testing.T) {
	// Ballesteros' formula against stars whose temperature is well known.
	tests := []struct {
		name   string
		bv     float64
		wantK  float64
		within float64
	}{
		{"the Sun (B−V 0.63, 5772 K)", 0.63, 5772, 150},
		{"Vega (B−V 0.00, 9600 K)", 0.00, 9600, 700},
		{"Betelgeuse (B−V 1.85, 3600 K)", 1.85, 3600, 400},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := bvToTemperatureK(tt.bv)
			require.True(t, ok)
			assert.InDelta(t, tt.wantK, got, tt.within)
		})
	}
	t.Run("refuses colours outside the fit", func(t *testing.T) {
		for _, bv := range []float64{-2.0, 5.0} {
			_, ok := bvToTemperatureK(bv)
			assert.False(t, ok, "B−V %v is outside the relation's range", bv)
		}
	})
}

func TestStarColour_PrefersTheCatalogue(t *testing.T) {
	blue := -0.2
	// A catalogued B−V wins over the sampled pixel, even when the pixel says otherwise: the pixel
	// carries the stack's colour balance and annotate's lift toward white, the catalogue does not.
	r, _, b, physical := starColour("#ffaa77", &blue, colourFit{})
	assert.True(t, physical)
	assert.Less(t, r, b, "a B−V of −0.2 is a hot blue star whatever the pixel looked like")
}

func TestStarColour_FallsBackWithoutPanicking(t *testing.T) {
	r, g, b, physical := starColour("", nil, colourFit{})
	assert.False(t, physical)
	assert.Equal(t, [3]uint8{255, 255, 255}, [3]uint8{r, g, b}, "nothing known → white, not black")
}
