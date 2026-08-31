package solar

import (
	"math"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// prominence.go measures how much chromosphere a frame shows OFF the solar limb.
//
// It exists because sharpness cannot answer the question a prominence shot asks. Prominences are not
// a property of the optics, they are a property of the MOMENT: a flame stands off one part of the
// limb for a few minutes and is simply not there in a frame taken from another part of the arc, or
// on the far side of the Moon's edge. Ranking by resolution picks the crispest crescent, which is
// very often the one with nothing standing off it — which is exactly what happened on the first
// render of the 12 Aug 2026 session, where the sharpest frame at maximum showed none at all while
// frames a minute either side showed a bright knot at the lower cusp.
//
// So the shot is chosen by looking for what it is a shot OF.

const (
	// promInnerR and promOuterR bound the annulus searched, in solar radii. Prominences reach a few
	// percent of the radius; going further collects only sky and scattered light.
	promInnerR = 1.005
	promOuterR = 1.100
	// promOcculterGuard grows the occulter before it is excluded, in solar radii. The lunar limb is
	// a bright edge in its own right and would otherwise be counted as chromosphere.
	promOcculterGuard = 0.008
)

// ProminenceSignal is the mean brightness in the annulus just outside the solar limb, over the disc's
// own level — a dimensionless number comparable between frames of different exposure.
//
// Pixels inside the occulter are excluded, because what stands behind the Moon is not visible and a
// mis-fitted lunar edge would otherwise dominate the score. ok=false means the annulus was empty,
// which happens when the crescent runs off the frame.
func ProminenceSignal(im *fits.Image, g Pair) (float64, bool) {
	if im == nil || len(im.Pix) == 0 || g.Sun.R <= 0 {
		return 0, false
	}
	level := crescentLevel(im, g)
	if level <= 0 {
		return 0, false
	}
	// Per SECTOR, not over the whole annulus. A prominence is a localised thing — one flame over a
	// few degrees of limb — and a mean around the entire annulus divides it by the ninety percent of
	// the ring that is empty sky, which is how a frame with an obvious flame scores the same as one
	// with none. Taking the brightest sector asks the question actually being asked: is there
	// anything standing off this limb, anywhere.
	sums := make([]float64, promSectors)
	counts := make([]int, promSectors)
	inner2 := (promInnerR * g.Sun.R) * (promInnerR * g.Sun.R)
	outer2 := (promOuterR * g.Sun.R) * (promOuterR * g.Sun.R)
	guard := g.Moon.R + promOcculterGuard*g.Sun.R
	guard2 := guard * guard

	p := im.Pix[0]
	total := 0
	for y := 0; y < im.H; y++ {
		dy := float64(y) - g.Sun.CY
		for x := 0; x < im.W; x++ {
			dx := float64(x) - g.Sun.CX
			d2 := dx*dx + dy*dy
			if d2 < inner2 || d2 > outer2 {
				continue
			}
			if g.Moon.R > 0 {
				mx, my := float64(x)-g.Moon.CX, float64(y)-g.Moon.CY
				if mx*mx+my*my <= guard2 {
					continue
				}
			}
			sec := int((math.Atan2(dy, dx)/(2*math.Pi) + 0.5) * promSectors)
			if sec < 0 {
				sec = 0
			}
			if sec >= promSectors {
				sec = promSectors - 1
			}
			sums[sec] += float64(p[y*im.W+x])
			counts[sec]++
			total++
		}
	}
	if total < promMinPixels {
		return 0, false
	}
	means := make([]float64, 0, promSectors)
	for i := range sums {
		if counts[i] >= promMinSectorPixels {
			means = append(means, sums[i]/float64(counts[i]))
		}
	}
	if len(means) < promMinSectors {
		return 0, false
	}
	sort.Float64s(means)
	// The 90th percentile rather than the maximum: one hot sector can be a cosmic ray, a bright
	// lunar edge the guard did not quite cover, or the internal reflection clipping the annulus.
	return means[int(0.90*float64(len(means)-1))] / level, true
}

// Sector bounds for the prominence measurement.
//
// The counts are small on purpose. Deep in an eclipse the Moon is the LARGER disc and nearly
// concentric, so almost the whole annulus just outside the solar limb is behind it: the only place
// anything can stand off the limb and be seen is within a few degrees of the cusps. That is exactly
// where the flames in this session appear, and thresholds sized for a full disc reject every frame.
const (
	promSectors         = 72
	promMinSectorPixels = 6
	promMinSectors      = 3
)

// promMinPixels is the smallest annulus worth believing.
const promMinPixels = 120

// crescentLevel is what the visible Sun renders at, on a frame where it may be a sliver.
//
// The obvious answer — the median over the visible-Sun mask — returns exactly ZERO past about
// seventy percent obscuration, because the mask is eroded from a crescent narrower than the erosion.
// That failure is silent and it is documented elsewhere in this package; here it made every deep
// frame report "no chromosphere" rather than a number. A high percentile of the frame itself is
// crude by comparison and cannot fail: whatever else is in the picture, the brightest half a percent
// of it is the crescent.
func CrescentLevel(im *fits.Image, g Pair) float64 { return crescentLevel(im, g) }

func crescentLevel(im *fits.Image, g Pair) float64 {
	if masked := MaskedMedian(im.Pix[0], g.VisibleSunMask(im.W, im.H, 0)); masked > 0 {
		return masked
	}
	return float64(imgops.Percentile(imgops.Subsample(im.Pix[0], 50000), 99.5))
}
