package postprocess

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

type synthStar struct {
	x, y  int
	amp   float32
	sigma float64
	clip  float32 // >0: clip the profile at this level (saturated flat core)
}

// synthMono builds a mono image with a flat background plus gaussian stars.
func synthMono(w, h int, back float32, stars []synthStar) *fits.Image {
	im := fits.NewImage(w, h, 1)
	pix := im.Pix[0]
	for i := range pix {
		pix[i] = back
	}
	for _, s := range stars {
		r := int(math.Ceil(s.sigma * 4))
		for dy := -r; dy <= r; dy++ {
			for dx := -r; dx <= r; dx++ {
				x, y := s.x+dx, s.y+dy
				if x < 0 || x >= w || y < 0 || y >= h {
					continue
				}
				g := math.Exp(-float64(dx*dx+dy*dy) / (2 * s.sigma * s.sigma))
				v := pix[y*w+x] + s.amp*float32(g)
				if s.clip > 0 && v > s.clip {
					v = s.clip
				}
				pix[y*w+x] = v
			}
		}
	}
	return im
}

func gridStars(w, h, cell, n int, amp float32, sigma float64) []synthStar {
	var stars []synthStar
	for cy := 0; cy < h/cell && len(stars) < n; cy++ {
		for cx := 0; cx < w/cell && len(stars) < n; cx++ {
			stars = append(stars, synthStar{
				x: cx*cell + cell/2 + (cx*7+cy*13)%5, y: cy*cell + cell/2 + (cx*11+cy*5)%5,
				amp: amp, sigma: sigma,
			})
		}
	}
	return stars
}

func TestDetectStarsOpts_ZeroOptionsMatchesLegacyConstants(t *testing.T) {
	im := synthMono(400, 400, 0.05, gridStars(400, 400, 40, 60, 0.5, 1.8))
	got := detectStarsOpts(im.Pix[0], im.W, im.H, StarDetectOptions{})
	want := detectStarsOpts(im.Pix[0], im.W, im.H, StarDetectOptions{
		Sigma:      starCalDetectSigma,
		MaxStars:   starCalMaxStars,
		MinSepPx:   starCalMinSep,
		SatLevel:   starCalSatLevel,
		MaxHalfMax: starCalMaxHalfMax,
	})
	assert.Equal(t, want, got, "zero options must reproduce the starCal* constants")
	assert.Len(t, got, 60)
}

func TestDetectStarPeaks_Options(t *testing.T) {
	count := StarDetectOptions{Sigma: 5, MaxStars: -1, MinSepPx: 6, SatLevel: 2, MaxHalfMax: 40}
	tests := []struct {
		name  string
		im    *fits.Image
		opts  StarDetectOptions
		peaks int
	}{
		{
			name:  "counts every gaussian uncapped",
			im:    synthMono(600, 600, 0.05, gridStars(600, 600, 20, 700, 0.4, 1.5)),
			opts:  count,
			peaks: 700,
		},
		{
			name:  "default cap limits to starCalMaxStars",
			im:    synthMono(600, 600, 0.05, gridStars(600, 600, 20, 700, 0.4, 1.5)),
			opts:  StarDetectOptions{},
			peaks: starCalMaxStars,
		},
		{
			name:  "positive cap honored",
			im:    synthMono(400, 400, 0.05, gridStars(400, 400, 40, 60, 0.4, 1.5)),
			opts:  StarDetectOptions{MaxStars: 10},
			peaks: 10,
		},
		{
			name:  "saturated core excluded by default",
			im:    synthMono(100, 100, 0.05, []synthStar{{x: 50, y: 50, amp: 1.0, sigma: 1.8, clip: 0.95}}),
			opts:  StarDetectOptions{},
			peaks: 0,
		},
		{
			name:  "saturated core kept when SatLevel disabled, one peak per star",
			im:    synthMono(100, 100, 0.05, []synthStar{{x: 50, y: 50, amp: 1.0, sigma: 1.8, clip: 0.95}}),
			opts:  count,
			peaks: 1,
		},
		{
			name:  "wide blob rejected by default half-max width",
			im:    synthMono(200, 200, 0.05, []synthStar{{x: 100, y: 100, amp: 0.5, sigma: 12}}),
			opts:  StarDetectOptions{},
			peaks: 0,
		},
		{
			name:  "wide bright star accepted with relaxed MaxHalfMax",
			im:    synthMono(200, 200, 0.05, []synthStar{{x: 100, y: 100, amp: 0.5, sigma: 12}}),
			opts:  count,
			peaks: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := DetectStarPeaks(tt.im, tt.opts)
			assert.Len(t, got, tt.peaks)
		})
	}
}

