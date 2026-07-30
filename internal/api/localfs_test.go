package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/localfs"
)

// relTo returns target expressed relative to the current working directory (skipping if that is not
// possible), so a test can feed a RELATIVE config dir and prove appDirs absolutizes it.
func relTo(t *testing.T, target string) string {
	t.Helper()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	rel, err := filepath.Rel(cwd, target)
	if err != nil {
		t.Skipf("cannot compute a relative path to %s: %v", target, err)
	}
	return rel
}

// A relative DataDir (the config default is ./data) MUST come back absolute — localfs.Allowed resolves a
// root's symlinks without absolutizing, so a relative root would silently never match. A missing dir is
// omitted rather than offered.
func TestServer_appDirs(t *testing.T) {
	data := t.TempDir()
	output := t.TempDir()
	s := &Server{cfg: &config.Config{
		DataDir:   relTo(t, data), // relative on purpose
		OutputDir: output,
		WorkDir:   filepath.Join(t.TempDir(), "does-not-exist"),
	}}

	byKey := map[string]string{}
	for _, d := range s.appDirs() {
		byKey[d.Key] = d.Path
		assert.True(t, filepath.IsAbs(d.Path), "app dir %q must be absolute, got %q", d.Key, d.Path)
	}
	assert.Contains(t, byKey, "input")
	assert.Contains(t, byKey, "output")
	assert.NotContains(t, byKey, "work", "a missing work dir is omitted, never offered")
}

// The app dirs widen the browse/upload allow-list but must NOT become fake "drives": localDrives stays on
// browseRoots (removable media only), while browse/upload use localAllowRoots (drives ∪ app dirs).
func TestServer_localAllowRootsSplit(t *testing.T) {
	data := t.TempDir()
	s := &Server{cfg: &config.Config{DataDir: data}}

	assert.Contains(t, s.localAllowRoots(), data, "the app dir is in the browse/upload allow-list")
	assert.NotContains(t, s.browseRoots(), data, "the app dir is NOT a drive-listing root")

	_, ok := localfs.Allowed(s.localAllowRoots(), data)
	assert.True(t, ok, "the app dir is browsable through the allow-list")
}

func TestLocalSources(t *testing.T) {
	data := t.TempDir()
	output := t.TempDir()
	s := &Server{cfg: &config.Config{
		DataDir:   data,
		OutputDir: output,
		WorkDir:   filepath.Join(t.TempDir(), "nope"),
	}}

	rec := httptest.NewRecorder()
	s.localSources(rec, httptest.NewRequest(http.MethodGet, "/api/local/sources", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Sources []appDir `json:"sources"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	keys := map[string]bool{}
	for _, src := range body.Sources {
		keys[src.Key] = true
		assert.True(t, filepath.IsAbs(src.Path), "source %q path must be absolute", src.Key)
	}
	assert.True(t, keys["input"])
	assert.True(t, keys["output"])
	assert.False(t, keys["work"], "a missing work dir is omitted from sources")
}
