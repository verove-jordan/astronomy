package inspect

import (
	"context"
	"fmt"
	"io/fs"
	"math"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/channeldetect"
	"github.com/verove-jordan/astronomy/internal/fits"
)

var (
	fitsExts  = map[string]bool{".fits": true, ".fit": true, ".fts": true}
	videoExts = map[string]bool{".ser": true, ".avi": true, ".mp4": true, ".mov": true, ".mkv": true, ".m4v": true}
	// cameraRawExts are one-shot-color camera raws (iPhone DNG/HEIC, DSLR raws). They are surfaced
	// as lights only when a directory holds no FITS/video, so stray jpg/png outputs in a FITS capture
	// set are never miscounted — which is why those non-raw still formats are excluded here.
	cameraRawExts = map[string]bool{
		".dng": true, ".heic": true, ".heif": true,
		".cr2": true, ".cr3": true, ".nef": true, ".arw": true, ".raf": true,
	}
)

// statsSample is how many center pixels to read when inferring a missing IMAGETYP from the pixel
// curve. Large enough that a real star field shows many peaks, yet a cheap bounded center read.
const statsSample = 200000

// ScanOptions tunes a scan: signal-based channel detection (for unlabeled captures) and an
// optional filter override (detected/known filter → chosen channel; "" or "ignore" excludes it).
type ScanOptions struct {
	DetectChannels bool
	Channel        channeldetect.Options
	FilterMapping  map[string]string
}

// DefaultScanOptions enables signal-based detection with robust default thresholds.
func DefaultScanOptions() ScanOptions {
	return ScanOptions{DetectChannels: true, Channel: channeldetect.DefaultOptions()}
}

// Scan walks root recursively with default options (channel detection enabled).
func Scan(ctx context.Context, root string) (*Inventory, error) {
	return ScanWithOptions(ctx, root, DefaultScanOptions())
}

// scanFrames walks root, reads FITS metadata, classifies every frame, and — when enabled — infers
// filters from the signal for unlabeled lights. It returns the classified frames/videos plus any
// per-file scan warnings, but does NOT build sets, apply the filter override, or add completeness
// warnings: those finalize once (across all roots for a merged multi-directory scan), so a
// lights-only folder never emits a false "no darks" when its calibration lives in a sibling folder.
func scanFrames(ctx context.Context, root string, opts ScanOptions) (*Inventory, error) {
	inv := &Inventory{Root: root}
	var unknown []*Frame
	var rawStills []string // camera raws; promoted to lights only if the dir has no FITS/video

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			// Skip hidden dirs and our own run-output tree so processed results / previews are never
			// re-ingested as captures (the pipeline writes to an "output" directory).
			if path != root && (strings.HasPrefix(d.Name(), ".") || strings.EqualFold(d.Name(), "output")) {
				return fs.SkipDir
			}
			return nil
		}
		switch ext := strings.ToLower(filepath.Ext(path)); {
		case fitsExts[ext]:
			fr, ferr := readFITSFrame(path)
			if ferr != nil {
				inv.Warnings = append(inv.Warnings, fmt.Sprintf("skip %s: %v", rel(root, path), ferr))
				return nil
			}
			inv.Frames = append(inv.Frames, fr)
			if fr.Type == Unknown {
				unknown = append(unknown, fr)
			}
		case videoExts[ext]:
			inv.Videos = append(inv.Videos, &Frame{Path: path, Type: Video, ClassSource: SourceExtension})
		case cameraRawExts[ext]:
			rawStills = append(rawStills, path)
		}
		return nil
	})
	if walkErr != nil {
		return nil, fmt.Errorf("scan %s: %w", root, walkErr)
	}

	// Back-fill filter/gain/exposure from any info.txt sidecars before classification, so bare-filename
	// legacy captures are labeled and signal detection only runs on whatever the manifest could not map.
	applyManifests(ctx, root, inv)
	// Name any remaining slot-bearing lights (sibling folders with no info.txt) by the default wheel
	// order, then surface any slot that had no name legend. Both run before signal detection so a frame
	// with a known physical filter never goes to the guess-prone detector.
	nameRemainingWheelSlots(inv)
	warnUnnamedWheelSlots(inv)
	unknown = unknown[:0]
	for _, fr := range inv.Frames {
		if fr.Type == Unknown {
			unknown = append(unknown, fr)
		}
	}

	classifyUnknowns(inv, unknown)
	if opts.DetectChannels {
		chOpts := opts.Channel
		if len(chOpts.Order) == 0 {
			chOpts = channeldetect.DefaultOptions()
		}
		processChannels(inv, chOpts)
	}
	// A one-shot-color raw directory (iPhone/DSLR) carries no FITS metadata; surface its stills as
	// RGB lights so the inventory is non-empty and the UI can offer milkyway mode. Done only when the
	// dir holds no FITS/video, so FITS capture sets (and stray jpg/png outputs) are unaffected.
	if len(inv.Frames) == 0 && len(inv.Videos) == 0 && len(rawStills) > 0 {
		for _, p := range rawStills {
			inv.Frames = append(inv.Frames, &Frame{Path: p, Type: Light, Filter: "RGB", ClassSource: SourceExtension})
		}
	}
	return inv, nil
}

