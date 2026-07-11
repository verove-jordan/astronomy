package transient

import (
	"fmt"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/trail"
)

// SeqOptions tunes MaskSequence. The zero value is the standard cross-frame mask (line-aware swaths
// plus the legacy per-pixel outlier pass).
type SeqOptions struct {
	// LineOnly keeps ONLY confirmed line swaths, skipping the per-pixel blanket outlier pass. Comet
	// mode sets it so the moving coma — a per-pixel positive outlier by design — is never touched.
	LineOnly bool
}

// MaskSequence removes satellite/aircraft trails from a set of REGISTERED frames (by path), in place,
// and reports what it changed. Unlike the per-pixel MaskCrossFrame it is LINE-AWARE: it detects each
// straight streak against the cross-frame median and paints the whole swath — including the
// sub-threshold wings a per-pixel threshold misses — back to the clean median (which, for any signal
// consistent across frames, equals that signal, so a false positive is ~lossless). A geostationary
// streak sitting on the same sky pixels in the median is detected on the median itself and repaired
// from local background instead. Unless LineOnly, it also runs the legacy per-pixel outlier pass
// (cosmic rays / hot pixels). With fewer than MinFrames frames it falls back to a per-frame detector.
// Soft-fail: preflight/IO problems return an error the caller notes; a run never depends on it.
func MaskSequence(paths []string, k float64, opts SeqOptions) (*Report, error) {
	if k <= 0 || len(paths) == 0 {
		return &Report{}, nil
	}
	if len(paths) < MinFrames {
		return maskPerFrame(paths, k)
	}
	frames, err := readAll(paths)
	if err != nil {
		return nil, err
	}
	w, h, c := frames[0].W, frames[0].H, frames[0].C
	if err := checkDims(frames, w, h, c); err != nil {
		return nil, err
	}
	med, sig := medianMADPlanes(frames, w, h, c)
	medSegs := detectMedianTrails(med, w, h)

	rep := &Report{width: w, height: h}
	changed := make([]bool, len(frames))
	for fi, f := range frames {
		fr := maskOneFrame(f, med, sig, medSegs, k, opts, w, h, c)
		fr.Index = fi + 1
		changed[fi] = fr.MaskedPx > 0
		rep.PerFrame = append(rep.PerFrame, fr)
	}
	if err := writeBack(paths, frames, changed); err != nil {
		return rep, err
	}
	return rep, nil
}

// maskOneFrame masks one frame's trails: confirmed residual-line swaths → cross-frame median,
// geostationary swaths → local background, then (unless LineOnly) the per-pixel outlier blanket.
func maskOneFrame(f *fits.Image, med, sig [][]float32, medSegs []trail.Segment, k float64,
	opts SeqOptions, w, h, c int) FrameReport {
	segs := trail.DetectSegments(residualPlane(f, med, w, h, c), w, h, trail.DefaultParams(k))
	masked := 0
	for _, s := range segs {
		for ch := 0; ch < c; ch++ {
			masked += trail.ApplySwathMedian(f.Pix[ch], med[ch], w, h, s)
		}
	}
	for si, s := range medSegs {
		for ch := 0; ch < c; ch++ {
			masked += trail.ApplySwathLocalBG(f.Pix[ch], w, h, s, seedFor(si, ch))
		}
	}
	if !opts.LineOnly {
		masked += blanketMask(f, med, sig, k, w, h, c)
	}
	return FrameReport{Segments: len(segs) + len(medSegs), MaskedPx: masked}
}

// blanketMask replaces every per-pixel positive outlier (value > median + k·MADσ) with the median —
// the legacy MaskCrossFrame behaviour, kept for cosmic rays / hot pixels the line detector ignores.
func blanketMask(f *fits.Image, med, sig [][]float32, k float64, w, h, c int) int {
	n := 0
	for ch := 0; ch < c; ch++ {
		p, m, s := f.Pix[ch], med[ch], sig[ch]
		for i := range p {
			if s[i] <= 0 {
				continue
			}
			if float64(p[i]) > float64(m[i])+k*float64(s[i]) {
				p[i] = m[i]
				n++
			}
		}
	}
	return n
}

// seedFor is a stable per-(segment,channel) noise seed for the geostationary local-background fill.
func seedFor(segIdx, ch int) int64 { return int64(segIdx+1)*2000003 + int64(ch)*131 }

// readAll loads every frame into memory (channels are masked sequentially, so only one channel-set is
// resident at a time upstream).
func readAll(paths []string) ([]*fits.Image, error) {
	frames := make([]*fits.Image, len(paths))
	for i, p := range paths {
		im, err := fits.ReadImage(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", filepath.Base(p), err)
		}
		frames[i] = im
	}
	return frames, nil
}

// checkDims enforces the registered-frame contract: identical dimensions and channel count.
func checkDims(frames []*fits.Image, w, h, c int) error {
	for i, f := range frames {
		if f.W != w || f.H != h || f.C != c {
			return fmt.Errorf("frame %d is %dx%dx%d, want %dx%dx%d", i, f.W, f.H, f.C, w, h, c)
		}
	}
	return nil
}

// writeBack rewrites only the frames that changed, preserving each header (OverwriteData).
func writeBack(paths []string, frames []*fits.Image, changed []bool) error {
	for i, p := range paths {
		if !changed[i] {
			continue
		}
		if err := frames[i].OverwriteData(p); err != nil {
			return fmt.Errorf("write %s: %w", filepath.Base(p), err)
		}
	}
	return nil
}

// maskPerFrame is the <MinFrames fallback: without enough frames for a robust cross-frame median, each
// frame is masked on its own (raw-mode line detection + local-background fill).
func maskPerFrame(paths []string, k float64) (*Report, error) {
	rep := &Report{PerFrameFallback: true}
	for i, p := range paths {
		fr, err := trail.MaskFrameFile(p, trail.RawParams(k))
		if err != nil {
			return rep, err
		}
		rep.PerFrame = append(rep.PerFrame, FrameReport{Index: i + 1, Segments: len(fr.Segments), MaskedPx: fr.MaskedPx})
	}
	return rep, nil
}
