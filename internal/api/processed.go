package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/store"
)

// processedPath is one capture folder of a past processing. Exists is whether it is still usable — on
// local disk OR on the S3 mirror — so the UI can cross out (and exclude from re-running) folders that are
// truly gone. Local distinguishes the two: false + Exists=true means the folder was freed locally after an
// S3 push and must be pulled back from the mirror before it can be inspected/re-run. Rel is the folder's
// DataDir-relative slash path — the authoritative ledger key the mirror pull must use (a client-side rel
// guess diverges for nested folders and misses the ledger).
type processedPath struct {
	Path   string `json:"path"`
	Exists bool   `json:"exists"`
	Local  bool   `json:"local"`
	Rel    string `json:"rel"`
}

// processedGroup is one past processing (a job) and the capture folders that fed it. The browser uses
// these to mark already-processed folders, to colour-group folders that shared a processing, and to
// offer the set for re-running (the Import "Processing history"). Mode/Format let a re-run pre-fill them.
// Signature is the backend-computed folder-set key (store.SelectionSignature) the frontend uses to dedup
// history rows and join saved-selection names/stars — never recomputed client-side.
type processedGroup struct {
	JobID       int64           `json:"job_id"`
	Kind        string          `json:"kind"`
	Object      string          `json:"object,omitempty"`
	Mode        string          `json:"mode,omitempty"`
	Format      string          `json:"format,omitempty"`
	Status      string          `json:"status"`
	CreatedAtMs int64           `json:"created_at_ms"`
	Signature   string          `json:"signature"`
	Paths       []processedPath `json:"paths"`
}

// selectionGroup is one saved (named/starred) selection riding along the processed report, with its
// folders annotated by the same existence machinery as the job groups — so an orphaned selection
// (its jobs pruned past the history window) still renders and re-runs like a live history row.
type selectionGroup struct {
	ID          int64           `json:"id"`
	Name        string          `json:"name"`
	Favorite    bool            `json:"favorite"`
	Signature   string          `json:"signature"`
	Mode        string          `json:"mode,omitempty"`
	Format      string          `json:"format,omitempty"`
	UpdatedAtMs int64           `json:"updated_at_ms"`
	Paths       []processedPath `json:"paths"`
}

