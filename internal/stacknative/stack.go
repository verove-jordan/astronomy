package stacknative

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/stackalg"
)

// bandRows is how many image rows are held in flight per frame. 64 rows of an ASI1600 frame is
// ~1.2 MB per frame, so a 60-frame sequence peaks around 70 MB — against 3.9 GB for the whole
// sequence in memory. See internal/calib/defects.go for the same streaming shape.
const bandRows = 64

// Request is one native stack: which registered frames to combine, how, and where to write.
type Request struct {
	// Frames are the registered FITS files IN SEQUENCE ORDER — linear-fit clipping reads that order
	// as its x axis, so it must match capture order, not directory order.
	Frames []string
	// Out is the master's path (".fits" included).
	Out string
	// Options is the resolved recipe (call stackalg.Resolve first, or Stack will).
	Options stackalg.Options
	// Weights, when non-nil, is one weight per frame — already derived from the sequence metrics
	// (noise / wFWHM / star count / sub-count). nil stacks unweighted.
	Weights []float64
	// OnProgress reports completed row bands, for the job's progress bar.
	OnProgress func(done, total int)
}

// Result reports what the stack did, for the run's notes and run.json.
type Result struct {
	Frames  int `json:"frames"`
	Width   int `json:"width"`
	Height  int `json:"height"`
	Channel int `json:"channels"`
	// Rejected is the fraction of (pixel, frame) samples the outlier test discarded — the same
	// number Siril prints as "Pixel rejection in channel #0".
	Rejected  float64 `json:"rejected_fraction"`
	Engine    string  `json:"engine"`
	Algorithm string  `json:"algorithm"`
}

