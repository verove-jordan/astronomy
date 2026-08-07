package stacknative_test

// Parity of the Go combiner against Siril's own `stack`, on the same frames. Where an algorithm
// exists in both engines the two must agree statistically — same sky level, same noise, same trail
// removal — even though they are independent implementations and never agree bit for bit.
//
// Skipped when no siril-cli is installed (e.g. Linux CI).

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/stackalg"
	"github.com/verove-jordan/astronomy/internal/stacknative"
)

const defaultSirilBin = "/Applications/Siril.app/Contents/MacOS/siril-cli"

func sirilRunner(t *testing.T) *siril.Runner {
	t.Helper()
	bin := os.Getenv("SIRIL_BIN")
	if bin == "" {
		bin = defaultSirilBin
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("no siril-cli at %s", bin)
	}
	return siril.New(bin, siril.Limits{})
}

type prng struct{ s uint64 }

func (r *prng) next() float64 {
	r.s = r.s*6364136223846793005 + 1442695040888963407
	return float64(r.s>>11) / float64(1<<53)
}

// buildSequence writes n frames of a synthetic star field with per-frame sky drift and one
// satellite trail, and returns their paths in order.
func buildSequence(t *testing.T, dir string, n, w, h int) []string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	r := &prng{s: 4242}
	var paths []string
	for i := 0; i < n; i++ {
		im := fits.NewImage(w, h, 1)
		sky := 0.10 + 0.002*float64(i) // the sky brightens through the session
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				v := sky + (r.next()-0.5)*0.006
				// A few fixed "stars", so the two masters have real structure to compare.
				for _, st := range [][3]float64{{20, 30, 0.6}, {60, 55, 0.35}, {45, 15, 0.22}} {
					dx, dy := float64(x)-st[0], float64(y)-st[1]
					v += st[2] * math.Exp(-(dx*dx+dy*dy)/8)
				}
				if i == 6 && y == 40 { // a satellite crossing one sub
					v += 0.5
				}
				im.Pix[0][y*w+x] = float32(v)
			}
		}
		p := filepath.Join(dir, fmt.Sprintf("f_%03d.fits", i+1))
		require.NoError(t, im.WriteFITS(p))
		paths = append(paths, p)
	}
	return paths
}

// stats summarises a master the way an astrophotographer judges one: sky level, sky noise, and the
// peak of the brightest star.
func stats(t *testing.T, path string) (sky, noise, peak float64) {
	t.Helper()
	im, err := fits.ReadImage(path)
	require.NoError(t, err)
	v := make([]float64, 0, len(im.Pix[0]))
	for _, x := range im.Pix[0] {
		v = append(v, float64(x))
		if float64(x) > peak {
			peak = float64(x)
		}
	}
	sort.Float64s(v)
	sky = v[len(v)/2]
	dev := make([]float64, len(v))
	for i, x := range v {
		dev[i] = math.Abs(x - sky)
	}
	sort.Float64s(dev)
	return sky, 1.4826 * dev[len(dev)/2], peak
}

// TestParity_NativeMatchesSiril stacks one identical sequence with both engines at the same
// algorithm and compares the masters. They are independent implementations, so the tolerance is set
// by the noise, not by floating point — what must match is the ASTRONOMY.
func TestParity_NativeMatchesSiril(t *testing.T) {
	r := sirilRunner(t)
	const n, w, h = 16, 80, 80

	for _, algo := range []stackalg.Reject{
		stackalg.RejectWinsorized,
		stackalg.RejectSigma,
		stackalg.RejectMAD,
		stackalg.RejectNone,
	} {
		t.Run(string(algo), func(t *testing.T) {
			root := t.TempDir()
			seqDir := filepath.Join(root, "seq")
			paths := buildSequence(t, seqDir, n, w, h)

			o := stackalg.DefaultLights()
			o.Reject = algo
			o.OutputNorm = false // compare in the frames' own units

			// Siril, over the same files in the same directory.
			script := "requires 1.2.0\nsetext fits\nset32bits\nlink cal -out=.\n" +
				fmt.Sprintf("stack cal %s -out=%s\n", siril.StackClause(o, n), filepath.Join(root, "siril"))
			_, err := r.Run(context.Background(), seqDir, script, nil)
			require.NoError(t, err)

			// The Go combiner, over the same frames.
			_, err = stacknative.Stack(context.Background(), stacknative.Request{
				Frames: paths, Out: filepath.Join(root, "native.fits"), Options: o,
			})
			require.NoError(t, err)

			sSky, sNoise, sPeak := stats(t, filepath.Join(root, "siril.fits"))
			nSky, nNoise, nPeak := stats(t, filepath.Join(root, "native.fits"))

			// The sky must land in the same place to well within one noise sigma.
			assert.InDelta(t, sSky, nSky, math.Max(sNoise, 1e-4),
				"sky level: siril %.5f vs native %.5f", sSky, nSky)
			// The noise must agree to within 35%: both average the same photons, and the residual
			// difference is the scale estimator (Siril's IKSS against our MAD).
			assert.InDelta(t, sNoise, nNoise, 0.35*sNoise+1e-5,
				"sky noise: siril %.6f vs native %.6f", sNoise, nNoise)
			// And the star must survive at the same brightness — rejection must not eat signal.
			assert.InDelta(t, sPeak, nPeak, 0.05*sPeak,
				"star peak: siril %.4f vs native %.4f", sPeak, nPeak)
		})
	}
}

// TestParity_BothEnginesRemoveTheTrail: the sequence carries a satellite across one sub. Whichever
// engine ran, the trail row must read like its neighbours.
func TestParity_BothEnginesRemoveTheTrail(t *testing.T) {
	r := sirilRunner(t)
	const n, w, h = 16, 80, 80
	root := t.TempDir()
	seqDir := filepath.Join(root, "seq")
	paths := buildSequence(t, seqDir, n, w, h)

	o := stackalg.DefaultLights()
	o.OutputNorm = false

	script := "requires 1.2.0\nsetext fits\nset32bits\nlink cal -out=.\n" +
		fmt.Sprintf("stack cal %s -out=%s\n", siril.StackClause(o, n), filepath.Join(root, "siril"))
	_, err := r.Run(context.Background(), seqDir, script, nil)
	require.NoError(t, err)
	_, err = stacknative.Stack(context.Background(), stacknative.Request{
		Frames: paths, Out: filepath.Join(root, "native.fits"), Options: o,
	})
	require.NoError(t, err)

	// Siril writes FITS bottom-up, so compare each master against ITS OWN neighbouring row rather
	// than assuming both put the trail at the same y.
	rowMean := func(im *fits.Image, y int) float64 {
		var sum float64
		for x := 0; x < im.W; x++ {
			sum += float64(im.Pix[0][y*im.W+x])
		}
		return sum / float64(im.W)
	}
	// The trail was planted on row 40, which carries no star; Siril may write the master bottom-up,
	// so both candidate rows are checked. Averaged in, the trail would lift its row by 0.5/16 =
	// 0.031 above its neighbours — three orders of magnitude above what a clean rejection leaves.
	for _, name := range []string{"siril.fits", "native.fits"} {
		im, err := fits.ReadImage(filepath.Join(root, name))
		require.NoError(t, err)
		for _, y := range []int{h - 1 - 40, 40} {
			excess := rowMean(im, y) - 0.5*(rowMean(im, y-1)+rowMean(im, y+1))
			assert.Less(t, excess, 0.004,
				"%s: row %d stands %.4f above its neighbours — the trail leaked through", name, y, excess)
		}
	}
}