func TestDetectStarPeaks_ChannelHandling(t *testing.T) {
	mono := synthMono(120, 120, 0.05, []synthStar{{x: 60, y: 60, amp: 0.5, sigma: 1.8}})
	peaks := DetectStarPeaks(mono, StarDetectOptions{})
	require.Len(t, peaks, 1)
	assert.Equal(t, 60, peaks[0].X)
	assert.Equal(t, 60, peaks[0].Y)

	rgb := fits.NewImage(120, 120, 3)
	for c := 0; c < 3; c++ {
		copy(rgb.Pix[c], mono.Pix[0])
	}
	assert.Len(t, DetectStarPeaks(rgb, StarDetectOptions{}), 1, "luma path")

	assert.Nil(t, DetectStarPeaks(nil, StarDetectOptions{}), "nil image")
	two := fits.NewImage(50, 50, 2)
	assert.Nil(t, DetectStarPeaks(two, StarDetectOptions{}), "unsupported channel count")
}

// --- local-contrast gate -----------------------------------------------------------------------

// nebulaPatch reproduces the situation that inflated the M42 count: most of the frame is dark sky
// with tiny noise, and one corner holds a bright, grainy nebula. The global median/MAD are set by
// the dark majority, so EVERY grain maximum in the bright patch clears the global threshold — while
// none of them stands out against its own neighbourhood.
func nebulaPatch(w, h int, sky, patch float32, seed int) *fits.Image {
	im := fits.NewImage(w, h, 1)
	pix := im.Pix[0]
	r := uint32(seed)
	next := func() float32 { // cheap deterministic LCG in [0,1)
		r = r*1664525 + 1013904223
		return float32(r>>8) / float32(1<<24)
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if x >= w*3/4 {
				pix[y*w+x] = patch + (next()-0.5)*patch*0.35
			} else {
				pix[y*w+x] = sky + (next()-0.5)*sky*0.05
			}
		}
	}
	return im
}

// plant adds a gaussian star in place (the fixture builders take a star list; these go on afterwards).
func plant(im *fits.Image, x, y int, amp float32, sigma float64) {
	r := int(math.Ceil(sigma * 4))
	for dy := -r; dy <= r; dy++ {
		for dx := -r; dx <= r; dx++ {
			px, py := x+dx, y+dy
			if px < 0 || px >= im.W || py < 0 || py >= im.H {
				continue
			}
			g := math.Exp(-float64(dx*dx+dy*dy) / (2 * sigma * sigma))
			im.Pix[0][py*im.W+px] += amp * float32(g)
		}
	}
}

func TestDetectStarPeaks_MinLocalSigma(t *testing.T) {
	base := StarDetectOptions{Sigma: 5, MaxStars: -1, MinSepPx: 6, SatLevel: 2, MaxHalfMax: 40}

	t.Run("zero keeps the detector byte-identical", func(t *testing.T) {
		im := synthMono(300, 300, 0.02, gridStars(300, 300, 40, 20, 0.5, 1.6))
		off := DetectStarPeaks(im, base)
		explicitZero := base
		explicitZero.MinLocalSigma = 0
		assert.Equal(t, off, DetectStarPeaks(im, explicitZero))
	})

	t.Run("suppresses nebula grain without losing the stars inside it", func(t *testing.T) {
		im := nebulaPatch(400, 400, 0.02, 0.35, 7)
		planted := [][2]int{{330, 100}, {350, 220}, {370, 320}}
		for _, s := range planted {
			plant(im, s[0], s[1], 0.6, 1.6)
		}
		// The width filter is switched OFF here on purpose: on this fixture the whole patch sits
		// above half-max, so it would reject the grain by itself and hide what is being tested.
		// With it out of the way, MinLocalSigma is the only thing that can tell grain from stars.
		wide := base
		wide.MaxHalfMax = 1000
		loose := DetectStarPeaks(im, wide)
		gated := wide
		gated.MinLocalSigma = 4
		tight := DetectStarPeaks(im, gated)

		assert.Greater(t, len(loose), 5*len(tight),
			"the global threshold alone is swamped by grain the local test rejects")

		// Every planted star survives the gate — suppressing texture must not cost real detections.
		near := func(peaks []StarPeak, x, y int) bool {
			for _, p := range peaks {
				if (p.X-x)*(p.X-x)+(p.Y-y)*(p.Y-y) <= 9 {
					return true
				}
			}
			return false
		}
		for _, s := range planted {
			assert.True(t, near(loose, s[0], s[1]), "sanity: star at %v is detected at all", s)
			assert.True(t, near(tight, s[0], s[1]), "planted star at %v survives the gate", s)
		}
	})

	t.Run("a flat background cannot be judged by scatter, so stars still pass", func(t *testing.T) {
		// The degenerate case that must never reject: zero MAD in the annulus. Any rise above a
		// perfectly flat surround is unambiguous, not unmeasurable.
		im := synthMono(300, 300, 0.02, gridStars(300, 300, 40, 12, 0.5, 1.6))
		gated := base
		gated.MinLocalSigma = 4
		assert.Len(t, DetectStarPeaks(im, gated), len(DetectStarPeaks(im, base)))
	})
}
