package skypano

import (
	"fmt"
	"sort"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

func TestZZDiag(t *testing.T) {
	requireHarness(t)
	base := "/private/tmp/claude-501/-Users-jordanverove-projects-perso-astronomy/be7181f0-9673-4b1f-98a9-5ac0b0801742/scratchpad/"
	im, err := fits.ReadImage(base + "mosaic_galactic_strip_flat.fits")
	if err != nil {
		t.Skip(err)
	}
	cv, err := fits.ReadImage(base + "mosaic_galactic_strip_cov.fits")
	if err != nil {
		t.Skip(err)
	}
	ref := TypicalCoverage(cv.Pix[0])
	fmt.Printf("typical coverage %.3f\n", ref)
	for k := 0; k < im.C; k++ {
		var v []float32
		for i, p := range im.Pix[k] {
			if cv.Pix[0][i] >= 0.5*ref {
				v = append(v, p)
			}
		}
		sort.Slice(v, func(a, b int) bool { return v[a] < v[b] })
		q := func(f float64) float32 { return v[int(f*float64(len(v)-1))] }
		fmt.Printf("ch%d n=%d  p1=%.5f p5=%.5f p25=%.5f p50=%.5f p75=%.5f p90=%.5f p99=%.5f p99.9=%.5f\n",
			k, len(v), q(.01), q(.05), q(.25), q(.5), q(.75), q(.90), q(.99), q(.999))
	}

	// Coarse map of the green-channel median: is the residual gradient aligned with the band?
	const nx, ny = 12, 8
	fmt.Println("median map (x ->, y down), green channel:")
	for ty := 0; ty < ny; ty++ {
		line := ""
		for tx := 0; tx < nx; tx++ {
			var v []float32
			for y := ty * im.H / ny; y < (ty+1)*im.H/ny; y += 3 {
				for x := tx * im.W / nx; x < (tx+1)*im.W/nx; x += 3 {
					if p := im.Pix[1][y*im.W+x]; p > 0 {
						v = append(v, p)
					}
				}
			}
			if len(v) < 100 {
				line += "   .  "
				continue
			}
			sort.Slice(v, func(a, b int) bool { return v[a] < v[b] })
			line += fmt.Sprintf(" %.3f", v[len(v)/2])
		}
		fmt.Println(line)
	}
}
