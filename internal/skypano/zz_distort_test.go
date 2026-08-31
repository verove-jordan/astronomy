package skypano

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/verove-jordan/astronomy/internal/deepstars"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/pointing"
	"github.com/verove-jordan/astronomy/internal/rawmeta"
	"github.com/verove-jordan/astronomy/internal/starfield"
)

// TestZZDistortionShape measures each panel's OWN residual against the catalogue as a function of
// field position, split into the radial and tangential directions.
//
// This is the measurement that chooses the lens model. A residual that is radial and grows with
// radius is uncorrected radial distortion, and more radial terms will absorb it. A residual that is
// tangential, or that grows without a radial pattern, is not — and adding radial terms to it would
// be fitting noise with a shape it does not have.
func TestZZDistortionShape(t *testing.T) {
	requireHarness(t)
	cat, deep := deepstars.Load("../../library/catalogues/athyg_v32.bin")
	if !deep {
		t.Skip("no catalogue")
	}
	for _, name := range []string{"p05", "p08"} {
		suffix := os.Getenv("CAMS") // "" = per-panel solve, "_bundled" = after the shared-lens fit
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
		frames, _ := filepath.Glob("../../input/Iphone_10_08_2026_panels/" + name + "/*.DNG")
		sort.Strings(frames)
		m := rawmeta.Read(frames[len(frames)/2])
		_, _ = pointing.FromMeta(m)
		epoch := time.UnixMilli(m.TakenAtMs).UTC()

		ra, dec := VecToRADec(cam.Axis())
		cs := cat.InField(ra, dec, 50, 8000, epoch)
		det := starfield.Detect(im.Pix[1], im.W, im.H,
			starfield.Options{Sigma: 8, BoxRadius: 6, MinSeparation: 10, Max: 6000})

		// Where the camera says each catalogue star should land.
		type pred struct{ x, y float64 }
		var preds []pred
		for _, s := range cs {
			if x, y, ok := cam.Project(RADecToVec(s.RADeg, s.DecDeg)); ok {
				preds = append(preds, pred{x, y})
			}
		}
		// Match each prediction to its nearest detection.
		const maxPx = 30.0
		type resid struct{ r, radial, tangential float64 }
		var rs []resid
		cx, cy := float64(im.W)/2, float64(im.H)/2
		for _, p := range preds {
			best, bx, by := math.Inf(1), 0.0, 0.0
			for _, d := range det {
				dd := math.Hypot(d.X-p.x, d.Y-p.y)
				if dd < best {
					best, bx, by = dd, d.X, d.Y
				}
			}
			if best > maxPx {
				continue
			}
			// Decompose (detected - predicted) into along-radius and across-radius.
			ex, ey := bx-p.x, by-p.y
			rx, ry := p.x-cx, p.y-cy
			r := math.Hypot(rx, ry)
			if r < 1 {
				continue
			}
			rx, ry = rx/r, ry/r
			rs = append(rs, resid{r: r, radial: ex*rx + ey*ry, tangential: -ex*ry + ey*rx})
		}
		if len(rs) < 100 {
			fmt.Printf("%s: only %d matches\n", name, len(rs))
			continue
		}
		sort.Slice(rs, func(a, b int) bool { return rs[a].r < rs[b].r })
		fmt.Printf("%s: %d catalogue matches, residual by field radius\n", name, len(rs))
		const bins = 10
		for k := 0; k < bins; k++ {
			lo, hi := k*len(rs)/bins, (k+1)*len(rs)/bins
			var rad, tan []float64
			for _, x := range rs[lo:hi] {
				rad = append(rad, x.radial)
				tan = append(tan, x.tangential)
			}
			fmt.Printf("   r=%4.0f-%4.0f px (n=%4d): radial %+6.2f px, tangential %+6.2f px\n",
				rs[lo].r, rs[hi-1].r, hi-lo, medianOf(rad), medianOf(tan))
		}
	}
}
