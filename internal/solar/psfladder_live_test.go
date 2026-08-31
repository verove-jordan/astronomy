package solar

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestPSFLadder_Live answers the only question worth asking when a stack looks worse than its frames:
// WHERE the resolution went.
//
// Three rungs, and they separate the two explanations that a single number cannot:
//
//   - individual frames — what the capture actually resolved
//   - the same frame warped ONCE onto the canonical raster — what any stack must pay, because
//     registration resamples. Quoting the raw frame as the target is a mistake this repo has made
//     before: a phone's own pipeline sharpens, so the raw figure can sit below what the optics can
//     deliver, and chasing it means chasing an artefact.
//   - the master — what the stack delivered
//
// If the master matches the warped frame, stacking is doing its job and the capture is the limit. If
// it is much worse, the loss is registration and no amount of extra frames will recover it.
//
//	ASTRO_SOLAR_FRAMES=work/sun_<id> ASTRO_SOLAR_MASTER=output/.../master_w01.fits \
//	  go test ./internal/solar -run PSFLadder -v
func TestPSFLadder_Live(t *testing.T) {
	dir := os.Getenv("ASTRO_SOLAR_FRAMES")
	if dir == "" {
		t.Skip("set ASTRO_SOLAR_FRAMES=<a run's work dir of ingested frames>")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.fits"))
	if err != nil || len(paths) == 0 {
		t.Skipf("no frames in %s", dir)
	}
	sort.Strings(paths)

	// Spread across the set rather than taking the first few: sharpness drifts through a clip.
	const n = 24
	step := len(paths)/n + 1

	type row struct {
		sun, moon float64
		score     float64
	}
	var rows []row
	var frames []Frame
	for i := 0; i < len(paths); i += step {
		im, err := fits.ReadImage(paths[i])
		if err != nil {
			continue
		}
		mono := firstPlane(im)
		g, ok := FitPair(mono)
		if !ok {
			continue
		}
		r := row{score: FrameSharpnessPair(mono, g)}
		if p := MeasurePSF(mono, g.Sun); p.OK {
			r.sun = p.SigmaPx
		}
		if g.Eclipsed() {
			if p := MeasureEdge(mono, g.Moon, edgeRising, 1); p.OK {
				r.moon = p.SigmaPx
			}
		}
		rows = append(rows, r)
		frames = append(frames, Frame{Path: paths[i], Index: i, Limb: g.Sun, Moon: g.Moon, Score: r.score})
	}
	if len(rows) < 4 {
		t.Fatalf("only %d frames could be measured", len(rows))
	}
	stat := func(get func(row) float64, name string) {
		var v []float64
		for _, r := range rows {
			if x := get(r); x > 0 {
				v = append(v, x)
			}
		}
		if len(v) == 0 {
			t.Logf("  %-22s no measurements", name)
			return
		}
		sort.Float64s(v)
		t.Logf("  %-22s best %.2f  p25 %.2f  median %.2f  p75 %.2f  worst %.2f  (n=%d)",
			name, v[0], v[len(v)/4], v[len(v)/2], v[3*len(v)/4], v[len(v)-1], len(v))
	}
	t.Logf("PER-FRAME point spread, sigma in the frame's own pixels (%d frames sampled of %d):", len(rows), len(paths))
	stat(func(r row) float64 { return r.sun }, "solar limb")
	stat(func(r row) float64 { return r.moon }, "occulter edge")
	stat(func(r row) float64 { return r.score }, "sharpness score")

	// Rung two: one frame through the whole registration path, alone. Whatever it loses, every stack
	// loses; whatever the master loses beyond it is the stack's own.
	best := 0
	for i, r := range rows {
		if r.score > rows[best].score {
			best = i
		}
	}
	one, err := Stack(context.Background(), frames[best:best+1], StackOptions{Drizzle: 1.5, NoRefine: true})
	if err != nil {
		t.Fatalf("single-frame stack: %v", err)
	}
	report := func(label string, res *StackResult) {
		p := MeasurePSF(res.Master, res.Limb)
		var m PSF
		if res.Moon.R > 0 {
			m = MeasureEdge(res.Master, res.Moon, edgeRising, 1)
		}
		t.Logf("  %-22s solar limb sigma %.2f  |  occulter edge sigma %.2f  (canonical px, %d frames)",
			label, p.SigmaPx, m.SigmaPx, res.Frames)
	}
	t.Log("ON THE CANONICAL RASTER (drizzle 1.5, so divide by 1.5 to compare with the frames above):")
	report("one frame, warped once", one)

	if mp := os.Getenv("ASTRO_SOLAR_MASTER"); mp != "" {
		im, err := fits.ReadImage(mp)
		if err == nil {
			mono := firstPlane(im)
			if g, ok := FitPair(mono); ok {
				report("the run's master", &StackResult{Master: mono, Limb: g.Sun, Moon: g.Moon})
			}
		}
	}
}
