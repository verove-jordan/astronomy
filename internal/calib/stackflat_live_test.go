package calib

// Live contract test for the flat-master build's uncalibrated retry (task #312: a whole 2020
// flat set died because its own calibration step failed, and the night's lights then went
// un-flat-fielded). Skipped when no host siril-cli is installed.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/stackalg"
)

func liveSirilRunner(t *testing.T) *siril.Runner {
	t.Helper()
	bin := os.Getenv("SIRIL_BIN")
	if bin == "" {
		bin = "/Applications/Siril.app/Contents/MacOS/siril-cli"
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("no siril-cli at %s", bin)
	}
	return siril.New(bin, siril.Limits{})
}

func writeFlatFrames(t *testing.T, dir string, n int) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	for i := 1; i <= n; i++ {
		im := fits.NewImage(64, 64, 1)
		for p := range im.Pix[0] {
			im.Pix[0][p] = 0.5 + 0.001*float32((p+i)%13)
		}
		require.NoError(t, im.WriteFITS(filepath.Join(dir, fmt.Sprintf("f_%03d.fits", i))))
	}
}

// flatPaths returns the paths writeFlatFrames wrote (the retry re-links from them).
func flatPaths(dir string, n int) []string {
	out := make([]string, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, filepath.Join(dir, fmt.Sprintf("f_%03d.fits", i)))
	}
	return out
}

func TestStackMasterSet_FlatRetriesUncalibrated(t *testing.T) {
	runner := liveSirilRunner(t)
	dir := t.TempDir()
	key := inspect.SetKey{Type: inspect.Flat, Filter: "L", ExposureMs: 250, Gain: 200, Bin: 1}

	t.Run("an unusable bias falls back to an uncalibrated flat stack with a note", func(t *testing.T) {
		seqDir := filepath.Join(dir, "seq_bad")
		writeFlatFrames(t, seqDir, 4)
		badBias := filepath.Join(dir, "master_bias_bad.fits")
		require.NoError(t, os.WriteFile(badBias, []byte("not a fits file"), 0o644))
		built := []Master{{Type: MasterBias, Gain: 200, Offset: 0, Bin: 1, Path: badBias}}

		outBase := filepath.Join(dir, "master_flat_bad")
		note, err := stackMasterSet(context.Background(), runner, key, built, seqDir, outBase, flatPaths(seqDir, 4), stackalg.DefaultMasters(), nil)
		require.NoError(t, err, "the flat must survive its broken bias")
		assert.Contains(t, note, "stacked WITHOUT its bias")
		assert.FileExists(t, outBase+".fits")
	})

	t.Run("a healthy bias calibrates normally with no note", func(t *testing.T) {
		seqDir := filepath.Join(dir, "seq_ok")
		writeFlatFrames(t, seqDir, 4)
		bias := fits.NewImage(64, 64, 1)
		for p := range bias.Pix[0] {
			bias.Pix[0][p] = 0.01
		}
		biasPath := filepath.Join(dir, "master_bias_ok.fits")
		require.NoError(t, bias.WriteFITS(biasPath))
		built := []Master{{Type: MasterBias, Gain: 200, Offset: 0, Bin: 1, Path: biasPath}}

		outBase := filepath.Join(dir, "master_flat_ok")
		note, err := stackMasterSet(context.Background(), runner, key, built, seqDir, outBase, flatPaths(seqDir, 4), stackalg.DefaultMasters(), nil)
		require.NoError(t, err)
		assert.Empty(t, note)
		assert.FileExists(t, outBase+".fits")
	})
}
