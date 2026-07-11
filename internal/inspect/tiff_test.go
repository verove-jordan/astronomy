package inspect

import (
	"context"
	"image"
	"image/color"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/image/tiff"
)

// A real SharpCap "<name>.TIF.txt" sidecar carries no frame-type field — dark and flat sidecars are
// identical (same 10 ms exposure, same slot). Only the ancestor folder names the type.
const sharpcapSidecar = "[ZWO ASI1600MM Pro]\nEFW Slot = 1(Alias: L)\nExposure = 10ms\nGain = 0\nTemperature = -17.0 C\n"

// writeTIFFFrame writes a placeholder .tif plus its "<name>.tif.txt" sidecar. Content is irrelevant for
// the folder/sidecar classification path — no pixels are read when a folder names the type.
func writeTIFFFrame(t *testing.T, dir, name, sidecar string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte("tif"), 0o600))
	if sidecar != "" {
		require.NoError(t, os.WriteFile(p+".txt", []byte(sidecar), 0o600))
	}
	return p
}

// writeGray16TIFF encodes a real 16-bit mono TIFF (RAW16-like) so sips can develop it for the curve pass.
func writeGray16TIFF(t *testing.T, path string, w, h int, fill func(x, y int) uint16) {
	t.Helper()
	img := image.NewGray16(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetGray16(x, y, color.Gray16{Y: fill(x, y)})
		}
	}
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, tiff.Encode(f, img, nil))
}

// TestScan_TIFFByDirName is the regression for the reported bug. These SharpCap 16-bit .TIF lunar
// captures encode the frame type ONLY in an ancestor folder (darks/flats/offsets/CapObj), two levels
// above the file, with a filename that looks like a light (…-L-Moon…) and a sidecar that carries no
// type. Previously .tif was in no extension map, so the walk silently dropped them and only the .FIT
// lights showed ("50 LIGHT"). They must now be recognized and typed by directory name, with the
// ".TIF.txt" sidecar back-filled.
func TestScan_TIFFByDirName(t *testing.T) {
	root := t.TempDir()
	mk := func(kind, session, name string) string {
		return writeTIFFFrame(t, filepath.Join(root, kind, "Moon", session), name, sharpcapSidecar)
	}
	mk("CapObj", "2026-07-09_03_00_00Z", "2026-07-09-0300_0-L-Moon_0000.tif")
	mk("CapObj", "2026-07-09_03_00_00Z", "2026-07-09-0300_0-L-Moon_0001.tif")
	darkP := mk("darks", "2026-07-09_02_47_32Z", "2026-07-09-0247_5-L-Moon_0000.tif")
	mk("darks", "2026-07-09_02_47_32Z", "2026-07-09-0247_5-L-Moon_0001.tif")
	mk("flats", "2026-07-09_02_33_52Z", "2026-07-09-0233_8-L-Moon_0000.tif")
	mk("offsets", "2026-07-09_02_20_00Z", "2026-07-09-0220_0-L-Moon_0000.tif")

	// A PROCESSED output stored beside the captures (old stack results are common inside capture
	// trees) must be ignored — ingested, it forms a phantom one-frame channel set that sinks that
	// channel's run at Siril `link`.
	writeTIFFFrame(t, filepath.Join(root, "CapObj", "Moon", "2026-07-09_03_00_00Z"), "moon_R_stacked.tif", "")

	inv, err := Scan(context.Background(), root)
	require.NoError(t, err)
	for _, fr := range inv.Frames {
		assert.NotContains(t, fr.Path, "_stacked", "processed outputs must never become frames")
	}

	counts := inv.CountsByType()
	assert.Equal(t, 2, counts[Light], "CapObj → lights")
	assert.Equal(t, 2, counts[Dark], "darks/ folder")
	assert.Equal(t, 1, counts[Flat], "flats/ folder")
	assert.Equal(t, 1, counts[Bias], "offsets/ folder → bias")

	// The sidecar back-fills metadata even without a FITS header (exposure 10 ms, wheel slot 1) — and the
	// 10 ms exposure does NOT demote the dark to a bias, because the folder name wins over the curve.
	var dark *Frame
	for _, fr := range inv.Frames {
		if fr.Path == darkP {
			dark = fr
		}
	}
	require.NotNil(t, dark)
	assert.Equal(t, Dark, dark.Type)
	assert.Equal(t, int64(10), dark.ExposureMs, "Exposure = 10ms from the .tif.txt sidecar")
	assert.Equal(t, 1, dark.WheelSlot, "EFW Slot = 1 from the sidecar")
}

// TestScan_TIFFByCurve exercises requirement (3): when no folder or filename names the type, deduce it
// from the pixel curve. Two unlabeled 16-bit TIFFs — a structured "light" (bright ~11% block on a dim
// field) vs a flat, dim calibration frame — are developed via sips and separated by classifyByStats.
// Skipped where no sips raw developer is present (e.g. Linux CI), mirroring the raw-still path.
func TestScan_TIFFByCurve(t *testing.T) {
	if _, err := exec.LookPath("sips"); err != nil {
		t.Skip("no sips raw developer on PATH; the pixel-curve fallback is host-only")
	}
	root := t.TempDir()
	const w, h = 256, 256
	// Neutral filenames: no date-time prefix (so no wheel slot forces a Light) and no type token, so both
	// frames stay Unknown and reach the curve pass.
	lightP := filepath.Join(root, "frameA_0000.tif")
	writeGray16TIFF(t, lightP, w, h, func(x, y int) uint16 {
		if x < w/3 && y < h/3 { // a bright ~11% region → high bright-fraction → light
			return 64000
		}
		return 1000
	})
	dimP := filepath.Join(root, "frameB_0000.tif")
	writeGray16TIFF(t, dimP, w, h, func(x, y int) uint16 { return 3000 }) // uniform, dim, structureless

	// Minimal sidecars: a short exposure, no EFW slot, no type — so classification rests purely on curves.
	for _, p := range []string{lightP, dimP} {
		require.NoError(t, os.WriteFile(p+".txt", []byte("Exposure = 10ms\n"), 0o600))
	}

	inv, err := Scan(context.Background(), root)
	require.NoError(t, err)

	got := map[string]FrameType{}
	for _, fr := range inv.Frames {
		got[fr.Path] = fr.Type
	}
	assert.Equal(t, Light, got[lightP], "structured frame → light by curve")
	assert.True(t, isCalibration(got[dimP]), "flat dim frame → calibration by curve, got %s", got[dimP])
}
