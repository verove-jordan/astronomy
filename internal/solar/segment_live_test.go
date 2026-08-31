package solar

import (
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// segment_live_test.go answers a question the pipeline never asks: is one clip one scene?
//
// The sun mode splits a session by measured disc radius and by exposure, and cuts each source into
// time windows so a field that turns while it records cannot smear. All three assume the SCENE is
// constant within a clip. On an Hα scope it is not. Tuning the etalon shifts the passband, and the
// disc that comes back is a different image of the Sun — plage where there was none, filaments gone,
// the whole thing brighter or flatter. Averaging across a retune is averaging two scenes.
//
// A passing cloud is the other break, and it is not the same break: it returns. The etalon does not.
// So this reports the two series that separate them — the on-disc level and the frame's own fine
// contrast — and cuts the clip where a step in either one PERSISTS.
//
//	ASTRO_SOLAR_CLIPS='input/x/a.MOV,input/x/b.MOV' go test ./internal/solar -run Segment_Live -v
//
// ASTRO_SOLAR_SEGCSV=<dir> also writes one CSV per clip for plotting.
func TestSegment_Live(t *testing.T) {
	clips := os.Getenv("ASTRO_SOLAR_CLIPS")
	if clips == "" {
		t.Skip("set ASTRO_SOLAR_CLIPS=<comma-separated clip paths> to segment clips")
	}
	csvDir := os.Getenv("ASTRO_SOLAR_SEGCSV")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()

	for _, path := range strings.Split(clips, ",") {
		if path = strings.TrimSpace(path); path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join("..", "..", path)
		}
		t.Run(filepath.Base(path), func(t *testing.T) {
			info := probeVideo(ctx, "", path)
			require.Greater(t, info.Frames, 0, "clip must probe")

			start := time.Now()
			scan, err := scanVideo(ctx, "", path, info, defaultCropMargin, false)
			require.NoError(t, err)
			t.Logf("%s: %d frames in %s (%.0f fps, %dx%d)", filepath.Base(path),
				len(scan.frames), time.Since(start).Round(time.Second), info.FPS, info.Width, info.Height)

			segs := segmentScan(scan.frames, info.FPS)
			t.Log("\n" + renderSegments(scan.frames, segs, info.FPS))

			if csvDir != "" {
				require.NoError(t, writeScanCSV(filepath.Join(csvDir,
					strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))+".csv"), scan.frames, info.FPS))
			}
			require.NotEmpty(t, segs, "a scanned clip must yield at least one segment")
		})
	}
}

// segRange is a stretch of frames whose scene did not change.
type segRange struct {
	from, to           int // inclusive frame indices
	level, score       float64
	cx, cy, r          float64
	drift              float64 // px of disc travel across the segment, in scan pixels
	levelMin, levelMax float64
}

func (s segRange) frames() int { return s.to - s.from + 1 }

// segmentScan cuts a clip where the scene steps and stays stepped.
//
// The statistic is a two-sided median over a half-second either side of every candidate cut, on both
// the level and the fine-contrast series, each expressed as a fraction of its own clip median so the
// two are comparable. A cloud produces a large level step with almost no contrast step and comes back
// within seconds; a retune moves both and does not come back. Requiring the step to persist for
// minSegFrames is what separates them without needing a cloud model.
func segmentScan(frames []frameScan, fps float64) []segRange {
	ok := make([]frameScan, 0, len(frames))
	for _, f := range frames {
		if f.ok {
			ok = append(ok, f)
		}
	}
	if len(ok) == 0 {
		return nil
	}
	if fps <= 0 {
		fps = 25
	}
	half := clampInt(int(fps/2), 5, 40)     // half-second either side of a cut
	minSeg := clampInt(int(2*fps), 12, 200) // a scene shorter than two seconds is not a scene
	if len(ok) < 2*minSeg {
		return []segRange{summarise(ok, 0, len(ok)-1)}
	}

	lev, sco := make([]float64, len(ok)), make([]float64, len(ok))
	for i, f := range ok {
		lev[i], sco[i] = f.level, f.score
	}
	levRef, scoRef := median(append([]float64(nil), lev...)), median(append([]float64(nil), sco...))
	if levRef <= 0 || scoRef <= 0 {
		return []segRange{summarise(ok, 0, len(ok)-1)}
	}

	// Step strength at every interior position, as a relative change in either series.
	step := make([]float64, len(ok))
	for i := half; i < len(ok)-half; i++ {
		dl := math.Abs(median(append([]float64(nil), lev[i-half:i]...))-
			median(append([]float64(nil), lev[i:i+half]...))) / levRef
		ds := math.Abs(median(append([]float64(nil), sco[i-half:i]...))-
			median(append([]float64(nil), sco[i:i+half]...))) / scoRef
		step[i] = math.Max(dl, ds)
	}

	// Cut greedily at the strongest step that still leaves two long-enough segments.
	cuts := []int{}
	for {
		best, bestVal := -1, segStepThreshold
		for i := range step {
			if step[i] <= bestVal || !farEnough(i, cuts, len(ok), minSeg) {
				continue
			}
			best, bestVal = i, step[i]
		}
		if best < 0 {
			break
		}
		cuts = append(cuts, best)
		sort.Ints(cuts)
	}

	segs := make([]segRange, 0, len(cuts)+1)
	from := 0
	for _, c := range append(cuts, len(ok)) {
		if c-1 >= from {
			segs = append(segs, summarise(ok, from, c-1))
		}
		from = c
	}
	return segs
}

