package optics

import (
	"encoding/json"
	"image/png"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteArtifacts(t *testing.T) {
	dir := t.TempDir()
	p := vignetteBase(512, 512, 0.18)
	addRing(p, 512, 200, 150, 8, 18, 0.077)
	master := writeFloatMaster(t, dir, "flat.fits", 512, 512, p)

	qc, defects, err := AnalyzeFlat(master, nil)
	require.NoError(t, err)
	require.Len(t, defects, 1)
	require.NoError(t, WriteArtifacts(master, qc, defects))

	base := strings.TrimSuffix(master, ".fits")

	// JSON sidecar: valid, versioned, Shape omitted, coordinate tags present.
	raw, err := os.ReadFile(base + ".defects.json")
	require.NoError(t, err)
	var doc struct {
		V       int      `json:"v"`
		QC      FlatQC   `json:"qc"`
		Defects []Defect `json:"defects"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))
	assert.Equal(t, 1, doc.V)
	assert.Equal(t, "warn", doc.QC.Status, "a 5%-deep donut trips the deep-defect warning")
	require.Len(t, doc.Defects, 1)
	assert.NotContains(t, string(raw), "Shape", "repair kernel must not be serialized")
	assert.Contains(t, string(raw), `"cx"`)

	// PNG preview decodes and is bounded by the downsampled detection size.
	f, err := os.Open(base + "_defects.png")
	require.NoError(t, err)
	defer f.Close()
	img, err := png.Decode(f)
	require.NoError(t, err)
	assert.LessOrEqual(t, img.Bounds().Dx(), 1024)
	assert.LessOrEqual(t, img.Bounds().Dy(), 1024)
}
