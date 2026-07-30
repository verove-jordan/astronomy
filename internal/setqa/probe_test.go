package setqa

import (
	"math"
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// synthPlane fills one channel with bg + gaussian noise + an optional spatial term.
func synthPlane(pix []float32, w, h int, bg, noise float64, seed int64, mod func(x, y int) float64) {
	rng := rand.New(rand.NewSource(seed))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := bg + noise*rng.NormFloat64()
			if mod != nil {
				v += mod(x, y)
			}
			pix[y*w+x] = float32(v)
		}
	}
}

func synthImage(w, h int, bg, noise float64, seed int64, mod func(x, y int) float64) *fits.Image {
	im := fits.NewImage(w, h, 1)
	synthPlane(im.Pix[0], w, h, bg, noise, seed, mod)
	return im
}

const (
	testW = 256
	testH = 192
)

func leftHalo(amp, scale float64) func(x, y int) float64 {
	return func(x, y int) float64 { return amp * math.Exp(-float64(x)/scale) }
}

func TestMeasureImage_Scenarios(t *testing.T) {
	cases := []struct {
		name   string
		mod    func(x, y int) float64
		verify func(t *testing.T, cp ChannelProbe)
	}{
		{
			"flat noise stays quiet",
			nil,
			func(t *testing.T, cp ChannelProbe) {
				assert.Less(t, cp.BorderSigma, 2.0)
				assert.Less(t, cp.GradSigma, 3.0)
			},
		},
		{
			"left halo flags the left border",
			leftHalo(0.05, 40),
			func(t *testing.T, cp ChannelProbe) {
				assert.Equal(t, "left", cp.WorstBorder)
				assert.Greater(t, cp.BorderSigma, 6.0)
				assert.Greater(t, cp.BorderPct, 8.0)
			},
		},
		{
			"vignetting (all borders darker) is not a halo",
			func(x, y int) float64 {
				dx := float64(x)/testW - 0.5
				dy := float64(y)/testH - 0.5
				return -0.03 * (dx*dx + dy*dy) * 4
			},
			func(t *testing.T, cp ChannelProbe) { assert.Less(t, cp.BorderSigma, 1.0) },
		},
		{
			"symmetric two-sided glow cancels",
			func(x, y int) float64 {
				return 0.03 * (math.Exp(-float64(x)/40) + math.Exp(-float64(testW-1-x)/40))
			},
			func(t *testing.T, cp ChannelProbe) { assert.Less(t, cp.BorderSigma, 3.0) },
		},
		{
			"linear ramp reads as a strong gradient",
			func(x, y int) float64 { return 0.04 * float64(x) / testW },
			func(t *testing.T, cp ChannelProbe) {
				assert.Greater(t, cp.GradSigma, 6.0)
				assert.Greater(t, cp.GradPct, 20.0)
			},
		},
		{
			"sprinkled stars do not move the P30 tiles",
			func(x, y int) float64 {
				if (x*31+y*17)%97 == 0 { // ~1% of pixels saturated
					return 0.9
				}
				return 0
			},
			func(t *testing.T, cp ChannelProbe) {
				assert.Less(t, cp.BorderSigma, 2.0)
				assert.InDelta(t, 0.10, cp.Background, 0.01)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			im := synthImage(testW, testH, 0.10, 0.002, 42, tc.mod)
			probe := MeasureImage(im)
			assert.Len(t, probe.Channels, 1)
			tc.verify(t, probe.Channels[0])
		})
	}
}

func TestMeasureImage_OSCRedOnlyHalo(t *testing.T) {
	im := fits.NewImage(testW, testH, 3)
	synthPlane(im.Pix[0], testW, testH, 0.10, 0.002, 1, leftHalo(0.05, 40))
	synthPlane(im.Pix[1], testW, testH, 0.10, 0.002, 2, nil)
	synthPlane(im.Pix[2], testW, testH, 0.10, 0.002, 3, nil)

	probe := MeasureImage(im)
	assert.Len(t, probe.Channels, 3)
	assert.Equal(t, "R", probe.Channels[0].Channel)
	assert.Greater(t, probe.Channels[0].BorderSigma, 6.0)
	assert.Less(t, probe.Channels[1].BorderSigma, 2.0)
	assert.Less(t, probe.Channels[2].BorderSigma, 2.0)
}
