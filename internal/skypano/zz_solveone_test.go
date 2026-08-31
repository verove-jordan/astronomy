package skypano

import (
	"fmt"
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

// TestZZSolveOne plate-solves ONE panel stack, to answer whether a given pointing is solvable without
// waiting for a whole run.
//
//	ASTRO_SOLVE_FITS=<lin_sky.fits> ASTRO_SOLVE_FRAMES=<dir of that panel's DNGs> \
//	  ASTRO_PANO_HARNESS=1 go test ./internal/skypano/ -run TestZZSolveOne -v
func TestZZSolveOne(t *testing.T) {
	requireHarness(t)
	fitsPath, frameDir := os.Getenv("ASTRO_SOLVE_FITS"), os.Getenv("ASTRO_SOLVE_FRAMES")
	if fitsPath == "" || frameDir == "" {
		t.Skip("set ASTRO_SOLVE_FITS and ASTRO_SOLVE_FRAMES")
	}
	cat, deep := deepstars.Load("../../library/catalogues/athyg_v32.bin")
	if !deep {
		t.Skip("no catalogue")
	}
	im, err := fits.ReadImage(fitsPath)
	if err != nil {
		t.Fatal(err)
	}
	frames, _ := filepath.Glob(filepath.Join(frameDir, "*.DNG"))
	sort.Strings(frames)
	if len(frames) == 0 {
		t.Skip("no frames in " + frameDir)
	}
	m := rawmeta.Read(frames[len(frames)/2])
	pf, ok := pointing.FromMeta(m)
	if !ok {
		t.Fatal("no pointing metadata")
	}

	det := starfield.Detect(im.Pix[1], im.W, im.H,
		starfield.Options{Sigma: 8, BoxRadius: 6, MinSeparation: 10, Max: 8000})
	dd := make([]Detection, len(det))
	for i, d := range det {
		dd[i] = Detection{X: d.X, Y: d.Y}
	}
	epoch := time.UnixMilli(m.TakenAtMs).UTC()
	// Match the catalogue's DEPTH to the detections': a quad needs all four of its stars present on
	// both sides, so 8000 catalogue stars against 667 detections cannot agree.
	catN := 8000
	if n := os.Getenv("ASTRO_SOLVE_CATN"); n != "" {
		fmt.Sscanf(n, "%d", &catN)
	}
	fmt.Printf("  catalogue depth %d\n", catN)
	catFor := func(ra, dec float64) [][3]float64 {
		cs := cat.InField(ra, dec, 50, catN, epoch)
		v := make([][3]float64, len(cs))
		for i, s := range cs {
			v[i] = RADecToVec(s.RADeg, s.DecDeg)
		}
		return v
	}
	fmt.Printf("%s: %dx%d, %d detections, recorded az %.0f alt %.0f\n",
		filepath.Base(filepath.Dir(fitsPath)), im.W, im.H, len(dd), pf.AzDeg, pf.AltDeg)

	start := time.Now()
	cam, sol, az, ok := AutoSolve(pf, m.Orientation, im.W, im.H,
		float64(m.FocalLength35mm), true, catFor, dd, DefaultQuadSolveOptions())
	if !ok {
		fmt.Printf("  NOT SOLVED after %s\n", time.Since(start).Round(time.Second))
		return
	}
	ra, dec := VecToRADec(cam.Axis())
	fmt.Printf("  solved: %d stars rms=%.2f px az=%.0f RA=%.2f Dec=%+.2f in %s\n",
		sol.Matches, sol.RMSPx, az, ra, dec, time.Since(start).Round(time.Second))
}
