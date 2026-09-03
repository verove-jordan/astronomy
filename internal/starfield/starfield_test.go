package starfield

import (
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// synth paints elliptical Gaussians onto a noisy background.
type synthStar struct {
	x, y, flux, sigmaMajor, sigmaMinor, paDeg float64
}

func synth(w, h int, bg, noise float64, stars []synthStar) []float32 {
	p := make([]float32, w*h)
	rng := rand.New(rand.NewSource(7))
	for i := range p {
		p[i] = float32(bg + rng.NormFloat64()*noise)
	}
	for _, s := range stars {
		pa := s.paDeg * math.Pi / 180
		cos, sin := math.Cos(pa), math.Sin(pa)
		norm := s.flux / (2 * math.Pi * s.sigmaMajor * s.sigmaMinor)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				dx, dy := float64(x)-s.x, float64(y)-s.y
				// Rotate into the star's own frame, so sigmaMajor lies along the position angle.
				u := dx*cos + dy*sin
				v := -dx*sin + dy*cos
				e := (u*u)/(2*s.sigmaMajor*s.sigmaMajor) + (v*v)/(2*s.sigmaMinor*s.sigmaMinor)
				if e < 20 {
					p[y*w+x] += float32(norm * math.Exp(-e))
				}
			}
		}
	}
	return p
}

func TestDetect_FindsStars(t *testing.T) {
	const w, h = 200, 200
	want := []synthStar{
		{x: 50.3, y: 40.7, flux: 30000, sigmaMajor: 2, sigmaMinor: 2},
		{x: 120.5, y: 90.2, flux: 60000, sigmaMajor: 2, sigmaMinor: 2},
		{x: 160.1, y: 150.9, flux: 15000, sigmaMajor: 2, sigmaMinor: 2},
	}
	plane := synth(w, h, 100, 3, want)

	got := Detect(plane, w, h, DefaultOptions())

	require.Len(t, got, len(want), "one detection per star, no duplicates from its own shoulders")
	// Brightest first.
	assert.InDelta(t, 120.5, got[0].X, 0.15)
	assert.InDelta(t, 90.2, got[0].Y, 0.15)
	assert.Greater(t, got[0].Flux, got[1].Flux)
	assert.Greater(t, got[1].Flux, got[2].Flux)
	for _, s := range got {
		assert.InDelta(t, 1.0, s.Elongation, 0.15, "a round star must measure round at this SNR")
	}
}

// TestDetect_MeasuresTrailing is the measurement the untracked-frame work needs: a star smeared
// along one direction has to report both how much and which way.
func TestDetect_MeasuresTrailing(t *testing.T) {
	tests := []struct {
		name                   string
		sigmaMajor, sigmaMinor float64
		paDeg                  float64
	}{
		{"round", 2, 2, 0},
		{"trailed along x", 4, 2, 0},
		{"trailed along y", 4, 2, 90},
		{"trailed diagonally", 4, 2, 45},
		{"trailed at 135", 4, 2, 135},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const w, h = 120, 120
			plane := synth(w, h, 100, 2, []synthStar{
				{x: 60, y: 60, flux: 80000, sigmaMajor: tt.sigmaMajor, sigmaMinor: tt.sigmaMinor, paDeg: tt.paDeg},
			})
			o := DefaultOptions()
			o.BoxRadius = 12 // wide enough to hold the whole trail, or the shape reads too round

			got := Detect(plane, w, h, o)

			require.Len(t, got, 1)
			assert.InDelta(t, tt.sigmaMajor/tt.sigmaMinor, got[0].Elongation, 0.15, "elongation")
			if tt.sigmaMajor == tt.sigmaMinor {
				return // position angle is meaningless for a round star
			}
			// Compare as an axis, where 179 degrees and 1 degree are two degrees apart.
			diff := math.Mod(math.Abs(got[0].PADeg-tt.paDeg), 180)
			assert.Less(t, math.Min(diff, 180-diff), 5.0, "position angle, got %.1f want %.1f", got[0].PADeg, tt.paDeg)
		})
	}
}

func TestDetect_Guards(t *testing.T) {
	tests := []struct {
		name  string
		plane []float32
		w, h  int
		o     Options
	}{
		{"empty plane", nil, 0, 0, DefaultOptions()},
		{"size mismatch", make([]float32, 10), 5, 5, DefaultOptions()},
		{"zero box radius", make([]float32, 100), 10, 10, Options{Sigma: 5}},
		{"flat plane has no noise to threshold against", make([]float32, 10000), 100, 100, DefaultOptions()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Empty(t, Detect(tt.plane, tt.w, tt.h, tt.o))
		})
	}
}

func TestDetect_MaxCapsBrightestFirst(t *testing.T) {
	const w, h = 200, 200
	plane := synth(w, h, 100, 3, []synthStar{
		{x: 40, y: 40, flux: 8000, sigmaMajor: 2, sigmaMinor: 2},
		{x: 100, y: 100, flux: 40000, sigmaMajor: 2, sigmaMinor: 2},
		{x: 160, y: 160, flux: 20000, sigmaMajor: 2, sigmaMinor: 2},
	})
	o := DefaultOptions()
	o.Max = 2

	got := Detect(plane, w, h, o)

	require.Len(t, got, 2)
	assert.InDelta(t, 100, got[0].X, 0.3, "brightest kept first")
	assert.InDelta(t, 160, got[1].X, 0.3)
}

func TestBackground(t *testing.T) {
	plane := synth(400, 400, 250, 8, nil)

	bg, noise := Background(plane)

	assert.InDelta(t, 250, bg, 1.0)
	assert.InDelta(t, 8, noise, 1.0)
}

// TestDetect_FaintStarReportsUnmeasuredShape pins the honest failure. Second moments need signal far
// from the centre, where the noise is loudest, so shape degrades long before position or flux do. A
// star too faint to measure must report Elongation 0 — "I could not tell" — and never 1.0, which
// would read as a confident "perfectly round".
func TestDetect_FaintStarReportsUnmeasuredShape(t *testing.T) {
	const w, h = 120, 120
	plane := synth(w, h, 100, 3, []synthStar{{x: 60, y: 60, flux: 1500, sigmaMajor: 4, sigmaMinor: 2}})
	o := DefaultOptions()
	o.BoxRadius = 12

	got := Detect(plane, w, h, o)

	require.Len(t, got, 1, "it is still detected and still centroided")
	assert.InDelta(t, 60, got[0].X, 1.0, "position survives where shape does not")
	assert.Zero(t, got[0].Elongation, "shape must report unmeasured, not round")
	assert.Zero(t, got[0].FWHM)
}
