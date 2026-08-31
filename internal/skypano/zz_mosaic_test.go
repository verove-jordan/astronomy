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

func TestZZBuildMosaic(t *testing.T) {
	requireHarness(t)
	cat, deep := deepstars.Load("../../library/catalogues/athyg_v32.bin")
	if !deep {
		t.Skip("no catalogue")
	}
	var panels []Panel
	for _, name := range []string{"p01", "p03", "p04", "p05", "p06", "p07", "p08"} {
		runs, _ := filepath.Glob("../../output/" + name + "/*")
		sort.Strings(runs)
		if len(runs) == 0 {
			continue
		}
		im, _ := fits.ReadImage(filepath.Join(runs[len(runs)-1], "lin_sky.fits"))
		if im == nil {
			continue
		}
		// The solve is deterministic and slow; cache it so re-rendering is a minute, not ten.
		cacheFile := scratch + "cam_" + name + ".json"
		if b, err := os.ReadFile(cacheFile); err == nil {
			var cam Camera
			if json.Unmarshal(b, &cam) == nil {
				ra, dec := VecToRADec(cam.Axis())
				fmt.Printf("%s cached: RA=%.2f Dec=%+.2f\n", name, ra, dec)
				panels = append(panels, Panel{Name: name, Cam: cam, Img: im})
				continue
			}
		}
		frames, _ := filepath.Glob("../../input/Iphone_10_08_2026_panels/" + name + "/*.DNG")
		sort.Strings(frames)
		m := rawmeta.Read(frames[len(frames)/2])
		pf, _ := pointing.FromMeta(m)
		det := starfield.Detect(im.Pix[1], im.W, im.H,
			starfield.Options{Sigma: 6, BoxRadius: 5, MinSeparation: 8, Max: 4000})
		dd := make([]Detection, len(det))
		for i, d := range det {
			dd[i] = Detection{X: d.X, Y: d.Y}
		}
		epoch := time.UnixMilli(m.TakenAtMs).UTC()
		catFor := func(ra, dec float64) [][3]float64 {
			cs := cat.InField(ra, dec, 50, 6000, epoch)
			v := make([][3]float64, len(cs))
			for i, s := range cs {
				v[i] = RADecToVec(s.RADeg, s.DecDeg)
			}
			return v
		}
		start := time.Now()
		cam, sol, az, ok := AutoSolve(pf, m.Orientation, im.W, im.H, float64(m.FocalLength35mm), true, catFor, dd, DefaultQuadSolveOptions())
		if !ok {
			fmt.Printf("%s: AutoSolve FAILED\n", name)
			continue
		}
		ra, dec := VecToRADec(cam.Axis())
		fmt.Printf("%s solved: %d stars rms=%.2f az=%.0f (recorded %.0f) RA=%.2f Dec=%+.2f  %s\n",
			name, sol.Matches, sol.RMSPx, az, pf.AzDeg, ra, dec, time.Since(start).Round(time.Second))
		if b, err := json.Marshal(cam); err == nil {
			_ = os.WriteFile(cacheFile, b, 0o644)
		}
		panels = append(panels, Panel{Name: name, Cam: cam, Img: im})
	}
	if len(panels) < 2 {
		t.Skip("not enough solved panels")
	}

	// Solve the LENS once, from every panel's stars together. Each panel was solved on its own above
	// and each looked fine, but a per-panel fit hides distortion rather than measuring it: matching
	// inside a few pixels drops exactly the outer-field stars that carry it. See bundle.go.
	if cat, deep := deepstars.Load("../../library/catalogues/athyg_v32.bin"); deep {
		cats := make([][][3]float64, len(panels))
		dets := make([][]Detection, len(panels))
		cams := make([]Camera, len(panels))
		for i, p := range panels {
			cams[i] = p.Cam
			frames, _ := filepath.Glob("../../input/Iphone_10_08_2026_panels/" + p.Name + "/*.DNG")
			sort.Strings(frames)
			m := rawmeta.Read(frames[len(frames)/2])
			ra, dec := VecToRADec(p.Cam.Axis())
			for _, s := range cat.InField(ra, dec, 50, 12000, time.UnixMilli(m.TakenAtMs).UTC()) {
				cats[i] = append(cats[i], RADecToVec(s.RADeg, s.DecDeg))
			}
			for _, d := range starfield.Detect(p.Img.Pix[1], p.Img.W, p.Img.H,
				starfield.Options{Sigma: 8, BoxRadius: 6, MinSeparation: 10, Max: 8000}) {
				dets[i] = append(dets[i], Detection{X: d.X, Y: d.Y})
			}
		}
		o := DefaultSolveOptions()
		o.MatchRadiusPx, o.FitRadiusPx = 60, 3
		start := time.Now()
		if got, sols, ok := BundleLens(cams, cats, dets, o, 10); ok {
			for i := range panels {
				panels[i].Cam = got[i]
				fmt.Printf("  bundled %s: %d stars rms=%.2f px\n", panels[i].Name, sols[i].Matches, sols[i].RMSPx)
				if b, err := json.Marshal(got[i]); err == nil {
					_ = os.WriteFile(scratch+"cam_"+panels[i].Name+"_bundled.json", b, 0o644)
				}
			}
			fmt.Printf("  shared lens: F=%.1f px (%.2f\"/px) K1=%+.5f K2=%+.5f K3=%+.5f  in %s\n",
				got[0].F, 3600*180/math.Pi/got[0].F, got[0].K1, got[0].K2, got[0].K3,
				time.Since(start).Round(time.Second))
		}
	}
	for _, spec := range []struct {
		name  string
		proj  Projection
		fr    Frame
		scale float64
	}{
		{"galactic_strip", Equirectangular, Galactic, 0.03},
		{"stereographic", Stereographic, Equatorial, 0.03},
	} {
		c, err := PlanCanvas(panels, spec.proj, spec.fr, spec.scale)
		if err != nil {
			t.Fatalf("%s: %v", spec.name, err)
		}
		MatchPhotometry(panels, c, 40000, 8)
		fmt.Printf("%s canvas %dx%d centre %.2f %+.2f planes:", spec.name, c.W, c.H, c.Lon0, c.Lat0)
		for _, p := range panels {
			fmt.Printf(" %s=%+.5f%+.5fu%+.5fv", p.Name,
				p.Corr.Plane[1][0], p.Corr.Plane[1][1], p.Corr.Plane[1][2])
		}
		fmt.Println()
		start := time.Now()
		img, cov, err := Render(panels, c, DefaultRenderOptions())
		if err != nil {
			t.Fatalf("%s render: %v", spec.name, err)
		}
		base := "/private/tmp/claude-501/-Users-jordanverove-projects-perso-astronomy/be7181f0-9673-4b1f-98a9-5ac0b0801742/scratchpad/mosaic_" + spec.name
		if err := img.WriteFITS(base + "_raw.fits"); err != nil {
			t.Fatal(err)
		}
		bg, err := Flatten(img, cov, c, DefaultFlattenOptions())
		if err != nil {
			t.Fatalf("%s flatten: %v", spec.name, err)
		}
		fmt.Printf("  rendered in %s; background order %d from %d tiles, model range %.5f..%.5f\n",
			time.Since(start).Round(time.Second), bg.Order, bg.Tiles, bg.MinMax[1][0], bg.MinMax[1][1])
		if err := img.WriteFITS(base + "_flat.fits"); err != nil {
			t.Fatal(err)
		}
		if b, err := json.Marshal(c); err == nil {
			_ = os.WriteFile(base+"_canvas.json", b, 0o644)
		}
		cvIm := fits.NewImage(c.W, c.H, 1)
		copy(cvIm.Pix[0], cov)
		if err := cvIm.WriteFITS(base + "_cov.fits"); err != nil {
			t.Fatal(err)
		}
		keep := Grade(img, cov, c, DefaultGradeOptions())
		if err := writePNG(img, keep, base+".png"); err != nil {
			t.Fatal(err)
		}
		fmt.Printf("  graded -> %s.png\n", base)
	}
}
