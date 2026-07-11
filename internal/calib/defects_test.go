package calib

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// defectLCG is a deterministic noise source so the scan's robust statistics are reproducible.
type defectLCG struct{ s uint64 }

func (l *defectLCG) next() float64 {
	l.s = l.s*6364136223846793005 + 1442695040888963407
	return float64(l.s>>11) / float64(1<<53)
}

// writeDarkPool writes n WxH mono float darks (fits.WriteFITS stamps ROWORDER=TOP-DOWN): a noisy
// floor plus per-frame values from pixel(frame, x, y) where non-negative.
func writeDarkPool(t *testing.T, dir string, n, w, h int, pixel func(frame, x, y int) float64) []string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	paths := make([]string, 0, n)
	for f := 0; f < n; f++ {
		rng := &defectLCG{s: uint64(100 + f)}
		im := fits.NewImage(w, h, 1)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				v := 0.2 + (rng.next()-0.5)*0.02 // dark floor with per-frame temporal noise
				if pv := pixel(f, x, y); pv >= 0 {
					v = pv
				}
				im.Pix[0][y*w+x] = float32(v)
			}
		}
		p := filepath.Join(dir, filepath.Base(dir)+string(rune('a'+f))+".fits")
		require.NoError(t, im.WriteFITS(p))
		paths = append(paths, p)
	}
	return paths
}

func TestScanDarkDefects_FindsHotColdAndRTS(t *testing.T) {
	const w, h = 32, 32
	// hot (5,7): elevated in EVERY frame — what -cc=dark would also see.
	// cold (11,3): dead pixel, always ~0.
	// RTS (20,9): flickers ±0.25 around the normal floor — mean looks NORMAL, only the temporal
	// sigma exposes it. This is the pixel class the whole feature exists for.
	paths := writeDarkPool(t, t.TempDir(), 12, w, h, func(frame, x, y int) float64 {
		switch {
		case x == 5 && y == 7:
			return 0.8
		case x == 11 && y == 3:
			return 0.0
		case x == 20 && y == 9:
			// Alternate 0.4 / 0.0 → temporal mean = 0.2 = the normal floor (mean test blind to it),
			// temporal sigma = 0.2 ≫ the floor's noise (sigma test catches it).
			if frame%2 == 0 {
				return 0.4
			}
			return 0.0
		default:
			return -1 // keep the noisy floor
		}
	})

	d, err := ScanDarkDefects(paths)
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.Equal(t, w, d.W)
	assert.True(t, d.TopDown, "fits.WriteFITS stamps ROWORDER=TOP-DOWN")

	kinds := map[[2]int]byte{}
	for _, p := range d.Pixels {
		kinds[[2]int{p.X, p.Y}] = p.Kind
	}
	assert.Equal(t, byte('H'), kinds[[2]int{5, 7}], "always-hot pixel flagged H")
	assert.Equal(t, byte('C'), kinds[[2]int{11, 3}], "dead pixel flagged C")
	assert.Equal(t, byte('H'), kinds[[2]int{20, 9}], "flickering RTS pixel flagged (as H) despite a normal mean")
	assert.GreaterOrEqual(t, d.Unstable, 1)
	assert.GreaterOrEqual(t, d.Hot, 1)
	assert.GreaterOrEqual(t, d.Cold, 1)
}

func TestWriteSirilBPM_FlipsYForTopDown(t *testing.T) {
	// Siril's pixel space is bottom-up; find_hot on a ROWORDER=TOP-DOWN master reports the planted
	// (10,20) as "P 10 43 H" for H=64 (verified live) — our writer must match that convention.
	d := &Defects{W: 64, H: 64, TopDown: true, Pixels: []DefectPixel{
		{X: 10, Y: 20, Kind: 'H'},
		{X: 40, Y: 50, Kind: 'C'},
	}}
	path := filepath.Join(t.TempDir(), "defects.lst")
	require.NoError(t, d.WriteSirilBPM(path))
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "P 10 43 H\nP 40 13 C\n", string(b))

	// Bottom-up sources (raw camera FITS without ROWORDER) keep their row indices as-is.
	d.TopDown = false
	require.NoError(t, d.WriteSirilBPM(path))
	b, _ = os.ReadFile(path)
	assert.Equal(t, "P 10 20 H\nP 40 50 C\n", string(b))
}

func TestScanDarkDefects_TooFewFramesSkips(t *testing.T) {
	paths := writeDarkPool(t, t.TempDir(), 5, 16, 16, func(int, int, int) float64 { return -1 })
	d, err := ScanDarkDefects(paths)
	require.NoError(t, err)
	assert.Nil(t, d, "temporal sigma over <8 frames is not trustworthy")
}

func TestScanDarkDefects_CapKeepsStrongest(t *testing.T) {
	const w, h = 32, 32 // 1024 px → cap = 5 pixels (defectMaxFrac 0.5%)
	paths := writeDarkPool(t, t.TempDir(), 10, w, h, func(frame, x, y int) float64 {
		if y == 15 && x >= 2 && x < 22 { // 20 hot pixels — far more than the cap
			return 0.6 + float64(x)*0.02 // increasing severity along the row
		}
		return -1
	})
	d, err := ScanDarkDefects(paths)
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.True(t, d.Truncated)
	assert.LessOrEqual(t, len(d.Pixels), 5)
	for _, p := range d.Pixels {
		assert.GreaterOrEqual(t, p.X, 17, "only the strongest (rightmost) hot pixels survive the cap")
	}
}

func TestBuildDefectList_WritesSidecarAndNote(t *testing.T) {
	dir := t.TempDir()
	paths := writeDarkPool(t, filepath.Join(dir, "pool"), 12, 24, 24, func(frame, x, y int) float64 {
		if x == 4 && y == 5 {
			return 0.9
		}
		return -1
	})
	master := filepath.Join(dir, "master_DARK_25000ms_g200o30_b1_-15C.fits")
	note := buildDefectList(master, paths)
	assert.Contains(t, note, "dark defect map:")
	lst := DefectsListFor(master)
	require.NotEmpty(t, lst)
	assert.Equal(t, strings.TrimSuffix(master, ".fits")+"_defects.lst", lst)

	// Too few frames → the (now stale) sidecar is removed and no note is emitted.
	note = buildDefectList(master, paths[:5])
	assert.Empty(t, note)
	assert.Empty(t, DefectsListFor(master), "stale defect list must not outlive its pool")
}

func TestDefectsListPath_Empty(t *testing.T) {
	assert.Empty(t, DefectsListPath(""))
	assert.Empty(t, DefectsListFor(""))
}
