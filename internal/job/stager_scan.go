package job

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/s3store"
	"github.com/verove-jordan/astronomy/internal/store"
	"github.com/verove-jordan/astronomy/internal/transfer"
)

// fitsHeaderBlocks / fitsHeaderMaxBlocks bound the ranged read of a FITS primary header (2880-byte
// blocks): most headers fit in ~16 blocks (45 KB); a long header retries once with more before giving up.
const (
	fitsHeaderBlocks    = 16
	fitsHeaderMaxBlocks = 64
	fitsBlock           = 2880
)

// Scan builds the run inventory from S3 WITHOUT downloading the captures: it enumerates each capture
// folder's objects from the s3_objects ledger, pulls only the tiny sidecars (info.txt, SharpCap metadata),
// classifies every frame through a ladder (catalog rows → ranged FITS-header read → filename tokens), and
// assembles the inventory with the same post-walk grouping as a local scan. If any frame cannot be
// classified remotely (needs pixel statistics), or a folder has no ledger rows (a legacy upload), it falls
// back to the classic full download + local scan for the whole run — so the plan is never wrong, only
// (rarely) less disk-efficient.
func (s *s3Stager) Scan(ctx context.Context, roots []string, opts inspect.ScanOptions) (*inspect.Inventory, error) {
	client, err := s.m.s3Client(ctx)
	if err != nil {
		return nil, err
	}
	framesByRoot := make(map[string][]*inspect.Frame, len(roots))
	for _, rootAbs := range roots {
		rel, rerr := filepath.Rel(s.dataDir, rootAbs)
		if rerr != nil || strings.HasPrefix(rel, "..") {
			return s.fullPullScan(ctx, roots, opts) // outside DataDir → can't remote-scan
		}
		rel = filepath.ToSlash(rel)
		rows, lerr := s.m.store.ListS3ObjectsUnder(ctx, s.bucket, s.prefix, rel)
		if lerr != nil || len(rows) == 0 {
			return s.fullPullScan(ctx, roots, opts) // no ledger (legacy/empty) → full pull
		}
		if serr := s.stageSidecars(ctx, client, rel, rows); serr != nil {
			return nil, serr
		}
		frames, needFull := s.classifyRows(ctx, client, rows)
		if needFull {
			return s.fullPullScan(ctx, roots, opts)
		}
		framesByRoot[rootAbs] = frames
	}
	inv := inspect.AssembleInventory(ctx, roots, framesByRoot, opts)
	inv.Warnings = append(inv.Warnings, fmt.Sprintf(
		"low-disk: planned %d capture folder(s) from S3 headers — no captures downloaded yet", len(roots)))
	return inv, nil
}

// classifyRows turns a folder's ledger rows into inspect frames via the ladder, and reports whether any
// frame needs a full download (an Unknown type, or a mono light with no filter — both need pixel stats).
func (s *s3Stager) classifyRows(ctx context.Context, client *s3store.Client, rows []store.S3Object) ([]*inspect.Frame, bool) {
	// Catalog fast-path: full classification for any previously-processed frame, with zero S3 reads.
	localOf := func(r store.S3Object) string { return filepath.Join(s.dataDir, filepath.FromSlash(r.LocalRel)) }
	var localPaths []string
	for _, r := range rows {
		if isFrameKey(r.S3Key) {
			localPaths = append(localPaths, localOf(r))
		}
	}
	cat := map[string]store.FrameRow{}
	if s.m.store != nil {
		if catRows, err := s.m.store.FramesByPaths(ctx, localPaths); err == nil {
			for _, fr := range catRows {
				cat[fr.Path] = fr
			}
		}
	}

	var frames []*inspect.Frame
	for _, r := range rows {
		if !isFrameKey(r.S3Key) {
			continue // sidecars/non-frames were staged locally already; not inventory frames
		}
		localPath := localOf(r)
		switch {
		case len(cat) > 0 && hasRow(cat, localPath):
			frames = append(frames, frameFromRow(cat[localPath]))
		default:
			if fr := s.frameFromRangedHeader(ctx, client, localPath, r.S3Key); fr != nil {
				frames = append(frames, fr)
			} else { // no readable header → filename/folder tokens only
				fr := &inspect.Frame{Path: localPath, Type: inspect.Unknown, BinX: 1, BinY: 1}
				inspect.ApplyPathMeta(fr, localPath)
				frames = append(frames, fr)
			}
		}
	}
	for _, fr := range frames {
		// A frame the header/tokens left Unknown, or a MONO light with no filter, would need the pixels
		// (stats classification / signal channel detection) a remote scan can't read → full pull.
		if fr.Type == inspect.Unknown || (fr.Type == inspect.Light && fr.Filter == "" && fr.Bayer == "") {
			return frames, true
		}
	}
	return frames, false
}