// Stack combines the registered frames into one master. It streams row bands rather than holding the
// sequence in memory, and parallelizes across bands.
//
// It does NOT soft-fail: a native stack that cannot run must surface as a channel error, because
// silently falling back to a different algorithm would make the run's own provenance a lie.
func Stack(ctx context.Context, req Request) (*Result, error) {
	if len(req.Frames) < 2 {
		return nil, fmt.Errorf("native stack needs at least 2 frames, got %d", len(req.Frames))
	}
	o := stackalg.Resolve(req.Options, len(req.Frames))

	files, w, h, c, err := openAll(req.Frames)
	if err != nil {
		return nil, err
	}

	norm, err := normalizationFor(files, o.Norm, o.FastNorm)
	if err != nil {
		return nil, fmt.Errorf("measure normalization: %w", err)
	}

	// Noise weighting is the one mode this package can measure for itself (from the same decimated
	// statistics the normalization needs); sharpness/star-count/sub-count weights come from the
	// sequence metrics, which only the caller has.
	weights := req.Weights
	if weights == nil && o.Weight == stackalg.WeightNoise {
		if weights, err = noiseWeights(files, o.FastNorm); err != nil {
			return nil, fmt.Errorf("measure frame noise: %w", err)
		}
	}
	if weights != nil && len(weights) != len(req.Frames) {
		return nil, fmt.Errorf("got %d weights for %d frames", len(weights), len(req.Frames))
	}

	out := fits.NewImage(w, h, c)
	bands := (h + bandRows - 1) / bandRows
	var rejected, samples int64
	var mu sync.Mutex

	err = forEachBand(ctx, bands, func(band int) error {
		y0 := band * bandRows
		y1 := y0 + bandRows
		if y1 > h {
			y1 = h
		}
		rej, tot, err := stackBand(files, out, norm, weights, o, w, y0, y1, c)
		if err != nil {
			return err
		}
		mu.Lock()
		rejected += rej
		samples += tot
		mu.Unlock()
		if req.OnProgress != nil {
			req.OnProgress(band+1, bands)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if o.OutputNorm {
		rescaleToUnit(out)
	}
	if err := writeMaster(out, req.Out, files[0], len(req.Frames)); err != nil {
		return nil, err
	}

	res := &Result{
		Frames: len(req.Frames), Width: w, Height: h, Channel: c,
		Engine: string(stackalg.EngineNative), Algorithm: string(o.Combine) + "/" + string(o.Reject),
	}
	if samples > 0 {
		res.Rejected = float64(rejected) / float64(samples)
	}
	return res, nil
}

// openAll opens every frame and checks they share one geometry — a mismatched frame would otherwise
// be read as garbage rows.
func openAll(paths []string) ([]*fits.File, int, int, int, error) {
	files := make([]*fits.File, 0, len(paths))
	var w, h, c int
	for i, p := range paths {
		f, err := fits.Open(p)
		if err != nil {
			return nil, 0, 0, 0, fmt.Errorf("open %s: %w", filepath.Base(p), err)
		}
		fw, fh, fc, err := planeDims(f)
		if err != nil {
			return nil, 0, 0, 0, fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		if i == 0 {
			w, h, c = fw, fh, fc
		} else if fw != w || fh != h || fc != c {
			return nil, 0, 0, 0, fmt.Errorf("%s is %dx%dx%d but the sequence is %dx%dx%d — the frames are not co-registered",
				filepath.Base(p), fw, fh, fc, w, h, c)
		}
		files = append(files, f)
	}
	return files, w, h, c, nil
}

// planeDims reads a frame's geometry from its header.
func planeDims(f *fits.File) (w, h, c int, err error) {
	nw, ok1 := f.Header.Int("NAXIS1")
	nh, ok2 := f.Header.Int("NAXIS2")
	if !ok1 || !ok2 {
		return 0, 0, 0, fmt.Errorf("missing NAXIS1/NAXIS2")
	}
	c = 1
	if naxis, ok := f.Header.Int("NAXIS"); ok && naxis >= 3 {
		if n3, ok := f.Header.Int("NAXIS3"); ok {
			c = int(n3)
		}
	}
	return int(nw), int(nh), c, nil
}

// stackBand combines rows [y0,y1) of every channel and writes them into out.
func stackBand(files []*fits.File, out *fits.Image, norm []normCoef, weights []float64,
	o stackalg.Options, w, y0, y1, channels int) (rejected, samples int64, err error) {
	n := len(files)
	rows := y1 - y0
	s := newScratch(n)
	v := make([]float64, n)
	needDetail := o.Reject == stackalg.RejectEntropyWeighted
	var detail []float64
	if needDetail {
		detail = make([]float64, n)
	}

	bands := make([][]float32, n)
	for ch := 0; ch < channels; ch++ {
		for i, f := range files {
			b, err := f.ReadPlaneBand(ch, y0, y1)
			if err != nil {
				return 0, 0, fmt.Errorf("read rows %d–%d of frame %d: %w", y0, y1, i+1, err)
			}
			bands[i] = b
		}
		var detailMaps [][]float32
		if needDetail {
			detailMaps = make([][]float32, n)
			for i := range bands {
				detailMaps[i] = localDetail(bands[i], w, rows)
			}
		}
		plane := out.Pix[ch]
		for y := 0; y < rows; y++ {
			for x := 0; x < w; x++ {
				idx := y*w + x
				for i := range bands {
					v[i] = norm[i].apply(float64(bands[i][idx]))
				}
				if needDetail {
					for i := range detailMaps {
						detail[i] = float64(detailMaps[i][idx])
					}
				}
				val, kept := combinePixelCounted(o, v, weights, detail, s)
				plane[(y0+y)*w+x] = float32(val)
				if kept >= 0 {
					rejected += int64(n - kept)
					samples += int64(n)
				}
			}
		}
	}
	return rejected, samples, nil
}

// localDetail measures each pixel's local detail energy — the absolute difference from a 3×3 box
// mean, which is what the entropy-weighted average uses to favour the frames that actually resolve
// structure at that pixel.
func localDetail(band []float32, w, rows int) []float32 {
	out := make([]float32, len(band))
	for y := 0; y < rows; y++ {
		for x := 0; x < w; x++ {
			var sum float32
			var cnt float32
			for dy := -1; dy <= 1; dy++ {
				yy := y + dy
				if yy < 0 || yy >= rows {
					continue
				}
				for dx := -1; dx <= 1; dx++ {
					xx := x + dx
					if xx < 0 || xx >= w {
						continue
					}
					sum += band[yy*w+xx]
					cnt++
				}
			}
			if cnt == 0 {
				continue
			}
			out[y*w+x] = float32(math.Abs(float64(band[y*w+x] - sum/cnt)))
		}
	}
	return out
}

// forEachBand runs fn over every band, bounded by the CPU count and cancellable.
func forEachBand(ctx context.Context, bands int, fn func(band int) error) error {
	workers := runtime.NumCPU()
	if workers > bands {
		workers = bands
	}
	if workers < 1 {
		workers = 1
	}
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		firstEr error
		next    = make(chan int)
	)
	go func() {
		defer close(next)
		for b := 0; b < bands; b++ {
			select {
			case next <- b:
			case <-ctx.Done():
				return
			}
		}
	}()
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range next {
				if err := ctx.Err(); err != nil {
					mu.Lock()
					if firstEr == nil {
						firstEr = err
					}
					mu.Unlock()
					return
				}
				if err := fn(b); err != nil {
					mu.Lock()
					if firstEr == nil {
						firstEr = err
					}
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	if firstEr != nil {
		return firstEr
	}
	return ctx.Err()
}

// rescaleToUnit maps the result into [0,1] — Siril's -output_norm.
func rescaleToUnit(im *fits.Image) {
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, plane := range im.Pix {
		for _, x := range plane {
			f := float64(x)
			if f < lo {
				lo = f
			}
			if f > hi {
				hi = f
			}
		}
	}
	span := hi - lo
	if !(span > 0) || math.IsInf(span, 0) {
		return
	}
	for _, plane := range im.Pix {
		for i, x := range plane {
			plane[i] = float32((float64(x) - lo) / span)
		}
	}
}

// writeMaster writes the combined image, carrying forward the reference frame's provenance cards so
// downstream steps (photometric normalization, the combined-mono integration's sub-count weighting,
// the run's depth report) still find what they read.
func writeMaster(im *fits.Image, path string, ref *fits.File, frames int) error {
	cards := []string{
		intCard("STACKCNT", int64(frames), "number of stacked images"),
	}
	if v, ok := ref.Header.Float("EXPTIME"); ok {
		cards = append(cards, floatCard("EXPTIME", v, "exposure time of one sub (s)"))
		cards = append(cards, floatCard("LIVETIME", v*float64(frames), "total integration time (s)"))
	}
	for _, key := range []string{"DATE-OBS", "FILTER", "OBJECT", "INSTRUME", "TELESCOP"} {
		if v, ok := ref.Header.String(key); ok && v != "" {
			cards = append(cards, strCard(key, v))
		}
	}
	tmp := path + ".tmp"
	if err := im.WriteFITSWith(tmp, cards); err != nil {
		return fmt.Errorf("write master: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("publish master: %w", err)
	}
	return nil
}