// ScanWithOptions walks root, reads FITS metadata, classifies and groups every frame, and — when
// enabled — infers filters from the signal for unlabeled lights and flags filter-wheel transitions.
func ScanWithOptions(ctx context.Context, root string, opts ScanOptions) (*Inventory, error) {
	inv, err := scanFrames(ctx, root, opts)
	if err != nil {
		return nil, err
	}
	finalizeInventory(inv, opts)
	return inv, nil
}

// finalizeInventory groups frames into sets, applies an optional filter override, and adds
// completeness warnings — the steps that must run once over the full (possibly multi-root) frame set.
func finalizeInventory(inv *Inventory, opts ScanOptions) {
	clearSpuriousBayer(inv)
	inv.Sets = buildSets(inv.Frames)
	if len(opts.FilterMapping) > 0 {
		ApplyFilterMapping(inv, opts.FilterMapping) // re-groups sets with the override applied
	}
	addWarnings(inv)
}

// clearSpuriousBayer drops the Bayer (one-shot-color) flag from frames that cannot actually be CFA.
// Older ASICAP/ZWO captures write a BAYERPAT card even for MONO cameras (e.g. the ASI 1600MM Pro): the
// mono per-filter pipeline would then route every frame to the OSC path, ExcludeBayer would drop the
// whole session, and the job "succeeds" in milliseconds with "no channels to combine". A one-shot-color
// camera is incompatible with a filter wheel, so a frame carrying a mono filter or a physical wheel slot
// proves the rig is monochrome — and its calibration frames (which carry no filter) belong to that same
// rig. We therefore clear Bayer on (a) every filter-wheel exposure and (b) the calibration frames,
// whenever the session shows any filter-wheel evidence. Genuine OSC sessions (no mono filter, no wheel
// slot anywhere) are left untouched, as are unfiltered colour lights dropped into a mono folder — both
// keep their Bayer flag and are still excluded from the mono pipeline downstream.
func clearSpuriousBayer(inv *Inventory) {
	monoRig := false
	for _, fr := range inv.Frames {
		if fr.WheelSlot > 0 || isMonoFilter(fr.Filter) {
			monoRig = true
			break
		}
	}
	if !monoRig {
		return
	}
	cleared := 0
	for _, fr := range inv.Frames {
		if fr.Bayer == "" {
			continue
		}
		if fr.WheelSlot > 0 || isMonoFilter(fr.Filter) || isCalibration(fr.Type) {
			fr.Bayer = ""
			cleared++
		}
	}
	if cleared > 0 {
		inv.Warnings = append(inv.Warnings, fmt.Sprintf(
			"%d frame(s) carry a BAYERPAT card but the session uses a filter wheel (mono rig) — "+
				"treating them as monochrome, not one-shot-color", cleared))
	}
}

// isMonoFilter reports whether f names a single mono filter-wheel slot (L/R/G/B/Ha/Sii/Oiii/…), as
// opposed to the empty/one-shot-color label ("", "RGB", "OSC", "COLOR").
func isMonoFilter(f string) bool {
	switch strings.ToUpper(strings.TrimSpace(f)) {
	case "", "RGB", "OSC", "COLOR", "COLOUR":
		return false
	default:
		return true
	}
}

