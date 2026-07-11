package job

import (
	"context"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/s3layout"
	"github.com/verove-jordan/astronomy/internal/store"
	"github.com/verove-jordan/astronomy/internal/transfer"
)

// buildDataPlan computes the classified transfer plan + a row-recording callback for a data-namespace
// transfer. Uploads inspect+walk the local folder, classify it into darks/offsets/flats/lum, disambiguate
// colliding calibration sets, and join keys under the user prefix; downloads read the persisted mapping so
// a source folder's scattered files reassemble into their local tree. A nil plan → legacy mirror behaviour
// (an empty/unclassifiable folder, or no store).
func (m *Manager) buildDataPlan(ctx context.Context, tr *TransferRequest) ([]transfer.PlannedFile, func(string, string, int64)) {
	if m.store == nil || tr.Namespace != "data" {
		return nil, nil
	}
	if tr.Op == "download" {
		return m.downloadPlan(ctx, tr), nil
	}
	return m.uploadPlan(ctx, tr)
}

// uploadPlan classifies the local folder and returns its planned files + the ledger-recording callback.
func (m *Manager) uploadPlan(ctx context.Context, tr *TransferRequest) ([]transfer.PlannedFile, func(string, string, int64)) {
	folderAbs := filepath.Join(m.cfg.DataDir, filepath.FromSlash(tr.RelPath))
	files, sizes := walkPlanFiles(ctx, folderAbs)
	if len(files) == 0 {
		return nil, nil
	}
	plan := s3layout.Classify(tr.RelPath, files)
	full := m.disambiguate(ctx, tr, plan.Keys, plan.Date)

	planned := make([]transfer.PlannedFile, 0, len(files))
	for _, f := range files {
		planned = append(planned, transfer.PlannedFile{Rel: f.Rel, Key: full[f.Rel], Size: sizes[f.Rel]})
	}
	onStored := func(rel, key string, size int64) {
		row := store.S3Object{Bucket: tr.Bucket, Prefix: tr.Prefix, LocalRel: path.Join(tr.RelPath, rel), S3Key: key, Size: size}
		if err := m.store.UpsertS3Object(ctx, row); err != nil {
			m.publish(Event{Status: store.JobRunning, Step: "warn: could not record S3 mapping for " + rel})
		}
	}
	return planned, onStored
}

// downloadPlan reads the persisted local-rel → key mapping for the folder, mapping each row back to a
// folder-relative path. Empty → nil (a folder never uploaded classified falls back to the legacy pull).
func (m *Manager) downloadPlan(ctx context.Context, tr *TransferRequest) []transfer.PlannedFile {
	rows, err := m.store.ListS3ObjectsUnder(ctx, tr.Bucket, tr.Prefix, tr.RelPath)
	if err != nil || len(rows) == 0 {
		return nil
	}
	planned := make([]transfer.PlannedFile, 0, len(rows))
	for _, r := range rows {
		rel := strings.TrimPrefix(strings.TrimPrefix(r.LocalRel, tr.RelPath), "/")
		if rel == "" {
			continue
		}
		planned = append(planned, transfer.PlannedFile{Rel: rel, Key: r.S3Key, Size: r.Size})
	}
	return planned
}

