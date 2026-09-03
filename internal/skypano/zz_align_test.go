package skypano

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/starfield"
)

// TestZZPanelAgreement measures how far apart two panels put the SAME star.
//
// The shape of that disagreement decides the fix. A constant shift means the panels' rotations are
// slightly off and solving them jointly against each other fixes it. A shift that grows across the
// overlap means the lens model is wrong — one radial term is not enough over 72 degrees — and no
// amount of re-pointing will close it.
func TestZZPanelAgreement(t *testing.T) {
	requireHarness(t)
	type solved struct {
		name string
		cam  Camera
		det  []starfield.Star
	}
	var ps []solved
	for _, name := range []string{"p01", "p03", "p04", "p05", "p06", "p07", "p08"} {
		suffix := os.Getenv("CAMS") // "" for the per-panel solve, "_bundled" for the shared-lens one
		b, err := os.ReadFile(scratch + "cam_" + name + suffix + ".json")
		if err != nil {
			continue
		}
		var cam Camera
		if json.Unmarshal(b, &cam) != nil {
			continue
		}
		runs, _ := filepath.Glob("../../output/" + name + "/*")
		sort.Strings(runs)
		if len(runs) == 0 {
			continue
		}
		im, _ := fits.ReadImage(filepath.Join(runs[len(runs)-1], "lin_sky.fits"))
		if im == nil {
			continue
		}
		det := starfield.Detect(im.Pix[1], im.W, im.H,
			starfield.Options{Sigma: 8, BoxRadius: 6, MinSeparation: 10, Max: 6000})
		ps = append(ps, solved{name, cam, det})
		fmt.Printf("%s: %d stars\n", name, len(det))
	}

	// Sky positions each panel assigns to its own detections.
	sky := make([][][3]float64, len(ps))
	for i, p := range ps {
		for _, s := range p.det {
			sky[i] = append(sky[i], p.cam.Unproject(s.X, s.Y))
		}
	}

	const matchArcsec = 900 // generous: we are measuring the disagreement, not asserting it is small
	for i := 0; i < len(ps); i++ {
		for j := i + 1; j < len(ps); j++ {
			var seps, dxs, dys, rads []float64
			for a, va := range sky[i] {
				best, bestK := math.Inf(1), -1
				for b, vb := range sky[j] {
					d := math.Acos(clamp1(dot3(va, vb))) * 180 / math.Pi * 3600
					if d < best {
						best, bestK = d, b
					}
				}
				if bestK < 0 || best > matchArcsec {
					continue
				}
				seps = append(seps, best)
				// Where in panel i the star sits, and which way panel j moved it.
				xb, yb, ok := ps[i].cam.Project(sky[j][bestK])
				if !ok {
					continue
				}
				dxs = append(dxs, xb-ps[i].det[a].X)
				dys = append(dys, yb-ps[i].det[a].Y)
				u, v := panelUV(ps[i].det[a].X, ps[i].det[a].Y, 4032, 3024)
				rads = append(rads, math.Hypot(u, v))
			}
			if len(seps) < 50 {
				continue
			}
			// The systematic part is the median; the scatter about it is what is left.
			mdx, mdy := medianOf(dxs), medianOf(dys)
			var scat []float64
			for k := range dxs {
				scat = append(scat, math.Hypot(dxs[k]-mdx, dys[k]-mdy))
			}
			// Does the disagreement grow towards the frame edge?
			var inner, outer []float64
			for k := range rads {
				d := math.Hypot(dxs[k], dys[k])
				if rads[k] < 0.5 {
					inner = append(inner, d)
				} else if rads[k] > 0.9 {
					outer = append(outer, d)
				}
			}
			in, out := math.NaN(), math.NaN()
			if len(inner) > 10 {
				in = medianOf(inner)
			}
			if len(outer) > 10 {
				out = medianOf(outer)
			}
			fmt.Printf("%s-%s: %4d pairs, median sep %5.0f\" (%.1f panel px), systematic shift (%+.1f,%+.1f) px, scatter %.1f px, centre %.1f px vs edge %.1f px\n",
				ps[i].name, ps[j].name, len(seps), medianOf(seps), medianOf(seps)/73.4,
				mdx, mdy, medianOf(scat), in, out)
		}
	}
}

func medianOf(v []float64) float64 {
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s) == 0 {
		return math.NaN()
	}
	return s[len(s)/2]
}