// processed reports, per past job, the capture folders it processed (with on-disk existence). GET /api/processed
//
// A ?job_id= narrows the report to that single job — the task-detail page needs only its own group (to gate
// the "Remove local files" action), so it must not fan out ListJobs(500) + a per-folder S3 check over every
// job. Without job_id it covers the recent window (Tasks list + Import history, which annotate every folder).
func (s *Server) processed(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	bucket, userPrefix := q.Get("bucket"), q.Get("prefix")

	jobs, err := s.processedJobs(r.Context(), q.Get("job_id"))
	if err != nil {
		serverError(w, err)
		return
	}
	exists := dirExistsCache() // a folder is shared by many jobs — stat it only once per request
	dataAbs, _ := filepath.Abs(s.cfg.DataDir)

	// Pass 1: build one group per job with the local truth (os.Stat) and each folder's DataDir-rel. Collect
	// the unique locally-absent rels so their S3-mirror presence can be resolved in one batched pass below.
	groups := make([]processedGroup, 0, len(jobs))
	absentRels := make(map[string]struct{})
	for _, j := range jobs {
		g, absent, ok := buildProcessedGroup(j, exists, dataAbs, bucket)
		if !ok {
			continue
		}
		groups = append(groups, g)
		for _, rel := range absent {
			absentRels[rel] = struct{}{}
		}
	}

	// Saved selections ride along the full-window report only (the task-detail ?job_id= narrow query
	// doesn't need them), annotated with the same stat cache + batched S3 pass as the groups.
	var sels []selectionGroup
	if q.Get("job_id") == "" {
		sels = s.selectionGroups(r.Context(), exists, dataAbs, bucket, absentRels)
	}
	respond := func() {
		writeJSON(w, http.StatusOK, map[string]any{"groups": groups, "selections": sels})
	}

	// Pass 2: for folders freed locally after an S3 push, treat presence on the S3 data mirror as still
	// existing. One batched, parent-grouped, warm-cached resolution (bounded by a timeout) instead of a
	// sequential ListDir per folder. The config honors ?conn= so the mirror is checked on the connection
	// the bucket was chosen under.
	if bucket == "" || len(absentRels) == 0 {
		respond()
		return
	}
	cfg, err := s.s3ConfigForRequest(r)
	if err != nil || !cfg.Configured() {
		respond()
		return
	}
	rels := make([]string, 0, len(absentRels))
	for rel := range absentRels {
		rels = append(rels, rel)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	remote := s.remoteDirsExist(ctx, cfg, bucket, userPrefix, rels)
	cancel()
	applyRemoteExistence(groups, sels, remote)
	respond()
}

// selectionGroups loads the saved selections annotated with the shared per-request stat cache;
// their locally-absent rels join absentRels so the batched S3 pass covers them too. A read failure
// degrades to an unnamed history (logged) rather than failing the whole processed report.
func (s *Server) selectionGroups(ctx context.Context, exists func(string) bool, dataAbs, bucket string, absentRels map[string]struct{}) []selectionGroup {
	rows, err := s.store.ListSelections(ctx)
	if err != nil {
		log.Printf("processed: list saved selections: %v", err)
		return nil
	}
	sels := make([]selectionGroup, 0, len(rows))
	for _, row := range rows {
		var raw []string
		if err := json.Unmarshal(row.Paths, &raw); err != nil {
			log.Printf("processed: selection %d paths: %v", row.ID, err)
			continue
		}
		dirs, absent := annotatePaths(raw, exists, dataAbs, bucket)
		for _, rel := range absent {
			absentRels[rel] = struct{}{}
		}
		sels = append(sels, selectionGroup{
			ID: row.ID, Name: row.Name, Favorite: row.Favorite, Signature: row.Signature,
			Mode: row.Mode, Format: row.Format, UpdatedAtMs: row.UpdatedAt, Paths: dirs,
		})
	}
	return sels
}

// applyRemoteExistence marks locally-freed folders that the batched S3 pass found on the mirror as
// still existing, across both the job groups and the saved selections.
func applyRemoteExistence(groups []processedGroup, sels []selectionGroup, remote map[string]bool) {
	fix := func(paths []processedPath) {
		for i := range paths {
			p := &paths[i]
			if !p.Local && remote[p.Rel] {
				p.Exists = true
			}
		}
	}
	for gi := range groups {
		fix(groups[gi].Paths)
	}
	for si := range sels {
		fix(sels[si].Paths)
	}
}

// processedJobs resolves the job set the report covers: the single job named by jobID (the task-detail
// page), else the recent window. An empty jobID → the window. A jobID that is unparseable or names no job
// yields no jobs (an empty report), never a 500 — the detail page degrades to "no group" gracefully, exactly
// as the existing getJob handler treats a missing id as not-found rather than an error.
func (s *Server) processedJobs(ctx context.Context, jobID string) ([]store.Job, error) {
	if jobID == "" {
		return s.store.ListJobs(ctx, 500, 0)
	}
	id, err := strconv.ParseInt(jobID, 10, 64)
	if err != nil {
		return nil, nil
	}
	jb, err := s.store.GetJob(ctx, id)
	if err != nil {
		return nil, nil // not found (or a transient read) → empty report, not a server error
	}
	return []store.Job{*jb}, nil
}

// buildProcessedGroup builds one job's processed group: its real capture folders (skipping the synthetic
// "s3://…" live-stacking key) with local on-disk existence + DataDir-rel. It also returns those folders'
// DataDir-rels that are locally absent — the candidates for the batched S3-mirror existence pass, but only
// when a bucket is in play. ok is false when the job processed no real folder (nothing to report). exists is
// the shared per-request stat cache.
func buildProcessedGroup(j store.Job, exists func(string) bool, dataAbs, bucket string) (processedGroup, []string, bool) {
	var p struct {
		Path   string   `json:"path"`
		Paths  []string `json:"paths"`
		Mode   string   `json:"mode"`
		Format string   `json:"format"`
	}
	_ = json.Unmarshal(j.Params, &p)
	raw := p.Paths
	if len(raw) == 0 && p.Path != "" {
		raw = []string{p.Path}
	}
	dirs, absent := annotatePaths(raw, exists, dataAbs, bucket)
	if len(dirs) == 0 {
		return processedGroup{}, nil, false
	}
	kept := make([]string, len(dirs))
	for i, d := range dirs {
		kept[i] = d.Path
	}
	var res struct {
		Object string `json:"object"`
	}
	_ = json.Unmarshal(j.Result, &res)
	return processedGroup{
		JobID:       j.ID,
		Kind:        j.Kind,
		Object:      res.Object,
		Mode:        p.Mode,
		Format:      p.Format,
		Status:      j.Status,
		CreatedAtMs: j.CreatedAt,
		Signature:   store.SelectionSignature(kept),
		Paths:       dirs,
	}, absent, true
}

// annotatePaths maps raw capture-folder paths (skipping the synthetic "s3://…" live-stacking key) to
// their local existence + DataDir-rel, returning the locally-absent rels the batched S3-mirror pass
// should resolve (only when a bucket is in play). Shared by the job groups and the saved selections.
func annotatePaths(raw []string, exists func(string) bool, dataAbs, bucket string) ([]processedPath, []string) {
	dirs := make([]processedPath, 0, len(raw))
	var absent []string
	for _, pp := range raw {
		if pp == "" || strings.HasPrefix(pp, "s3://") {
			continue
		}
		local := exists(pp)
		rel := relForData(dataAbs, pp)
		dirs = append(dirs, processedPath{Path: pp, Exists: local, Local: local, Rel: rel})
		if !local && rel != "" && bucket != "" {
			absent = append(absent, rel)
		}
	}
	return dirs, absent
}

// relForData maps a capture folder's absolute path to its DataDir-relative slash path — the stable ledger
// key. Returns "" when the path escapes DataDir (never mirrored under data/).
func relForData(dataAbs, abs string) string {
	rel, err := filepath.Rel(dataAbs, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return ""
	}
	return filepath.ToSlash(rel)
}

// dirExistsCache returns a memoized "does this path exist on disk?" check, so a folder shared across
// many past jobs is stat-ed only once per request.
func dirExistsCache() func(string) bool {
	seen := make(map[string]bool)
	return func(path string) bool {
		if v, ok := seen[path]; ok {
			return v
		}
		_, err := os.Stat(path)
		v := err == nil
		seen[path] = v
		return v
	}
}
