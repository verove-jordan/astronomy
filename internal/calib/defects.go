package calib

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// Defect scan over a master dark's raw pool. Per pixel it measures the TEMPORAL mean and sigma
// across the darks, then flags outliers against a LOCAL baseline (a 5x5 separable median), so
// large-scale structure (amp glow) is never mistaken for defects:
//
//   - hot / cold — temporal mean far above/below its neighborhood (what `-cc=dark` would catch);
//   - unstable (RTS/telegraph) — temporal sigma far above its neighborhood: pixels that flicker
//     between states. Their master-dark value looks normal, so dark subtraction leaves a random
//     residual in every light — on an undithered, drifting sequence these smear into walking
//     noise. Only a per-frame cosmetic repair (or capture-time dithering) removes them.
//
// The list is written in Siril's find_hot/cosme format and applied at calibration time via
// `calibrate -cc=bpm` (see siril.CalibMasters.BadPixelMap).
const (
	defectMinFrames  = 8      // fewer darks → temporal sigma too noisy to trust; skip the scan
	defectSigmaK     = 3.0    // hot/cold: |mean − local median| > K·robust σ (mirrors -cc=dark 3/3)
	defectRTSK       = 6.0    // unstable: sigma − local median > K·robust σ (tight: repair is per-pixel)
	defectMaxFrac    = 0.005  // cap: at most 0.5% of pixels may be listed — keeps a bad scan harmless
	defectStatSample = 200000 // pixels sampled for the robust (median/MAD) scale estimates
)

// DefectPixel is one flagged sensor pixel in the source frames' own row order (0-based).
type DefectPixel struct {
	X, Y int
	Kind byte    // 'H' (hot or unstable — repaired the same way) or 'C' (cold/dead)
	cat  byte    // detection category — 'h' hot, 'c' cold, 'u' unstable — drives the count split
	sev  float64 // detection strength in robust sigmas, for the cap ordering
}

// Defects is the outcome of a dark-pool scan.
type Defects struct {
	W, H      int
	TopDown   bool // source frames are ROWORDER=TOP-DOWN → y is flipped when writing the Siril list
	Pixels    []DefectPixel
	Hot       int
	Cold      int
	Unstable  int
	Truncated bool // the defectMaxFrac cap dropped the weakest detections
}

// Summary is a short human description for run notes.
func (d *Defects) Summary() string {
	s := fmt.Sprintf("%d hot, %d cold, %d unstable px", d.Hot, d.Cold, d.Unstable)
	if d.Truncated {
		s += " (capped)"
	}
	return s
}

// ScanDarkDefects measures per-pixel temporal statistics across the raw dark frames and returns
// the flagged defects. It returns (nil, nil) when there are too few usable frames for a
// trustworthy temporal sigma — the caller then simply keeps the -cc=dark fallback.
func ScanDarkDefects(paths []string) (*Defects, error) {
	var sum, sumsq []float64
	var w, h, used int
	topDown := false
	for _, p := range paths {
		f, err := fits.Open(p)
		if err != nil {
			continue // unreadable frame: the master stack itself will surface real corruption
		}
		order, _ := f.Header.String("ROWORDER")
		im, err := fits.ReadImage(p)
		if err != nil || im.C != 1 {
			continue // color/odd frame: defect coordinates would be ambiguous
		}
		if used == 0 {
			w, h = im.W, im.H
			topDown = strings.EqualFold(order, "TOP-DOWN")
			sum = make([]float64, w*h)
			sumsq = make([]float64, w*h)
		} else if im.W != w || im.H != h {
			continue // mismatched dims cannot share per-pixel stats
		}
		for i, v := range im.Pix[0] {
			fv := float64(v)
			sum[i] += fv
			sumsq[i] += fv * fv
		}
		used++
	}
	if used < defectMinFrames {
		return nil, nil
	}

	// Per-pixel temporal mean and sigma, as float32 maps (precision is ample for outlier flagging).
	n := float64(used)
	means := make([]float32, w*h)
	sigmas := make([]float32, w*h)
	for i := range sum {
		m := sum[i] / n
		means[i] = float32(m)
		sigmas[i] = float32(math.Sqrt(math.Max(0, sumsq[i]/n-m*m)))
	}

	d := &Defects{W: w, H: h, TopDown: topDown}
	flagged := make(map[int]bool)
	flag := func(i int, kind, cat byte, sev float64) {
		if flagged[i] {
			return
		}
		flagged[i] = true
		d.Pixels = append(d.Pixels, DefectPixel{X: i % w, Y: i / w, Kind: kind, cat: cat, sev: sev})
	}

	// Hot/cold from the mean map, unstable from the sigma map — both against a local median
	// baseline, so amp glow / vignetting gradients never read as defects.
	meanRes, meanScale := localResiduals(means, w, h)
	if meanScale > 0 {
		for i, r := range meanRes {
			switch dev := float64(r) / meanScale; {
			case dev > defectSigmaK:
				flag(i, 'H', 'h', dev)
			case dev < -defectSigmaK:
				flag(i, 'C', 'c', -dev)
			}
		}
	}
	sigRes, sigScale := localResiduals(sigmas, w, h)
	if sigScale > 0 {
		for i, r := range sigRes {
			if dev := float64(r) / sigScale; dev > defectRTSK && !flagged[i] {
				flag(i, 'H', 'u', dev)
			}
		}
	}
	d.cap()
	d.recount()
	sort.Slice(d.Pixels, func(a, b int) bool { // deterministic list order
		pa, pb := d.Pixels[a], d.Pixels[b]
		if pa.Y != pb.Y {
			return pa.Y < pb.Y
		}
		return pa.X < pb.X
	})
	return d, nil
}

