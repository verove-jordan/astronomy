package api

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/scene3d"
)

func TestSceneNeedsRebuild(t *testing.T) {
	tests := []struct {
		name  string
		m     *scene3d.Manifest
		found bool
		want  bool
	}{
		{"never computed", nil, false, true},
		{"found but nil", nil, true, true},
		{"current version is served as-is", &scene3d.Manifest{Version: scene3d.ManifestVersion}, true, false},
		// The regression this pins: a run whose scene was cached by an older build must be rebuilt.
		// Serving it instead pushes the failure into the browser, where the decoder refuses the
		// record layout and the view simply looks broken.
		{"older version is rebuilt", &scene3d.Manifest{Version: scene3d.ManifestVersion - 1}, true, true},
		{"unversioned is rebuilt", &scene3d.Manifest{}, true, true},
		{"an unavailable scene of the current version is still served", &scene3d.Manifest{
			Version: scene3d.ManifestVersion, Available: false, Reason: "no solve",
		}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sceneNeedsRebuild(tt.m, tt.found))
		})
	}
}

func TestWithRunPaths(t *testing.T) {
	runDir := filepath.Join("/out", "M51", "run-1")
	m := &scene3d.Manifest{
		Version: scene3d.ManifestVersion, Available: true,
		Points: "scene3d.bin", Backdrop: "scene3d_bg.png",
	}
	got := withRunPaths(m, runDir)

	// Artifacts are stored run-relative so a run directory can move or be restored from S3 without
	// rewriting the file, and served absolute because that is what GET /api/file takes.
	assert.Equal(t, filepath.Join(runDir, "scene3d.bin"), got.Points)
	assert.Equal(t, filepath.Join(runDir, "scene3d_bg.png"), got.Backdrop)
	require.Equal(t, "scene3d.bin", m.Points, "the cached manifest must not be mutated")
	require.Equal(t, "scene3d_bg.png", m.Backdrop)
}

func TestWithRunPaths_LeavesAbsentArtifactsAlone(t *testing.T) {
	// A run with no scene has no files to point at, and joining a run dir onto "" would invent one.
	got := withRunPaths(&scene3d.Manifest{Version: scene3d.ManifestVersion}, "/out/M51/run-1")
	assert.Empty(t, got.Points)
	assert.Empty(t, got.Backdrop)
}
