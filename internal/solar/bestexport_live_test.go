package solar

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestBestExport_Live renders a spread of the sharpest frames from a run's work directory, so the
// export can be judged against the stack without re-ingesting the clip.
//
//	ASTRO_SOLAR_FRAMES=work/sun_<id> ASTRO_SOLAR_OUT=<dir> go test ./internal/solar -run BestExport -v
func TestBestExport_Live(t *testing.T) {
	dir, out := os.Getenv("ASTRO_SOLAR_FRAMES"), os.Getenv("ASTRO_SOLAR_OUT")
	if dir == "" || out == "" {
		t.Skip("set ASTRO_SOLAR_FRAMES and ASTRO_SOLAR_OUT")
	}
	paths, _ := filepath.Glob(filepath.Join(dir, "*.fits"))
	if len(paths) == 0 {
		t.Skipf("no frames in %s", dir)
	}
	var frames []Frame
	for i, p := range paths {
		im, err := fits.ReadImage(p)
		if err != nil {
			continue
		}
		mono := firstPlane(im)
		g, ok := FitPair(mono)
		if !ok {
			continue
		}
		frames = append(frames, Frame{Path: p, Index: i, TimeMs: int64(i) * 33,
			Limb: g.Sun, Moon: g.Moon, Score: FrameSharpnessPair(mono, g)})
	}
	t.Logf("measured %d of %d frames", len(frames), len(paths))

	picks := SelectSpread(frames, 6, 3000)
	for i, f := range picks {
		im, _ := fits.ReadImage(f.Path)
		mono := firstPlane(im)
		g := Pair{Sun: f.Limb, Moon: f.Moon}
		fin, psf, _ := ResolveFinish(mono, g.Sun, DefaultFinish())
		edge := MeasureEdge(mono, g.Moon, edgeRising, 1)
		t.Logf("  best%02d: idx %5d score %.5f | solar sigma %.2f | occulter edge sigma %.2f",
			i+1, f.Index, f.Score, psf.SigmaPx, edge.SigmaPx)
		dst := filepath.Join(out, fmt.Sprintf("best%02d", i+1))
		if err := WritePNG(FinishPair(mono, g, fin), dst+".png"); err != nil {
			t.Fatal(err)
		}
	}
}
