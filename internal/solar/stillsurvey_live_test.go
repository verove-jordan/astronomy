package solar

import (
	"encoding/csv"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/tiff"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// stillsurvey_live_test.go measures a pile of stills without developing or stacking anything.
//
// Triage answers "which of these share a scale" and the bracket splitter answers "which share an
// exposure", but both run inside a job. When the question is how to CUT a session into runs in the
// first place — this burst and that burst are the same scene, that one is not — what is needed is
// the per-file geometry and levels, cheaply, for hundreds of files at once.
//
// It reads whatever image files it is pointed at, so the fast path is a camera's embedded previews
// (`exiftool -b -PreviewImage`), which on a Canon raw are full resolution. The preview carries white
// balance and a tone curve, so its LEVELS are only comparable between frames of equal exposure —
// which is exactly the comparison that matters here. Geometry is unaffected.
//
//	ASTRO_SOLAR_STILLS='/tmp/prev/*.jpg' go test ./internal/solar -run StillSurvey_Live -v
//
// ASTRO_SOLAR_STILLCSV=<file> writes the table for grouping.
func TestStillSurvey_Live(t *testing.T) {
	spec := os.Getenv("ASTRO_SOLAR_STILLS")
	if spec == "" {
		t.Skip("set ASTRO_SOLAR_STILLS=<comma-separated files or globs> to survey stills")
	}
	var paths []string
	for _, pat := range strings.Split(spec, ",") {
		m, err := filepath.Glob(strings.TrimSpace(pat))
		if err != nil {
			t.Fatalf("glob %s: %v", pat, err)
		}
		paths = append(paths, m...)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		t.Skip("no files matched")
	}

	rows := make([]stillRow, len(paths))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, p := range paths {
		wg.Add(1)
		go func(i int, p string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rows[i] = surveyStill(p)
		}(i, p)
	}
	wg.Wait()

	var b strings.Builder
	fmt.Fprintf(&b, "\n  %-16s %7s %9s %9s %8s %8s %8s %6s\n",
		"file", "radius", "cx", "cy", "disc", "contrast", "sky", "sat%")
	for _, r := range rows {
		if !r.ok {
			fmt.Fprintf(&b, "  %-16s  (no limb)\n", r.name)
			continue
		}
		fmt.Fprintf(&b, "  %-16s %7.1f %9.1f %9.1f %8.4f %8.4f %8.4f %6.1f\n",
			r.name, r.r, r.cx, r.cy, r.disc, r.contrast, r.sky, 100*r.satFrac)
	}
	t.Log(b.String())

	if out := os.Getenv("ASTRO_SOLAR_STILLCSV"); out != "" {
		if err := writeStillCSV(out, rows); err != nil {
			t.Fatalf("write csv: %v", err)
		}
		t.Logf("wrote %s", out)
	}
}

type stillRow struct {
	name       string
	ok         bool
	cx, cy, r  float64
	arc        float64
	disc       float64 // median on-disc luma, 0..1
	contrast   float64 // robust fine contrast on the disc, relative to its own median
	sky        float64 // median off-disc luma, 0..1
	satFrac    float64 // share of on-disc pixels at full scale
	redOverGrn float64
}

// surveyStill measures one image: where the disc is, how big, how bright, and how much structure it
// carries. Structure is the local (3 px) absolute deviation over the inner disc, normalised by the
// disc median — the quantity that collapses when an etalon is off band and the disc goes flat.
func surveyStill(path string) stillRow {
	row := stillRow{name: strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))}
	f, err := os.Open(path)
	if err != nil {
		return row
	}
	defer f.Close()
	src, _, err := image.Decode(f)
	if err != nil {
		return row
	}
	bnds := src.Bounds()
	w, h := bnds.Dx(), bnds.Dy()
	luma := make([]float64, w*h)
	var rsum, gsum float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			cr, cg, cb, _ := src.At(bnds.Min.X+x, bnds.Min.Y+y).RGBA()
			fr, fg, fb := float64(cr)/65535, float64(cg)/65535, float64(cb)/65535
			luma[y*w+x] = math.Max(fr, math.Max(fg, fb)) // an Hα disc lives in red alone
			rsum, gsum = rsum+fr, gsum+fg
		}
	}
	if gsum > 0 {
		row.redOverGrn = rsum / gsum
	}

	im := fits.NewImage(w, h, 1)
	for i, v := range luma {
		im.Pix[0][i] = float32(v)
	}
	lb, ok := FitLimb(im)
	if !ok {
		return row
	}
	row.ok, row.cx, row.cy, row.r, row.arc = true, lb.CX, lb.CY, lb.R, lb.ArcDeg

	var on, off []float64
	var sat int
	inner := 0.85 * lb.R
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			d := math.Hypot(float64(x)-lb.CX, float64(y)-lb.CY)
			v := luma[y*w+x]
			switch {
			case d < inner:
				on = append(on, v)
				if v >= 0.999 {
					sat++
				}
			case d > 1.25*lb.R && d < 1.8*lb.R:
				off = append(off, v)
			}
		}
	}
	if len(on) == 0 {
		row.ok = false
		return row
	}
	row.disc = median(append([]float64(nil), on...))
	row.sky = median(append([]float64(nil), off...))
	row.satFrac = float64(sat) / float64(len(on))
	row.contrast = discStructure(luma, w, h, lb, row.disc)
	return row
}

// discStructure is the median |v - blur(v)| over the inner disc, as a fraction of the disc level.
// The blur is a 3 px box in each axis, so what survives is granulation, plage and filaments — the
// things an etalon on band produces and an etalon off band does not.
func discStructure(luma []float64, w, h int, lb Limb, level float64) float64 {
	if level <= 0 {
		return 0
	}
	inner := int(0.8 * lb.R)
	x0, x1 := clampInt(int(lb.CX)-inner, 2, w-3), clampInt(int(lb.CX)+inner, 2, w-3)
	y0, y1 := clampInt(int(lb.CY)-inner, 2, h-3), clampInt(int(lb.CY)+inner, 2, h-3)
	var dev []float64
	for y := y0; y <= y1; y += 2 {
		for x := x0; x <= x1; x += 2 {
			if math.Hypot(float64(x)-lb.CX, float64(y)-lb.CY) > 0.8*lb.R {
				continue
			}
			var sum float64
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					sum += luma[(y+dy)*w+x+dx]
				}
			}
			dev = append(dev, math.Abs(luma[y*w+x]-sum/9))
		}
	}
	if len(dev) == 0 {
		return 0
	}
	return median(dev) / level
}

func writeStillCSV(path string, rows []stillRow) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"file", "ok", "cx", "cy", "r", "arc", "disc", "contrast", "sky", "sat", "r_over_g"}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := w.Write([]string{
			r.name, strconv.FormatBool(r.ok),
			strconv.FormatFloat(r.cx, 'f', 2, 64), strconv.FormatFloat(r.cy, 'f', 2, 64),
			strconv.FormatFloat(r.r, 'f', 2, 64), strconv.FormatFloat(r.arc, 'f', 1, 64),
			strconv.FormatFloat(r.disc, 'f', 5, 64), strconv.FormatFloat(r.contrast, 'f', 6, 64),
			strconv.FormatFloat(r.sky, 'f', 5, 64), strconv.FormatFloat(r.satFrac, 'f', 5, 64),
			strconv.FormatFloat(r.redOverGrn, 'f', 3, 64),
		}); err != nil {
			return err
		}
	}
	return nil
}
