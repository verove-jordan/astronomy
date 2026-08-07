// 3D field-map endpoint: serves the scene internal/scene3d builds from a run's star annotation —
// every detected star placed at its own distance, plus the catalogued objects in the field hung at
// theirs. The scene is NOT computed here: it is built as part of the star annotation
// (computeStars), because it needs exactly the detections, identifications and validated geometry
// that pass already produced. One detection pass, two artifacts, no way for them to disagree.
//
// Like stars.json the manifest stays local-only — a cheap derived cache. The heavy parts
// (scene3d.bin, scene3d_bg.png) are ordinary run files and go out through GET /api/file, which
// already handles the S3 pull-through for a run whose local copy was freed.
package api

import (
	"context"
	"log"
	"net/http"
	"path/filepath"
	"time"

	"github.com/verove-jordan/astronomy/internal/annotate"
	"github.com/verove-jordan/astronomy/internal/scene3d"
)

// buildScene renders the 3D scene for a freshly annotated run. Failure is logged and swallowed: the
// 3D view is an extra, and losing it must never cost the caller the star count it actually asked
// for.
func buildScene(res *annotate.Result, o scene3d.Options) {
	if _, err := scene3d.Build(res, o); err != nil {
		log.Printf("scene3d: build %s: %v", o.RunDir, err)
	}
}

// sceneBuildTimeout bounds an on-demand rebuild. It is pure Go over an already-computed annotation
// — no detection, no plate solve — and the only slow part is repainting the backdrop.
const sceneBuildTimeout = 60 * time.Second

// getScene3D returns the scene manifest for a run, building it on demand when it is missing or was
// written by an older version.
//
// Rebuilding inside a GET is deliberate. The scene is a pure function of the star annotation, which
// is already on disk, so a run that has stars can always be given a scene without re-detecting
// anything — and the alternative is what actually shipped: a run with 957 detected stars whose 3D
// view was silently absent because the artifact predated the feature. The result is cached, so this
// happens once per run.
//
// 404 only when the stars were never computed at all; a run that genuinely cannot have a scene
// answers 200 with available:false and a reason, so the viewer can say why.
// GET /api/jobs/{id}/scene3d
func (s *Server) getScene3D(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), sceneBuildTimeout)
	defer cancel()
	runDir, _, ok := s.starsRun(ctx, w, r)
	if !ok {
		return
	}
	if m, found := scene3d.Load(runDir); !sceneNeedsRebuild(m, found) {
		writeJSON(w, http.StatusOK, withRunPaths(m, runDir))
		return
	}

	res, found := annotate.Load(runDir)
	if !found {
		writeJSON(w, http.StatusNotFound,
			map[string]string{"error": "stars not computed yet", "code": "scene3d_not_computed"})
		return
	}
	// Its own singleflight key: a rebuild must not queue behind a full star recompute on the same
	// run, and the two write the same files either way.
	v, err, _ := s.starsFlight.Do(runDir+"|scene3d", func() (any, error) {
		return scene3d.Build(res, scene3d.Options{
			RunDir: runDir,
			Locate: func(rel string) (string, bool) {
				return s.ensureServable(ctx, r, filepath.Join(runDir, rel), s.cfg.OutputDir, "output")
			},
		})
	})
	if err != nil {
		log.Printf("scene3d: rebuild %s: %v", runDir, err)
		serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, withRunPaths(v.(*scene3d.Manifest), runDir))
}

// sceneNeedsRebuild decides whether a cached manifest can be served as it stands.
//
// A version older than the current one must be rebuilt rather than served: the viewer's decoder
// refuses a record layout it does not recognise, so handing it a stale file moves the failure into
// the browser, where it reads as a broken view rather than as an out-of-date cache.
func sceneNeedsRebuild(m *scene3d.Manifest, found bool) bool {
	return !found || m == nil || m.Version != scene3d.ManifestVersion
}

// withRunPaths resolves the manifest's run-relative artifact names into the full paths the browser
// fetches them by. They are stored relative so a run directory can be moved or restored from S3
// without rewriting the file; they are served absolute because that is what GET /api/file takes.
func withRunPaths(m *scene3d.Manifest, runDir string) *scene3d.Manifest {
	out := *m
	if out.Points != "" {
		out.Points = filepath.Join(runDir, m.Points)
	}
	if out.Backdrop != "" {
		out.Backdrop = filepath.Join(runDir, m.Backdrop)
	}
	return &out
}