// disambiguate joins the classified keys under the user prefix and, when an existing row already owns a
// planned key for a DIFFERENT file (two sessions' identically-named calib dirs), date-suffixes that whole
// calibration set so the sessions stay separate. rel → full key.
func (m *Manager) disambiguate(ctx context.Context, tr *TransferRequest, relKeys map[string]string, date string) map[string]string {
	full := make(map[string]string, len(relKeys))
	keys := make([]string, 0, len(relKeys))
	for rel, k := range relKeys {
		fk := path.Join(tr.Prefix, k)
		full[rel] = fk
		keys = append(keys, fk)
	}
	owners, err := m.store.S3KeyOwners(ctx, tr.Bucket, tr.Prefix, keys)
	if err != nil || len(owners) == 0 {
		return full
	}
	collide := map[string]bool{}
	for rel, fk := range full {
		set := calibSetPrefix(relKeys[rel])
		if set == "" {
			continue
		}
		if owner, ok := owners[fk]; ok && owner != path.Join(tr.RelPath, rel) {
			collide[set] = true
		}
	}
	if len(collide) == 0 {
		return full
	}
	for rel, ck := range relKeys {
		if set := calibSetPrefix(ck); set != "" && collide[set] {
			ck = suffixSet(ck, date)
		}
		full[rel] = path.Join(tr.Prefix, ck)
	}
	return full
}

// calibSetPrefix returns "<root>/<set>" for a calibration key (darks/offsets/flats), else "".
func calibSetPrefix(key string) string {
	parts := strings.SplitN(key, "/", 3)
	if len(parts) < 2 {
		return ""
	}
	switch parts[0] {
	case "darks", "offsets", "flats":
		return parts[0] + "/" + parts[1]
	}
	return ""
}

// suffixSet appends "_<date>" to the set segment of a calibration key (darks/set/f → darks/set_date/f).
func suffixSet(key, date string) string {
	parts := strings.SplitN(key, "/", 3)
	if len(parts) < 3 || date == "" {
		return key
	}
	return parts[0] + "/" + parts[1] + "_" + date + "/" + parts[2]
}

// walkPlanFiles lists every regular file under folderAbs (with size + mtime) and merges the inspector's
// per-frame classification, producing the s3layout.FileInfo slice + a rel → size map.
func walkPlanFiles(ctx context.Context, folderAbs string) ([]s3layout.FileInfo, map[string]int64) {
	var files []s3layout.FileInfo
	sizes := map[string]int64{}
	_ = filepath.WalkDir(folderAbs, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			if err == nil && d.IsDir() && strings.HasPrefix(d.Name(), ".") && p != folderAbs {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || strings.HasSuffix(d.Name(), ".part") {
			return nil
		}
		rel := relSlash(folderAbs, p)
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		files = append(files, s3layout.FileInfo{Rel: rel, MTimeMs: info.ModTime().UnixMilli()})
		sizes[rel] = info.Size()
		return nil
	})
	applyInspection(ctx, folderAbs, files)
	return files, sizes
}

// applyInspection fills each file's frame Type/Object/DateObsMs from an inspection of the folder (files
// the inspector doesn't recognise stay Unknown and inherit their directory's majority type in Classify).
func applyInspection(ctx context.Context, folderAbs string, files []s3layout.FileInfo) {
	inv, err := inspect.Scan(ctx, folderAbs)
	if err != nil || inv == nil {
		return
	}
	byRel := make(map[string]*inspect.Frame, len(inv.Frames))
	for _, fr := range inv.Frames {
		byRel[relSlash(folderAbs, fr.Path)] = fr
	}
	for i := range files {
		if fr := byRel[files[i].Rel]; fr != nil {
			files[i].Type = mapFrameType(fr.Type)
			files[i].Object = fr.Object
			files[i].DateObsMs = fr.DateObsMs
		}
	}
}

// mapFrameType maps an inspector frame type to the coarse s3layout class (darkflat calibrates flats →
// Flat; a video is a light-like capture → Light).
func mapFrameType(t inspect.FrameType) s3layout.FrameType {
	switch t {
	case inspect.Light, inspect.Video:
		return s3layout.Light
	case inspect.Dark:
		return s3layout.Dark
	case inspect.Bias:
		return s3layout.Bias
	case inspect.Flat, inspect.DarkFlat:
		return s3layout.Flat
	default:
		return s3layout.Unknown
	}
}

// relSlash is p relative to root, in slash form.
func relSlash(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return filepath.ToSlash(p)
	}
	return filepath.ToSlash(rel)
}