// segStepThreshold is how big a step has to be, relative to the clip's own median, to be a new scene.
// Seeing moves the contrast series by a few percent frame to frame; a retune moves it by tens.
const segStepThreshold = 0.12

func farEnough(i int, cuts []int, n, minSeg int) bool {
	if i < minSeg || n-i < minSeg {
		return false
	}
	for _, c := range cuts {
		if abs(i-c) < minSeg {
			return false
		}
	}
	return true
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func summarise(f []frameScan, from, to int) segRange {
	lv := make([]float64, 0, to-from+1)
	sc := make([]float64, 0, to-from+1)
	cx := make([]float64, 0, to-from+1)
	cy := make([]float64, 0, to-from+1)
	rr := make([]float64, 0, to-from+1)
	minL, maxL := math.Inf(1), math.Inf(-1)
	for i := from; i <= to; i++ {
		lv, sc = append(lv, f[i].level), append(sc, f[i].score)
		cx, cy, rr = append(cx, f[i].limb.CX), append(cy, f[i].limb.CY), append(rr, f[i].limb.R)
		minL, maxL = math.Min(minL, f[i].level), math.Max(maxL, f[i].level)
	}
	s := segRange{
		from: f[from].index, to: f[to].index,
		level: median(append([]float64(nil), lv...)), score: median(append([]float64(nil), sc...)),
		cx: median(append([]float64(nil), cx...)), cy: median(append([]float64(nil), cy...)),
		r: median(append([]float64(nil), rr...)), levelMin: minL, levelMax: maxL,
	}
	s.drift = math.Hypot(f[to].limb.CX-f[from].limb.CX, f[to].limb.CY-f[from].limb.CY)
	return s
}

func renderSegments(frames []frameScan, segs []segRange, fps float64) string {
	if fps <= 0 {
		fps = 25
	}
	var b strings.Builder
	fmt.Fprintf(&b, "  %d segment(s)\n", len(segs))
	fmt.Fprintf(&b, "  %-4s %-13s %-13s %8s %8s %8s %7s %7s\n",
		"seg", "frames", "seconds", "level", "contrast", "radius", "drift", "dip%")
	for i, s := range segs {
		dip := 0.0
		if s.level > 0 {
			dip = 100 * (1 - s.levelMin/s.level)
		}
		fmt.Fprintf(&b, "  %-4d %-13s %-13s %8.4g %8.4g %8.1f %7.1f %7.1f\n",
			i, fmt.Sprintf("%d-%d", s.from, s.to),
			fmt.Sprintf("%.1f-%.1f", float64(s.from)/fps, float64(s.to)/fps),
			s.level, s.score, s.r, s.drift, dip)
	}
	// A coarse trace so the shape is visible without leaving the log.
	var lv []float64
	for _, f := range frames {
		if f.ok {
			lv = append(lv, f.level)
		}
	}
	if med := median(append([]float64(nil), lv...)); med > 0 {
		b.WriteString("\n  level trace (each cell ~1 s, '#' = clip median)\n  ")
		per := clampInt(int(fps), 1, 60)
		for i := 0; i < len(lv); i += per {
			j := clampInt(i+per, 0, len(lv))
			b.WriteByte(traceCell(median(append([]float64(nil), lv[i:j]...)) / med))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// traceCell maps a relative level onto one character: '#' at the median, darker as it falls.
func traceCell(rel float64) byte {
	switch {
	case rel >= 1.05:
		return '^'
	case rel >= 0.97:
		return '#'
	case rel >= 0.90:
		return '+'
	case rel >= 0.75:
		return '-'
	case rel >= 0.50:
		return '.'
	default:
		return ' '
	}
}

func writeScanCSV(path string, frames []frameScan, fps float64) error {
	if fps <= 0 {
		fps = 25
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	if err := w.Write([]string{"index", "seconds", "level", "score", "cx", "cy", "r", "ok"}); err != nil {
		return err
	}
	for _, fr := range frames {
		rec := []string{
			strconv.Itoa(fr.index),
			strconv.FormatFloat(float64(fr.index)/fps, 'f', 3, 64),
			strconv.FormatFloat(fr.level, 'g', 6, 64),
			strconv.FormatFloat(fr.score, 'g', 6, 64),
			strconv.FormatFloat(fr.limb.CX, 'f', 2, 64),
			strconv.FormatFloat(fr.limb.CY, 'f', 2, 64),
			strconv.FormatFloat(fr.limb.R, 'f', 2, 64),
			strconv.FormatBool(fr.ok),
		}
		if err := w.Write(rec); err != nil {
			return err
		}
	}
	return nil
}