// frameFromRangedHeader reads just the FITS primary header via a byte-range GET and classifies it, growing
// the range once if the header is longer than the first read. nil when the object has no readable header.
func (s *s3Stager) frameFromRangedHeader(ctx context.Context, client *s3store.Client, localPath, key string) *inspect.Frame {
	for _, blocks := range []int64{fitsHeaderBlocks, fitsHeaderMaxBlocks} {
		data, err := client.ReadRange(ctx, s.bucket, key, 0, blocks*fitsBlock)
		if err != nil || len(data) < fitsBlock {
			return nil
		}
		if h, _, herr := fits.ReadHeaderFrom(bytes.NewReader(data)); herr == nil {
			return inspect.FrameFromHeader(localPath, h)
		}
		// herr (short read: header longer than the range) → retry with more blocks.
	}
	return nil
}

// stageSidecars downloads a folder's small non-frame files (info.txt manifests, SharpCap sidecars) so the
// manifest back-fill + sidecar reads work during classification. It is a plain subset download.
func (s *s3Stager) stageSidecars(ctx context.Context, client *s3store.Client, root string, rows []store.S3Object) error {
	var plan []transfer.PlannedFile
	for _, r := range rows {
		if isFrameKey(r.S3Key) {
			continue
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(r.LocalRel, root), "/")
		if rel == "" {
			continue
		}
		plan = append(plan, transfer.PlannedFile{Rel: rel, Key: r.S3Key, Size: r.Size})
	}
	if len(plan) == 0 {
		return nil
	}
	req := s.baseReq(transfer.OpDownload, root, plan, false)
	_, err := s.m.runTransferReq(ctx, s.jobID, client, req, "Staging sidecars")
	return err
}

// fullPullScan is the fallback: download every capture folder (the classic full-S3 pull) and scan locally.
// A run that lands here still frees per channel during the run (peak drops after the pull), just without
// the remote-plan disk win.
func (s *s3Stager) fullPullScan(ctx context.Context, roots []string, opts inspect.ScanOptions) (*inspect.Inventory, error) {
	for _, rel := range s.roots {
		tr := &TransferRequest{Op: "download", Bucket: s.bucket, Prefix: s.prefix, Namespace: "data", RelPath: rel}
		if _, err := s.m.execTransfer(ctx, s.jobID, tr, "Downloading inputs"); err != nil {
			return nil, fmt.Errorf("low-disk: full-pull fallback %s: %w", rel, err)
		}
	}
	inv, err := inspect.ScanMany(ctx, roots, opts)
	if err != nil {
		return nil, err
	}
	inv.Warnings = append(inv.Warnings,
		"low-disk: could not classify all frames from S3 headers — fell back to a full download for this run")
	s.notes = append(s.notes, "low-disk: full download fallback (a frame could not be classified remotely)")
	return inv, nil
}

// frameFromRow converts a catalogued frame row into an inspect Frame at its (to-be-staged) local path.
func frameFromRow(r store.FrameRow) *inspect.Frame {
	bin := r.Bin
	if bin <= 0 {
		bin = 1
	}
	return &inspect.Frame{
		Path: r.Path, Type: inspect.FrameType(r.FrameType), Filter: r.Filter,
		ExposureMs: r.ExposureMs, Gain: r.Gain, Offset: r.Offset,
		BinX: bin, BinY: bin, TempMilliC: r.TempMilliC, HasTemp: r.HasTemp,
		DateObsMs: r.DateObsMs, ClassSource: inspect.SourceHeader,
	}
}

func hasRow(cat map[string]store.FrameRow, path string) bool { _, ok := cat[path]; return ok }

// isFrameKey reports whether an S3 key names a FITS frame (vs a sidecar/manifest). Low-disk mode is scoped
// to the mono-FITS deep-sky/nebula pipeline, so non-FITS objects in a capture folder are sidecars.
func isFrameKey(key string) bool {
	switch strings.ToLower(path.Ext(key)) {
	case ".fits", ".fit", ".fts":
		return true
	}
	return false
}
