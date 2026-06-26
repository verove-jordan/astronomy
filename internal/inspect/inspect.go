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

// statsSample is how many center pixels to read when inferring a missing IMAGETYP.
const statsSample = 50000

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

// ScanWithOptions walks root, reads FITS metadata, classifies and groups every frame, and — when
// enabled — infers filters from the signal for unlabeled lights and flags filter-wheel transitions.
func ScanWithOptions(ctx context.Context, root string, opts ScanOptions) (*Inventory, error) {
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
			if path != root && strings.HasPrefix(d.Name(), ".") {
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
	inv.Sets = buildSets(inv.Frames)
	if len(opts.FilterMapping) > 0 {
		ApplyFilterMapping(inv, opts.FilterMapping) // re-groups sets with the override applied
	}
	addWarnings(inv)
	return inv, nil
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

	// Fill anything the header lacked from the filename/folder (common for ASI captures that omit
	// IMAGETYP/FILTER and encode them in the name, e.g. Light_..._filter-B_-20.0C_gain300_...).
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
	return fr, nil
}

func exposureSec(h *fits.Header) (float64, bool) {
	if v, ok := h.Float("EXPTIME"); ok {
		return v, true
	}
	return h.Float("EXPOSURE")
}

// classifyUnknowns assigns a type to frames that lacked IMAGETYP, using a sampled mean ADU.
func classifyUnknowns(inv *Inventory, unknown []*Frame) {
	if len(unknown) == 0 {
		return
	}
	minExp := int64(math.MaxInt64)
	for _, fr := range unknown {
		if fr.ExposureMs < minExp {
			minExp = fr.ExposureMs
		}
	}
	for _, fr := range unknown {
		var mean float64
		if f, err := fits.Open(fr.Path); err == nil {
			if st, serr := f.Stats(statsSample); serr == nil {
				mean = st.Mean
			}
		}
		fr.Type = classifyHeuristic(mean, fr.ExposureMs, minExp)
		fr.ClassSource = SourceHeuristic
		inv.Warnings = append(inv.Warnings, fmt.Sprintf(
			"%s has no IMAGETYP; classified as %s by heuristic (mean ADU %.0f)",
			rel(inv.Root, fr.Path), fr.Type, mean))
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
