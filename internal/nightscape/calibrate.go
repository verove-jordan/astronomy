package nightscape

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/rawconv"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// maxCalFrames caps how many frames per calibration group are median-combined, to bound memory (each
// full-res frame is held in RAM, ~150 MB for a phone frame). Phone calibration sets are small; frames
// beyond the cap are dropped with a warning rather than risking an out-of-memory.
const maxCalFrames = 24

// hasCalibration reports whether any calibration-frame folder was supplied.
func hasCalibration(o Options) bool {
	return o.DarkDir != "" || o.FlatDir != "" || o.BiasDir != ""
}

// calibrateLights builds dark/flat/bias masters from the configured folders and calibrates each light
// FITS in seqDir in place, in LINEAR light: load the (gamma) light → linearize → subtract the dark
// (else the bias) → divide the normalized flat → clamp → re-encode gamma → save. Calibration is only
// valid in linear light, but the gamma round-trip keeps the downstream register + compose (which
// linearizes each frame again) byte-compatible with the uncalibrated path, so only this opt-in branch
// changes. Soft-fail: any problem returns a note and leaves the lights untouched, so a bad or empty
// calibration set never breaks the proven path. Offset == bias; a dark already contains the bias, so
// the bias master is used only when no dark is supplied.
func calibrateLights(ctx context.Context, o Options, seqDir string, lightNames []string) string {
	if len(lightNames) == 0 {
		return ""
	}
	dark, dn := buildMaster(ctx, o, o.DarkDir, "dark")
	bias, bn := buildMaster(ctx, o, o.BiasDir, "bias")
	flat, fn := buildMaster(ctx, o, o.FlatDir, "flat")
	notes := joinNotes(dn, bn, fn)
	if dark == nil && bias == nil && flat == nil {
		if notes != "" {
			return "calibration skipped (" + notes + ")"
		}
		return ""
	}

	// Masters must match the lights pixel-for-pixel; drop any that don't (e.g. cal frames shot at a
	// different resolution) so a mismatch degrades to "less calibration", never a crash.
	ref, err := fits.ReadImage(lightNames[0])
	if err != nil {
		return "calibration skipped (read light: " + err.Error() + ")"
	}
	dark, notes = matchOrDrop(dark, ref, "dark", notes)
	bias, notes = matchOrDrop(bias, ref, "bias", notes)
	flat, notes = matchOrDrop(flat, ref, "flat", notes)
	if dark == nil && bias == nil && flat == nil {
		return "calibration skipped (" + notes + ")"
	}

	// Master flat: remove its own pedestal (bias, else dark) then normalize to mean 1 per channel, so
	// dividing a light by it corrects vignetting/dust without shifting overall brightness.
	useFlat := flat != nil
	if useFlat {
		switch {
		case bias != nil:
			subtractImage(flat, bias)
		case dark != nil:
			subtractImage(flat, dark)
		}
		if !normalizeFlat(flat) {
			useFlat = false
			notes = joinNotes(notes, "flat unusable (near-zero), skipped")
		}
	}

	done := 0
	for _, name := range lightNames {
		im, err := fits.ReadImage(name)
		if err != nil {
			return fmt.Sprintf("calibration aborted (read %s: %v); %d lights done", filepath.Base(name), err, done)
		}
		linearizeSRGB(im)
		switch {
		case dark != nil:
			subtractImage(im, dark)
		case bias != nil:
			subtractImage(im, bias)
		}
		if useFlat {
			divideImage(im, flat)
		}
		clampZero(im)
		encodeSRGBImage(im)
		if err := im.WriteFITS(name); err != nil {
			return fmt.Sprintf("calibration aborted (write %s: %v); %d lights done", filepath.Base(name), err, done)
		}
		done++
	}
	summary := fmt.Sprintf("calibrated %d lights (dark=%v flat=%v bias=%v)", done, dark != nil, useFlat, bias != nil && dark == nil)
	if notes != "" {
		summary += "; " + notes
	}
	return summary
}

// buildMaster develops the raws (or links the FITS) in dir, converts them to FITS via Siril, and
// returns their per-channel, per-pixel median in linear light. Returns (nil, note) when dir is empty,
// unreadable, or the convert/read fails — the caller treats a nil master as "this calibration type
// unavailable" and proceeds.
func buildMaster(ctx context.Context, o Options, dir, tag string) (*fits.Image, string) {
	if dir == "" {
		return nil, ""
	}
	tmp := filepath.Join(o.WorkDir, "cal_"+tag)
	if err := fsutil.EnsureDir(tmp); err != nil {
		return nil, tag + ": " + err.Error()
	}
	capped := ""
	if raws, _ := inspect.ListRawFrames(dir); len(raws) > 0 {
		if len(raws) > maxCalFrames {
			capped = fmt.Sprintf("%s: used %d of %d frames (cap)", tag, maxCalFrames, len(raws))
			raws = raws[:maxCalFrames]
		}
		if _, _, err := rawconv.PrepareTIFF(ctx, raws, tmp, nil); err != nil {
			return nil, tag + ": develop: " + err.Error()
		}
	} else if ff, _ := inspect.ListFITSFrames(dir); len(ff) > 0 {
		if len(ff) > maxCalFrames {
			capped = fmt.Sprintf("%s: used %d of %d frames (cap)", tag, maxCalFrames, len(ff))
			ff = ff[:maxCalFrames]
		}
		if _, err := fsutil.LinkFrames(tmp, ff); err != nil {
			return nil, tag + ": stage: " + err.Error()
		}
	} else {
		return nil, tag + " dir has no usable frames"
	}
	if _, err := o.Siril.Run(ctx, tmp, siril.ConvertScript(tag), nil); err != nil {
		return nil, tag + ": convert: " + err.Error()
	}
	frames, _ := filepath.Glob(filepath.Join(tmp, tag+"_*.fit*"))
	sort.Strings(frames)
	master, err := medianMaster(frames)
	if err != nil {
		return nil, tag + ": " + err.Error()
	}
	return master, capped
}

