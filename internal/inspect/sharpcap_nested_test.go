package inspect

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/fits/fitstest"
)

// writeCapObjFrame writes one SharpCap-style FITS + numeric-alias sidecar at
// <root>/<kind>/CapObj/<session>/<name>. The FITS carries EXPOINUS/GAIN but NO IMAGETYP/FILTER (as real
// ASICAP captures do), so type resolves from the "<kind>" grandparent and filter from the wheel slot.
func writeCapObjFrame(t *testing.T, root, kind, session, name string, slot int, expoinUS int64, gain int) {
	t.Helper()
	dir := filepath.Join(root, kind, "CapObj", session)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	fitstest.Write(t, dir, name, 16, 16, uint16(1000+slot*100), map[string]string{
		"GAIN": fmt.Sprintf("%d", gain), "EXPOINUS": fmt.Sprintf("%d", expoinUS), "CCD-TEMP": "-25.0",
	})
	// Alias is the bare slot NUMBER (the user never named the wheel in SharpCap) — no filter name to read.
	body := fmt.Sprintf("EFW Slot = %d(Alias: %d)\nGain = %d\nBrightness = 10\nTemperature = -25.0 C\n", slot, slot, gain)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".txt"), []byte(body), 0o644))
}

// TestScan_SharpCapCapObjNestedUnderCalibFolders is the regression for the reported bug: SharpCap nests
// its "CapObj" capture folder UNDER the type folder (darks/CapObj/…, flats/CapObj/…, offset/CapObj/…),
// and the frames carry no IMAGETYP/FILTER. Previously the nearer "CapObj" (a Light signal) shadowed the
// real calibration grandparent, so EVERY frame — darks, offset, flats — was mis-typed LIGHT and no
// calibration was found. Now the explicit calibration folder wins, and per-filter EFW flats (numeric slot
// alias) are named from their slot instead of collapsing into one merged cross-filter master flat.
func TestScan_SharpCapCapObjNestedUnderCalibFolders(t *testing.T) {
	root := t.TempDir()

	// Lights: L/R/G at slots 1/2/3, 30 s, gain 250.
	for _, s := range []int{1, 2, 3} {
		for i := 0; i < 2; i++ {
			writeCapObjFrame(t, root, "triplet_m66", "2023-02-27_22_55_39Z",
				fmt.Sprintf("2023-02-27-2255_6-%d-CapObj_%04d.FIT", s, i), s, 30_000_000, 250)
		}
	}
	// Flats: L/R/G at slots 1/2/3, ~5 ms, gain 0 — per-filter, must NOT merge.
	for _, s := range []int{1, 2, 3} {
		for i := 0; i < 2; i++ {
			writeCapObjFrame(t, root, "flats", "2023-02-28_06_00_34Z",
				fmt.Sprintf("2023-02-28-0600_5-%d-CapObj_%04d.FIT", s, i), s, 4794, 0)
		}
	}
	// Darks: 45 s, gain 400 — filter-agnostic regardless of the slot they happened to sit on.
	for i := 0; i < 2; i++ {
		writeCapObjFrame(t, root, "darks", "2023-02-28_05_09_49Z",
			fmt.Sprintf("2023-02-28-0509_8-2-CapObj_%04d.FIT", i), 2, 45_000_000, 400)
	}
	// Offset (bias): ~0 s, gain 400.
	for i := 0; i < 2; i++ {
		writeCapObjFrame(t, root, "offset", "2023-02-28_05_52_24Z",
			fmt.Sprintf("2023-02-28-0552_4-1-CapObj_%04d.FIT", i), 1, 32, 400)
	}

	inv, err := Scan(context.Background(), root)
	require.NoError(t, err)

	counts := inv.CountsByType()
	assert.Equal(t, 6, counts[Light], "3 filters × 2 lights")
	assert.Equal(t, 2, counts[Dark], "darks/ grandparent wins over the nearer CapObj")
	assert.Equal(t, 6, counts[Flat], "flats/ grandparent wins over the nearer CapObj")
	assert.Equal(t, 2, counts[Bias], "offset/ grandparent → bias")

	// Flats split per-filter (numeric slot alias named via the wheel legend), never one merged set.
	flatByFilter := map[string]int{}
	for _, s := range inv.SetsOfType(Flat) {
		flatByFilter[s.Key.Filter] += s.Count
	}
	assert.Equal(t, map[string]int{"L": 2, "R": 2, "G": 2}, flatByFilter, "per-filter master flats, not a merged Filter=\"\" set")

	// Darks and bias stay filter-agnostic — one set each, no filter identity.
	require.Len(t, inv.SetsOfType(Dark), 1)
	assert.Empty(t, inv.SetsOfType(Dark)[0].Key.Filter, "dark SetKey ignores filter")
	require.Len(t, inv.SetsOfType(Bias), 1)
	assert.Empty(t, inv.SetsOfType(Bias)[0].Key.Filter, "bias SetKey ignores filter")
}
