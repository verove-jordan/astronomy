// Remote inventory seams for the low-disk S3 processing mode: the classification steps of a scan,
// exported so the job layer can build a run's Inventory from RANGED FITS-header reads (fits.ReadHeaderFrom
// over the first few KB of each S3 object) + catalog rows + filename tokens — WITHOUT downloading the
// multi-MB captures. AssembleInventory then runs the exact same post-walk grouping as a local ScanMany,
// so the plan is byte-identical to a local scan for every header/manifest/token-classifiable frame; a
// frame that only pixel statistics could classify stays Unknown and the caller falls back to a full pull.
package inspect

import (
	"context"
	"math"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// FrameFromHeader builds a Frame from an already-parsed FITS primary header and the file's path, WITHOUT
// opening the file — the header can come from a ranged read of just the first few KB. It mirrors
// readFITSFrame's header extraction exactly, then back-fills blanks from the SharpCap sidecar (when a
// local copy exists) + the filename/folder tokens (backfillMeta reads no pixels). Type may stay Unknown
// when the header lacks IMAGETYP and the name gives no hint — the caller decides whether to fall back.
func FrameFromHeader(path string, h *fits.Header) *Frame {
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
	n1, _ := h.Int("NAXIS1")
	n2, _ := h.Int("NAXIS2")
	fr.Width, fr.Height = int(n1), int(n2)
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
	return fr
}

// ApplyPathMeta back-fills a frame's blank fields from its SharpCap sidecar (when present locally) and
// its filename/folder tokens — header values are never overwritten (backfillMeta reads no pixels).
// Exported for the remote scan's token-only rung: a frame whose header could not be read still gets a
// type/filter from its name. Build the frame with Type=Unknown and BinX/BinY=1, then call this.
func ApplyPathMeta(fr *Frame, path string) { backfillMeta(fr, path) }

// AssembleInventory builds a finalized Inventory from frames already classified per root (by the remote
// scan's ladder), running the SAME post-walk steps as a local scan — the manifest (info.txt) back-fill +
// wheel-slot naming per root, then the single cross-root finalize (sets, filter override, completeness
// warnings) — so the grouping is byte-identical to ScanMany over the same frames. It does NOT run pixel
// classification or channel detection (no pixels available); frames left Unknown are the caller's signal
// to fall back to a full download. framesByRoot maps each root to the frames found under it (the manifest
// + wheel-naming steps are per-root). roots order sets a stable merge order.
func AssembleInventory(ctx context.Context, roots []string, framesByRoot map[string][]*Frame, opts ScanOptions) *Inventory {
	root := ""
	switch len(roots) {
	case 0:
		root = ""
	case 1:
		root = roots[0]
	default:
		root = commonParent(roots)
	}
	merged := &Inventory{Root: root}
	for _, r := range roots {
		inv := &Inventory{Root: r, Frames: framesByRoot[r]}
		applyManifests(ctx, r, inv)
		nameRemainingWheelSlots(inv)
		warnUnnamedWheelSlots(inv)
		merged.Frames = append(merged.Frames, inv.Frames...)
		merged.Warnings = append(merged.Warnings, inv.Warnings...)
	}
	finalizeInventory(merged, opts)
	return merged
}