// medianMaster reads every frame (linearizing each) and returns their per-channel, per-pixel median.
// All frames are held in memory at once (bounded by maxCalFrames upstream); the median rejects hot
// pixels, cosmic rays and the odd plane drift better than a mean.
func medianMaster(paths []string) (*fits.Image, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no frames to combine")
	}
	imgs := make([]*fits.Image, 0, len(paths))
	for _, p := range paths {
		im, err := fits.ReadImage(p)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", filepath.Base(p), err)
		}
		linearizeSRGB(im)
		imgs = append(imgs, im)
	}
	w, h, c := imgs[0].W, imgs[0].H, imgs[0].C
	for _, im := range imgs[1:] {
		if im.W != w || im.H != h || im.C != c {
			return nil, fmt.Errorf("frame size mismatch (%dx%dx%d)", im.W, im.H, im.C)
		}
	}
	out := fits.NewImage(w, h, c)
	scratch := make([]float32, len(imgs))
	for ch := 0; ch < c; ch++ {
		for i := 0; i < w*h; i++ {
			for k, im := range imgs {
				scratch[k] = im.Pix[ch][i]
			}
			out.Pix[ch][i] = medianInPlace(scratch)
		}
	}
	return out, nil
}

// matchOrDrop returns m if it matches ref's dimensions, else nil plus an appended note — so a
// differently-sized calibration master degrades the run to "less calibration" instead of a crash.
func matchOrDrop(m, ref *fits.Image, tag, notes string) (*fits.Image, string) {
	if m == nil {
		return nil, notes
	}
	if m.W != ref.W || m.H != ref.H || m.C != ref.C {
		return nil, joinNotes(notes, fmt.Sprintf("%s master %dx%d≠light %dx%d, dropped", tag, m.W, m.H, ref.W, ref.H))
	}
	return m, notes
}

// --- in-place pixel math (masters and lights share dimensions; callers guarantee it) ---

func subtractImage(im, sub *fits.Image) {
	for c := 0; c < im.C && c < sub.C; c++ {
		p, s := im.Pix[c], sub.Pix[c]
		for i := range p {
			p[i] -= s[i]
		}
	}
}

// divideImage divides im by the normalized flat, guarding against tiny/zero flat values (which would
// blow up to huge gains) by leaving such pixels unchanged.
func divideImage(im, flat *fits.Image) {
	for c := 0; c < im.C && c < flat.C; c++ {
		p, f := im.Pix[c], flat.Pix[c]
		for i := range p {
			if f[i] > 1e-4 {
				p[i] /= f[i]
			}
		}
	}
}

// normalizeFlat scales each channel of the (pedestal-removed) flat to mean 1, so dividing by it is a
// pure vignetting/dust correction. Returns false if a channel mean is non-positive (an unusable flat).
func normalizeFlat(flat *fits.Image) bool {
	for c := 0; c < flat.C; c++ {
		p := flat.Pix[c]
		var sum float64
		for _, v := range p {
			sum += float64(v)
		}
		mean := sum / float64(len(p))
		if mean <= 1e-6 {
			return false
		}
		inv := float32(1.0 / mean)
		for i := range p {
			p[i] *= inv
		}
	}
	return true
}

// encodeSRGBImage applies the sRGB transfer (linear → display) in place — the inverse of
// linearizeSRGB — so a calibrated linear frame is re-encoded to the gamma the rest of the pipeline
// expects (it linearizes again at compose time).
func encodeSRGBImage(im *fits.Image) {
	for c := 0; c < im.C; c++ {
		p := im.Pix[c]
		for i := range p {
			p[i] = float32(encodeSRGB(float64(p[i])))
		}
	}
}

// medianInPlace returns the median of v, partially sorting v (the caller reuses it as scratch). Uses
// an insertion sort — v is small (one value per calibration frame).
func medianInPlace(v []float32) float32 {
	for i := 1; i < len(v); i++ {
		x := v[i]
		j := i - 1
		for j >= 0 && v[j] > x {
			v[j+1] = v[j]
			j--
		}
		v[j+1] = x
	}
	n := len(v)
	if n%2 == 1 {
		return v[n/2]
	}
	return 0.5 * (v[n/2-1] + v[n/2])
}

// joinNotes concatenates the non-empty notes with "; ".
func joinNotes(parts ...string) string {
	out := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		if out != "" {
			out += "; "
		}
		out += p
	}
	return out
}
