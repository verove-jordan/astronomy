package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
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
type processedGroup struct {
	JobID       int64           `json:"job_id"`
	Kind        string          `json:"kind"`
	Object      string          `json:"object,omitempty"`
	Mode        string          `json:"mode,omitempty"`
	Format      string          `json:"format,omitempty"`
	Status      string          `json:"status"`
	CreatedAtMs int64           `json:"created_at_ms"`
	Paths       []processedPath `json:"paths"`
}

// processed reports, per past job, the capture folders it processed (with on-disk existence). GET /api/processed
func (s *Server) processed(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.store.ListJobs(r.Context(), 500, 0)
	if err != nil {
		serverError(w, err)
		return
	}
	exists := dirExistsCache() // a folder is shared by many jobs — stat it only once per request
	dataAbs, _ := filepath.Abs(s.cfg.DataDir)

	q := r.URL.Query()
	bucket, userPrefix := q.Get("bucket"), q.Get("prefix")

	// Pass 1: build groups with the local truth (os.Stat) and each folder's DataDir-rel. Collect the unique
	// locally-absent rels so their S3-mirror presence can be resolved in one batched pass below.
	groups := make([]processedGroup, 0, len(jobs))
	absentRels := make(map[string]struct{})
	for _, j := range jobs {
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
		// Keep only real filesystem folders (skip the synthetic "s3://…" live-stacking key).
		dirs := make([]processedPath, 0, len(raw))
		for _, pp := range raw {
			if pp == "" || strings.HasPrefix(pp, "s3://") {
				continue
			}
			local := exists(pp)
			rel := relForData(dataAbs, pp)
			dirs = append(dirs, processedPath{Path: pp, Exists: local, Local: local, Rel: rel})
			if !local && rel != "" && bucket != "" {
				absentRels[rel] = struct{}{}
			}
		}
		if len(dirs) == 0 {
			continue
		}
		var res struct {
			Object string `json:"object"`
		}
		_ = json.Unmarshal(j.Result, &res)
		groups = append(groups, processedGroup{
			JobID:       j.ID,
			Kind:        j.Kind,
			Object:      res.Object,
			Mode:        p.Mode,
			Format:      p.Format,
			Status:      j.Status,
			CreatedAtMs: j.CreatedAt,
			Paths:       dirs,
		})
	}

	// Pass 2: for folders freed locally after an S3 push, treat presence on the S3 data mirror as still
	// existing. One batched, parent-grouped, warm-cached resolution (bounded by a timeout) instead of a
	// sequential ListDir per folder. The config honors ?conn= so the mirror is checked on the connection
	// the bucket was chosen under.
	if bucket == "" || len(absentRels) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
		return
	}
	cfg, err := s.s3ConfigForRequest(r)
	if err != nil || !cfg.Configured() {
		writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
		return
	}
	rels := make([]string, 0, len(absentRels))
	for rel := range absentRels {
		rels = append(rels, rel)
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	remote := s.remoteDirsExist(ctx, cfg, bucket, userPrefix, rels)
	cancel()
	for gi := range groups {
		for pi := range groups[gi].Paths {
			p := &groups[gi].Paths[pi]
			if !p.Local && remote[p.Rel] {
				p.Exists = true
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
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