// cap enforces defectMaxFrac, keeping the strongest detections so a pathological scan can only
// ever repair a bounded, most-defective fraction of the sensor.
func (d *Defects) cap() {
	maxN := int(defectMaxFrac * float64(d.W) * float64(d.H))
	if maxN < 1 || len(d.Pixels) <= maxN {
		return
	}
	sort.Slice(d.Pixels, func(a, b int) bool { return d.Pixels[a].sev > d.Pixels[b].sev })
	d.Pixels = d.Pixels[:maxN]
	d.Truncated = true
}

// recount derives the per-category counts from the (possibly capped) pixel list.
func (d *Defects) recount() {
	d.Hot, d.Cold, d.Unstable = 0, 0, 0
	for _, p := range d.Pixels {
		switch p.cat {
		case 'c':
			d.Cold++
		case 'u':
			d.Unstable++
		default:
			d.Hot++
		}
	}
}

// localResiduals returns each value minus its local (5x5 separable median) baseline, plus a robust
// scale (1.4826·MAD) of those residuals estimated on a deterministic sample. Scale 0 means the map
// is degenerate (uniform) and the caller must not flag anything.
func localResiduals(vals []float32, w, h int) ([]float32, float64) {
	base := medianFilter5(vals, w, h)
	res := base // reuse: overwrite the baseline buffer with the residuals
	for i, v := range vals {
		res[i] = v - base[i]
	}
	stride := len(res)/defectStatSample + 1
	sample := make([]float64, 0, len(res)/stride+1)
	for i := 0; i < len(res); i += stride {
		sample = append(sample, float64(res[i]))
	}
	med := medianOf(sample)
	for i, v := range sample {
		sample[i] = math.Abs(v - med)
	}
	return res, 1.4826 * medianOf(sample)
}

// medianFilter5 is a separable 5-median (horizontal then vertical) — a robust local baseline that
// follows smooth structure (amp glow) but not single-pixel defects, at a fraction of a true 5x5
// median's cost. Windows clamp at the borders.
func medianFilter5(vals []float32, w, h int) []float32 {
	tmp := make([]float32, len(vals))
	out := make([]float32, len(vals))
	var win [5]float32
	for y := 0; y < h; y++ {
		row := vals[y*w : (y+1)*w]
		dst := tmp[y*w : (y+1)*w]
		for x := 0; x < w; x++ {
			k := 0
			for dx := -2; dx <= 2; dx++ {
				if c := x + dx; c >= 0 && c < w {
					win[k] = row[c]
					k++
				}
			}
			dst[x] = medianSmall(win[:k])
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			k := 0
			for dy := -2; dy <= 2; dy++ {
				if r := y + dy; r >= 0 && r < h {
					win[k] = tmp[r*w+x]
					k++
				}
			}
			out[y*w+x] = medianSmall(win[:k])
		}
	}
	return out
}

// medianSmall sorts up to 5 values in place (insertion sort) and returns the middle one.
func medianSmall(v []float32) float32 {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
	return v[len(v)/2]
}

func medianOf(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	sort.Float64s(v)
	return v[len(v)/2]
}

// WriteSirilBPM writes the defect list in Siril's find_hot/cosme format ("P x y H|C", 0-based).
// Siril's pixel space is bottom-up, so y is flipped for TOP-DOWN sources — verified against
// find_hot output on Siril 1.4.3 (see the live syntax test). The write is atomic (tmp + rename)
// because the library dir is shared across concurrent runs.
func (d *Defects) WriteSirilBPM(path string) error {
	var b strings.Builder
	for _, p := range d.Pixels {
		y := p.Y
		if d.TopDown {
			y = d.H - 1 - p.Y
		}
		fmt.Fprintf(&b, "P %d %d %c\n", p.X, y, p.Kind)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// DefectsListPath is the bad-pixel-map sidecar path convention for a master dark ("" for "").
// Keeping it a sibling file (no schema) means the S3 library mirror and the reuse signature
// machinery need no awareness of it.
func DefectsListPath(masterPath string) string {
	if masterPath == "" {
		return ""
	}
	return strings.TrimSuffix(masterPath, filepath.Ext(masterPath)) + "_defects.lst"
}

// DefectsListFor returns the master dark's defect sidecar when one exists on disk, else "".
func DefectsListFor(masterPath string) string {
	p := DefectsListPath(masterPath)
	if p == "" {
		return ""
	}
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// buildDefectList scans the master dark's raw pool and writes the Siril bad-pixel map beside the
// master. Soft by design: it returns a human note (possibly "") and never fails the build — a
// missing or failed map just leaves the -cc=dark fallback in place.
func buildDefectList(masterPath string, framePaths []string) string {
	d, err := ScanDarkDefects(framePaths)
	if err != nil {
		return "dark defect map skipped: " + err.Error()
	}
	lst := DefectsListPath(masterPath)
	if d == nil || len(d.Pixels) == 0 {
		// Too few darks for a temporal scan, or a genuinely clean sensor: remove any stale list so
		// calibration falls back to -cc=dark instead of applying an outdated map.
		_ = os.Remove(lst)
		return ""
	}
	if err := d.WriteSirilBPM(lst); err != nil {
		return "dark defect map skipped: " + err.Error()
	}
	return fmt.Sprintf("dark defect map: %s → per-frame cosmetic repair via -cc=bpm", d.Summary())
}