// ScanMany scans multiple roots and merges them into one Inventory: frames, videos, and per-file
// scan warnings are concatenated; sets and completeness warnings are computed once across the union
// so calibration frames in one folder satisfy lights in another. A single root is byte-identical to
// ScanWithOptions; Root is set to the roots' common parent (display only). Channel detection stays
// per-root (each folder is its own filter-wheel run).
func ScanMany(ctx context.Context, roots []string, opts ScanOptions) (*Inventory, error) {
	switch len(roots) {
	case 0:
		return nil, fmt.Errorf("scan: no directories given")
	case 1:
		return ScanWithOptions(ctx, roots[0], opts)
	}
	merged := &Inventory{Root: commonParent(roots)}
	for _, root := range roots {
		inv, err := scanFrames(ctx, root, opts)
		if err != nil {
			return nil, err
		}
		merged.Frames = append(merged.Frames, inv.Frames...)
		merged.Videos = append(merged.Videos, inv.Videos...)
		merged.Warnings = append(merged.Warnings, inv.Warnings...)
		if merged.ChannelDetection == nil {
			merged.ChannelDetection = inv.ChannelDetection
		}
	}
	finalizeInventory(merged, opts)
	return merged, nil
}

// commonParent returns the longest shared directory prefix of the given (cleaned) paths. Used only
// as the display Root of a merged multi-directory scan.
func commonParent(roots []string) string {
	if len(roots) == 0 {
		return ""
	}
	sep := string(filepath.Separator)
	parts := strings.Split(filepath.Clean(roots[0]), sep)
	for _, r := range roots[1:] {
		other := strings.Split(filepath.Clean(r), sep)
		n := len(parts)
		if len(other) < n {
			n = len(other)
		}
		i := 0
		for i < n && parts[i] == other[i] {
			i++
		}
		parts = parts[:i]
	}
	switch {
	case len(parts) == 0:
		return ""
	case len(parts) == 1 && parts[0] == "":
		return sep // common prefix is the filesystem root
	default:
		return strings.Join(parts, sep)
	}
}

// readFITSFrame opens one FITS file and extracts the metadata we care about.
func readFITSFrame(path string) (*Frame, error) {
	f, err := fits.Open(path)
	if err != nil {
		return nil, err
	}
	h := f.Header
	fr := &Frame{Path: path, Type: Unknown, BinX: 1, BinY: 1}

	if v, ok := h.String("FILTER"); ok {
		fr.Filter = normalizeFilter(v)
	}
	if sec, ok := exposureSec(h); ok {
		fr.ExposureMs = int64(math.Round(sec * 1000))
	}
	if g, ok := h.Int("GAIN"); ok {
		fr.Gain = g
	}
	if o, ok := h.Int("OFFSET"); ok {
		fr.Offset = o
	}
	if t, ok := h.Float("CCD-TEMP"); ok {
		fr.TempMilliC = int64(math.Round(t * 1000))
		fr.HasTemp = true
	}
	if b, ok := h.Int("XBINNING"); ok && b > 0 {
		fr.BinX, fr.BinY = int(b), int(b)
	}
	if b, ok := h.Int("YBINNING"); ok && b > 0 {
		fr.BinY = int(b)
	}
	if v, ok := h.String("BAYERPAT"); ok {
		if b := strings.TrimSpace(v); b != "" && !strings.EqualFold(b, "NONE") {
			fr.Bayer = strings.ToUpper(b) // one-shot-color; the mono pipeline excludes these
		}
	}
	fr.Width, fr.Height = f.Dimensions()
	fr.Object, _ = h.String("OBJECT")
	fr.Instrument, _ = h.String("INSTRUME")
	fr.Telescope, _ = h.String("TELESCOP")
	if v, ok := h.String("DATE-OBS"); ok {
		fr.DateObs = v
		fr.DateObsMs = parseDateObs(v)
	}
	if v, ok := h.Float("FOCALLEN"); ok {
		fr.FocalLenMM = v
	}
	if v, ok := h.Float("XPIXSZ"); ok {
		fr.PixelSizeUm = v
	}
	fr.ObjCtRA, _ = h.String("OBJCTRA")
	fr.ObjCtDec, _ = h.String("OBJCTDEC")
	if v, ok := h.String("IMAGETYP"); ok {
		if t := classifyImageType(v); t != Unknown {
			fr.Type = t
			fr.ClassSource = SourceHeader
		}
	}

	backfillMeta(fr, path)
	return fr, nil
}

