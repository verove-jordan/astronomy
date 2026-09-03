package nightscape

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/verove-jordan/astronomy/internal/calib"
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

// hasCalibration reports whether any calibration frames were supplied this run — an explicit folder
// or frames auto-detected among the input stills.
// phoneMasterPath returns a matched phone master's local file path, or "" when nothing matched.
func phoneMasterPath(m *calib.PhoneMaster) string {
	if m == nil {
		return ""
	}
	return m.Path
}

func hasCalibration(o Options) bool {
	return o.DarkDir != "" || o.FlatDir != "" || o.BiasDir != "" ||
		len(o.DarkFrames) > 0 || len(o.FlatFrames) > 0 || len(o.BiasFrames) > 0
}

// calibrateLights obtains the dark/flat/bias masters (built from this run's cal frames, else reused
// from the library) and calibrates each light FITS in seqDir in place, in LINEAR light: load the
// (gamma) light → linearize → subtract the dark (else the bias) → divide the normalized flat → clamp →
// re-encode gamma → save. Calibration is only valid in linear light, but the gamma round-trip keeps the
// downstream register + compose (which linearizes each frame again) byte-compatible with the
// uncalibrated path, so only this opt-in branch changes. Soft-fail: any problem returns a note and
// leaves the lights untouched, so a bad or empty calibration set never breaks the proven path. Offset ==
// bias; a dark already contains the bias, so the bias master is used only when no dark is supplied.
func calibrateLights(ctx context.Context, o Options, plan calPlan, seqDir string, lightNames []string) string {
	if len(lightNames) == 0 {
		return ""
	}
	// Read the reference light first: its developed dimensions complete the light key used to select
	// library masters, and it is the pixel-for-pixel template every master must match.
	ref, err := fits.ReadImage(lightNames[0])
	if err != nil {
		return "calibration skipped (read light: " + err.Error() + ")"
	}
	key := plan.light
	key.Width, key.Height = ref.W, ref.H
	sel := calib.MatchPhoneCalibration(key, plan.masters)
	// Pull the matched phone masters back from the S3 library mirror if their files are absent locally.
	o.ensureMasters(ctx, []string{phoneMasterPath(sel.Dark), phoneMasterPath(sel.Bias), phoneMasterPath(sel.Flat)})

	dark, dn := buildOrReusePhoneMaster(ctx, o, "dark", key, sel.Dark)
	bias, bn := buildOrReusePhoneMaster(ctx, o, "bias", key, sel.Bias)
	flat, fn := buildOrReusePhoneMaster(ctx, o, "flat", key, sel.Flat)
	notes := joinNotes(dn, bn, fn)
	if dark == nil && bias == nil && flat == nil {
		if notes != "" {
			return "calibration skipped (" + notes + ")"
		}
		return ""
	}

	// Masters must match the lights pixel-for-pixel; drop any that don't (e.g. cal frames shot at a
	// different resolution) so a mismatch degrades to "less calibration", never a crash.
	dark, notes = matchOrDrop(dark, ref, "dark", notes)
	bias, notes = matchOrDrop(bias, ref, "bias", notes)
	// The flat is reduced to its radial falloff BEFORE the size check, because that is what makes the
	// check pass: a radial model in normalised radius does not know how big the image is, so a flat
	// shot at 48 megapixels calibrates 12-megapixel lights without binning anything. It also throws
	// away the non-radial part, which on a phone flat is mostly a reflection of the phone's own camera
	// bump in the middle of the frame. See internal/calib/radialflat.go.
	if flat != nil && o.FlatRadialOnly {
		var note string
		flat, note = radialiseFlat(flat, ref)
		notes = joinNotes(notes, note)
	}
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

	done, mismatched := 0, 0
	for _, name := range lightNames {
		im, err := fits.ReadImage(name)
		if err != nil {
			return fmt.Sprintf("calibration aborted (read %s: %v); %d lights done", filepath.Base(name), err, done)
		}
		// The masters were matched against ref, ONE light. That is not the same as matching every
		// light: a folder holding two sensor resolutions passes the check on ref and then runs the
		// pixel math off the end of the smaller master. Leave such a frame uncalibrated and say so.
		if im.W != ref.W || im.H != ref.H || im.C != ref.C {
			mismatched++
			continue
		}
		normalizeADU(im) // Siril convert output is 0..65535 ADU; bring it to [0,1] before linearize
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
	if mismatched > 0 {
		summary += fmt.Sprintf("; %d light(s) left uncalibrated — they are not %dx%d like the rest of the set",
			mismatched, ref.W, ref.H)
	}
	if notes != "" {
		summary += "; " + notes
	}
	return summary
}

// buildMaster develops the raw frames (and links any FITS), converts them to FITS via Siril, and
// returns their per-channel, per-pixel median in linear light. Returns (nil, note) when the set is
// empty or the develop/convert/read fails — the caller treats a nil master as "this calibration type
// unavailable" and proceeds.
func buildMaster(ctx context.Context, o Options, raws []string, tag string) (*fits.Image, string) {
	if len(raws) == 0 {
		return nil, ""
	}
	tmp := filepath.Join(o.WorkDir, "cal_"+tag)
	if err := fsutil.EnsureDir(tmp); err != nil {
		return nil, tag + ": " + err.Error()
	}
	capped := ""
	if len(raws) > maxCalFrames {
		capped = fmt.Sprintf("%s: used %d of %d frames (cap)", tag, maxCalFrames, len(raws))
		raws = raws[:maxCalFrames]
	}
	if err := stageCalFrames(ctx, raws, tmp); err != nil {
		return nil, tag + ": " + err.Error()
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

// stageCalFrames prepares calibration frames into dir for Siril `convert`: raws are developed to TIFF
// (sips → PrepareTIFF), FITS frames are linked in place.
func stageCalFrames(ctx context.Context, frames []string, dir string) error {
	var raws, fitsFrames []string
	for _, p := range frames {
		if isFITSPath(p) {
			fitsFrames = append(fitsFrames, p)
		} else {
			raws = append(raws, p)
		}
	}
	if len(raws) > 0 {
		if _, _, err := rawconv.PrepareTIFF(ctx, raws, dir, nil); err != nil {
			return fmt.Errorf("develop: %w", err)
		}
	}
	if len(fitsFrames) > 0 {
		if _, err := fsutil.LinkFrames(dir, fitsFrames); err != nil {
			return fmt.Errorf("stage: %w", err)
		}
	}
	return nil
}

func isFITSPath(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".fits", ".fit", ".fts":
		return true
	}
	return false
}

// calFrames returns the calibration raws for a role: the frames auto-detected among the input stills
// plus any frames under the explicitly-supplied folder, de-duplicated (a folder the user points at may
// overlap the input dir).
func calFrames(o Options, tag string) []string {
	var dir string
	var frames []string
	switch tag {
	case "dark":
		dir, frames = o.DarkDir, o.DarkFrames
	case "bias":
		dir, frames = o.BiasDir, o.BiasFrames
	case "flat":
		dir, frames = o.FlatDir, o.FlatFrames
	}
	seen := make(map[string]bool)
	var out []string
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range frames {
		add(p)
	}
	if dir != "" {
		raws, _ := inspect.ListRawFrames(dir)
		for _, p := range raws {
			add(p)
		}
	}
	return out
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
		normalizeADU(im) // cal frames are Siril convert output (0..65535 ADU)
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
// An exactly-TRANSPOSED master (portrait vs landscape of the same sensor) is still dropped — the
// 90° direction is ambiguous from dims alone and a wrong guess would misplace the fixed-pattern
// noise, which is worse than no calibration — but the note names the real cause: an old raw
// developer baked the EXIF rotation into one side (dcraw_emu now runs -t 0, so re-developing the
// masters fixes it for good).
func matchOrDrop(m, ref *fits.Image, tag, notes string) (*fits.Image, string) {
	if m == nil {
		return nil, notes
	}
	if m.W == ref.H && m.H == ref.W && m.C == ref.C && m.W != m.H {
		return nil, joinNotes(notes, fmt.Sprintf(
			"%s master %dx%d is TRANSPOSED vs light %dx%d (orientation was baked by an old raw develop) — dropped; rebuild the master so both are sensor-native",
			tag, m.W, m.H, ref.W, ref.H))
	}
	if m.W != ref.W || m.H != ref.H || m.C != ref.C {
		return nil, joinNotes(notes, fmt.Sprintf("%s master %dx%d≠light %dx%d, dropped", tag, m.W, m.H, ref.W, ref.H))
	}
	return m, notes
}

// --- in-place pixel math ---
//
// Every loop below is bounded by the SHORTER of the two planes. Callers are expected to have matched
// dimensions already, and one of them once did not: a mismatched master ran the subtraction off the
// end and took the whole run down with it. A wrong picture can be seen and argued with; a panic
// cannot, so the bound stays even though it should never be reached.

func subtractImage(im, sub *fits.Image) {
	for c := 0; c < im.C && c < sub.C; c++ {
		p, s := im.Pix[c], sub.Pix[c]
		for i := 0; i < len(p) && i < len(s); i++ {
			p[i] -= s[i]
		}
	}
}

// divideImage divides im by the normalized flat, guarding against tiny/zero flat values (which would
// blow up to huge gains) by leaving such pixels unchanged.
func divideImage(im, flat *fits.Image) {
	for c := 0; c < im.C && c < flat.C; c++ {
		p, f := im.Pix[c], flat.Pix[c]
		for i := 0; i < len(p) && i < len(f); i++ {
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

// radialiseFlat replaces a master flat with the smooth lens falloff fitted to it, materialised at the
// lights' own size. It returns the original and a note if the fit could not be made — a flat that
// cannot be reduced is better handled by the size check that follows than by guessing.
func radialiseFlat(flat, ref *fits.Image) (*fits.Image, string) {
	lum := make([]float64, flat.W*flat.H)
	for c := 0; c < flat.C; c++ {
		for i, v := range flat.Pix[c] {
			lum[i] += float64(v)
		}
	}
	for i := range lum {
		lum[i] /= float64(flat.C)
	}
	prof := calib.RadialProfileOf(lum, flat.W, flat.H, radialFlatBins)
	v, err := calib.FitRadialVignette(prof, radialFlatFitFrom)
	if err != nil {
		return flat, "flat kept whole (" + err.Error() + ")"
	}
	return v.Image(ref.W, ref.H, ref.C), fmt.Sprintf(
		"flat reduced to its lens falloff (corner %.0f%% of centre, fit rms %.4f, centre extrapolated from r>%.2f)",
		100*v.At(1)/v.At(0), v.RMS, v.FitFrom)
}

const (
	// radialFlatBins is how finely the falloff is measured. Twenty bins over the half-diagonal is
	// about 100 pixels per bin on a 12-megapixel frame — far finer than a vignette varies.
	radialFlatBins = 20
	// radialFlatFitFrom is where the CLEAN data starts. Measured on a real iPhone flat set, the
	// reflection of the phone's camera bump reaches to about 0.45 of the half-diagonal; fitting
	// outside it and extrapolating inward is what keeps that reflection out of the model.
	radialFlatFitFrom = 0.45
)
