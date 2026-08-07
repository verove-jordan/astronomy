package solar

import (
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/imgops"
)

// TestNoiseFloor_Live measures the per-frame noise ON THE DISC, where FrameSharpness cannot see it.
//
// FrameSharpness calibrates itself by measuring band-pass energy on the sky outside the limb. That
// is only valid if the noise there is the same as the noise on the disc. For a raw sensor it very
// nearly is. For a VIDEO CODEC it is not, and the difference is enormous: HEVC spends its bits where
// there is signal, so its quantisation error is concentrated exactly on the bright, textured disc
// and is almost absent from the flat dark sky — and it is structured at the 2-8 px block scale,
// which is precisely the band this metric integrates.
//
// If that is happening, a single frame's "detail" is substantially codec noise, it averages away
// over a stack like any other independent noise, and the resulting drop is read as the stack losing
// detail when the stack is in fact converging on the truth.
//
// The measurement uses time: two frames 8 ms apart show the same Sun, so their difference is noise
// (plus a little seeing). Halving that difference's band energy gives the per-frame on-disc noise
// directly, with no model of the codec required.
//
//	ASTRO_SOLAR_FRAMES=<dir of ingested *.fits> go test ./internal/solar -run NoiseFloor -v
func TestNoiseFloor_Live(t *testing.T) {
	dir := os.Getenv("ASTRO_SOLAR_FRAMES")
	if dir == "" {
		t.Skip("set ASTRO_SOLAR_FRAMES=<dir of ingested frames>")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.fits"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	sort.Strings(paths)

	idxRe := regexp.MustCompile(`_(\d+)\.fits$`)
	type item struct {
		path string
		idx  int
	}
	var items []item
	for _, p := range paths {
		m := idxRe.FindStringSubmatch(p)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		items = append(items, item{p, n})
	}
	sort.Slice(items, func(a, b int) bool { return items[a].idx < items[b].idx })

	// Pairs close enough in time that the Sun itself has not changed.
	type pair struct{ a, b string }
	var pairs []pair
	for i := 0; i+1 < len(items) && len(pairs) < 12; i++ {
		if d := items[i+1].idx - items[i].idx; d >= 1 && d <= 3 {
			pairs = append(pairs, pair{items[i].path, items[i+1].path})
			i++
		}
	}
	require.NotEmpty(t, pairs, "no temporally adjacent frames to difference")

	var skySum, discSum, totalSum, medSum float64
	for _, pr := range pairs {
		ia, err := fits.ReadImage(pr.a)
		require.NoError(t, err)
		ib, err := fits.ReadImage(pr.b)
		require.NoError(t, err)
		a, b := firstPlane(ia), firstPlane(ib)
		la, ok := FitLimb(a)
		require.True(t, ok)
		lb, ok := FitLimb(b)
		require.True(t, ok)

		// Put b on a's geometry, so the difference is noise rather than the disc's drift.
		bw, _ := warpCovered(b, Transform{Scale: la.R / lb.R, CX: lb.CX, CY: lb.CY}, a.W, 1, nil)
		aw, _ := warpCovered(a, Transform{Scale: 1, CX: la.CX, CY: la.CY}, a.W, 1, nil)
		half := float64(a.W-1) / 2
		l := Limb{CX: half, CY: half, R: la.R}

		diff := fits.NewImage(a.W, a.W, 1)
		for i := range diff.Pix[0] {
			diff.Pix[0][i] = aw.Pix[0][i] - bw.Pix[0][i]
		}
		// Half the difference's energy is one frame's worth: var(a-b) = var(a) + var(b) = 2·var.
		discSum += 0.5 * bandOnDisc(diff, l)
		skySum += bandEnergy(imgops.GaussianBlur(aw.Pix[0], aw.W, aw.H, bandInner),
			imgops.GaussianBlur(aw.Pix[0], aw.W, aw.H, bandOuter), aw.W, aw.H, l, 1.15, 1.45)
		totalSum += bandOnDisc(aw, l)
		medSum += discMedian(aw, l)
	}
	n := float64(len(pairs))
	sky, disc, total, med := skySum/n, discSum/n, totalSum/n, medSum/n

	t.Logf("band-pass energy over %d adjacent pairs (mean):", len(pairs))
	t.Logf("  total on disc          %.3e", total)
	t.Logf("  noise floor, sky       %.3e   <- what FrameSharpness subtracts today", sky)
	t.Logf("  noise floor, on disc   %.3e   <- what is actually there", disc)
	t.Logf("  on-disc noise is %.1fx the sky estimate", disc/math.Max(sky, 1e-30))
	t.Logf("detail after subtracting:")
	t.Logf("  the sky floor      %.5f", math.Sqrt(math.Max(total-sky, 0))/med)
	t.Logf("  the on-disc floor  %.5f   <- the honest single-frame figure", math.Sqrt(math.Max(total-disc, 0))/med)
}

// bandOnDisc is the mean squared band-pass response inside the quality radius.
func bandOnDisc(im *fits.Image, l Limb) float64 {
	inner := imgops.GaussianBlur(im.Pix[0], im.W, im.H, bandInner)
	outer := imgops.GaussianBlur(im.Pix[0], im.W, im.H, bandOuter)
	r2 := (qualityRadius * l.R) * (qualityRadius * l.R)
	var sum float64
	var n int
	for y := 0; y < im.H; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < im.W; x++ {
			dx := float64(x) - l.CX
			if dx*dx+dy*dy > r2 {
				continue
			}
			i := y*im.W + x
			d := float64(inner[i] - outer[i])
			sum += d * d
			n++
		}
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

func discMedian(im *fits.Image, l Limb) float64 {
	var level []float32
	r2 := (qualityRadius * l.R) * (qualityRadius * l.R)
	for y := 0; y < im.H; y++ {
		dy := float64(y) - l.CY
		for x := 0; x < im.W; x++ {
			dx := float64(x) - l.CX
			if dx*dx+dy*dy <= r2 {
				level = append(level, im.Pix[0][y*im.W+x])
			}
		}
	}
	if len(level) == 0 {
		return 1
	}
	return float64(imgops.Percentile(imgops.Subsample(level, 100000), 50))
}