// backfillMeta fills frame fields the FITS header lacked (common for ASI/ASICAP captures that omit
// IMAGETYP/FILTER) from the SharpCap sidecar and the filename/folder, and records the EFW wheel slot —
// from the sidecar first, then the filename. It only ever fills blanks, so header values always win.
func backfillMeta(fr *Frame, path string) {
	if side, ok := readSharpcapSidecar(path); ok {
		fr.WheelSlot = side.Slot
		if fr.ExposureMs == 0 && side.ExposureMs > 0 {
			fr.ExposureMs = side.ExposureMs
		}
		if fr.Gain == 0 && side.HasGain {
			fr.Gain = side.Gain
		}
		if !fr.HasTemp && side.HasTemp {
			fr.TempMilliC, fr.HasTemp = side.TempMilliC, true
		}
	}
	meta := parseFilenameMeta(path)
	if fr.Filter == "" {
		fr.Filter = meta.Filter
	}
	if fr.ExposureMs == 0 {
		fr.ExposureMs = meta.ExposureMs
	}
	if fr.Gain == 0 {
		fr.Gain = meta.Gain
	}
	if !fr.HasTemp && meta.HasTemp {
		fr.TempMilliC, fr.HasTemp = meta.TempMilliC, true
	}
	if fr.BinX <= 1 && meta.Bin > 0 {
		fr.BinX, fr.BinY = meta.Bin, meta.Bin
	}
	if fr.Type == Unknown && meta.Type != Unknown {
		fr.Type = meta.Type
		fr.ClassSource = SourceFilename
	}
	if fr.WheelSlot == 0 {
		fr.WheelSlot = meta.WheelSlot
	}
}

func exposureSec(h *fits.Header) (float64, bool) {
	if v, ok := h.Float("EXPTIME"); ok {
		return v, true
	}
	if v, ok := h.Float("EXPOSURE"); ok {
		return v, true
	}
	// ASICAP (older ZWO captures) records the exposure only as EXPOINUS, in microseconds.
	if us, ok := h.Float("EXPOINUS"); ok {
		return us / 1e6, true
	}
	return 0, false
}

// classifyUnknowns assigns a type to frames left Unknown by header/folder/filename, comparing each
// frame's pixel "curve" (brightness, noise, uniformity, peak count) across the session via
// classifyByStats. The stats are sampled once per frame and reused for the warning detail.
func classifyUnknowns(inv *Inventory, unknown []*Frame) {
	if len(unknown) == 0 {
		return
	}
	stats := make([]frameStat, len(unknown))
	for i, fr := range unknown {
		stats[i] = frameStat{exposureMs: fr.ExposureMs}
		f, err := fits.Open(fr.Path)
		if err != nil {
			continue
		}
		if st, serr := f.Stats(statsSample); serr == nil {
			stats[i].median = st.Median
			stats[i].mad = st.MAD
			stats[i].brightFrac = st.BrightFrac
			stats[i].peaks = st.Peaks
		}
	}
	types := classifyByStats(stats)
	for i, fr := range unknown {
		fr.Type = types[i]
		fr.ClassSource = SourceHeuristic
		if fr.Bayer != "" {
			continue // one-shot-color frames routinely omit IMAGETYP — not worth a per-frame warning
		}
		inv.Warnings = append(inv.Warnings, fmt.Sprintf(
			"%s has no IMAGETYP/type folder; classified as %s by curve heuristic (median %.0f, peaks %d)",
			rel(inv.Root, fr.Path), fr.Type, stats[i].median, stats[i].peaks))
	}
}

// addWarnings flags missing calibration categories that will degrade the result.
func addWarnings(inv *Inventory) {
	counts := inv.CountsByType()
	if counts[Light] == 0 && len(inv.Videos) == 0 {
		inv.Warnings = append(inv.Warnings, "no light frames or videos found")
		return
	}
	if counts[Light] == 0 {
		return
	}
	if counts[Dark] == 0 {
		inv.Warnings = append(inv.Warnings, "no darks found — dark calibration skipped unless a library master matches")
	}
	if counts[Flat] == 0 {
		inv.Warnings = append(inv.Warnings, "no flats found — vignetting/dust correction skipped unless a library master matches")
	}
	if counts[Bias] == 0 && counts[Dark] == 0 {
		inv.Warnings = append(inv.Warnings, "no bias or dark frames found — no read-noise calibration available")
	}
}

func rel(root, path string) string {
	if r, err := filepath.Rel(root, path); err == nil {
		return r
	}
	return path
}
