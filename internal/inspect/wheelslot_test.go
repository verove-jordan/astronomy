package inspect

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/verove-jordan/astronomy/internal/filters"
	"github.com/verove-jordan/astronomy/internal/fits/fitstest"
)

func TestLegendFromManifest(t *testing.T) {
	m33 := parseManifest("LLL R G B Ha\nLLL R G B Ha\nLLL\n")
	assert.Equal(t, []string{"L", "R", "G", "B", "Ha"}, legendFromManifest(m33))

	glued := parseManifest("LLLRGBHaHa\n")
	assert.Equal(t, []string{"L", "R", "G", "B", "Ha"}, legendFromManifest(glued))

	// A numeric / empty info.txt names no filters → the default wheel order.
	assert.Equal(t, defaultSlotLegend(), legendFromManifest(manifest{}))
}

func TestFilterForSlot(t *testing.T) {
	legend := []string{"L", "R", "G", "B", "Ha"}
	assert.Equal(t, "L", filterForSlot(legend, 1))
	assert.Equal(t, "Ha", filterForSlot(legend, 5))
	assert.Equal(t, "S6", filterForSlot(legend, 6), "slot beyond the legend → stable placeholder")
}

// The legend covers every canonical filter, NOT just the ones signal detection can discriminate:
// a 7-slot wheel's narrowband positions must resolve to OIII/SII rather than "S6"/"S7" placeholders.
func TestDefaultSlotLegend(t *testing.T) {
	assert.Equal(t, filters.Canonical, defaultSlotLegend())
	assert.Equal(t, "OIII", filterForSlot(defaultSlotLegend(), 6))
	assert.Equal(t, "SII", filterForSlot(defaultSlotLegend(), 7))
}

// writeSlotFrame writes a uniform FITS plus the SharpCap "<name>.txt" sidecar carrying its EFW slot.
func writeSlotFrame(t *testing.T, dir, name string, slot int) {
	t.Helper()
	fitstest.Write(t, dir, name, 16, 16, uint16(1000+slot*100), map[string]string{
		"GAIN": "300", "EXPOINUS": "30000000", "CCD-TEMP": "-15.0",
	})
	body := fmt.Sprintf("EFW Slot = %d(Alias: %d)\nExposure = 30s\nGain = 300\nTemperature = -15.0 C\n", slot, slot)
	require.NoError(t, os.WriteFile(filepath.Join(dir, name+".txt"), []byte(body), 0o644))
}

func filterCounts(inv *Inventory) map[string]int {
	m := make(map[string]int)
	for _, fr := range inv.Frames {
		if fr.Type == Light {
			m[fr.Filter]++
		}
	}
	return m
}

// End-to-end: an info.txt that over-counts the folders (the M33 bug) plus per-frame EFW sidecars, and a
// sibling capture folder with NO info.txt. The wheel slot is ground truth, so the off-by-one is harmless
// and every light is named from its slot — never collapsed into Ha by signal detection.
func TestScan_ClassifiesByWheelSlotDespiteOffByOneManifest(t *testing.T) {
	root := t.TempDir()
	capObj := filepath.Join(root, "M33", "CapObj")
	require.NoError(t, os.MkdirAll(capObj, 0o755))
	// 7 filter tokens but only 6 capture folders below.
	require.NoError(t, os.WriteFile(filepath.Join(capObj, "info.txt"),
		[]byte("L R G B Ha\nL L\n300 gain\n-15°\n"), 0o644))
	for i, slot := range []int{1, 2, 3, 4, 5, 1} {
		sub := filepath.Join(capObj, fmt.Sprintf("2021-08-14_0%d_00_00Z", i))
		require.NoError(t, os.MkdirAll(sub, 0o755))
		writeSlotFrame(t, sub, fmt.Sprintf("2021-08-14-0%d00_6-%d-CapObj_0000.FIT", i, slot), slot)
	}
	// Sibling folder, no info.txt — slot 1 must still resolve to L via the default legend.
	capObj0 := filepath.Join(root, "M33", "CapObj_0", "2021-08-13_23_47_49Z")
	require.NoError(t, os.MkdirAll(capObj0, 0o755))
	writeSlotFrame(t, capObj0, "2021-08-13-2347_8-1-CapObj_0000.FIT", 1)

	inv, err := ScanMany(context.Background(),
		[]string{filepath.Join(root, "M33", "CapObj"), filepath.Join(root, "M33", "CapObj_0")},
		DefaultScanOptions())
	require.NoError(t, err)

	assert.Equal(t, map[string]int{"L": 3, "R": 1, "G": 1, "B": 1, "Ha": 1}, filterCounts(inv),
		"L-dominant (slots 1×2 in CapObj + slot 1 in CapObj_0), not Ha-dominant")
	for _, fr := range inv.Frames {
		if fr.Type == Light {
			assert.Equal(t, SourceWheel, fr.ClassSource, fr.Path)
		}
	}
}

// ngc6992 (Veil): the info.txt capture order (O3, Ha, L, R, G) is NOT the physical wheel-slot order
// (L=1, R=2, G=3, O3=4, Ha=5). The legend must be LEARNED from each folder's physical slot, not assumed
// from capture position — otherwise "O3" is dropped, every label shifts, and slot 5 shows the bare "S5"
// placeholder. Also exercises the "O3"→OIII manifest alias.
func TestScan_NamesNarrowbandFromManifestByPhysicalSlot(t *testing.T) {
	root := t.TempDir()
	autorun := filepath.Join(root, "ngc6992", "autorun")
	require.NoError(t, os.MkdirAll(autorun, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(autorun, "info.txt"),
		[]byte("O3 250 gain\nHa 250 gain\nL 100 gain\nR 100\nG 100\n10 x 60s\n"), 0o644))

	// Folder timestamps are in capture order (O3, Ha, L, R, G); each sits in a different physical slot.
	steps := []struct {
		ts   string
		slot int
	}{
		{"2020-07-12_21_00_00Z", 4}, // O3 → slot 4
		{"2020-07-12_21_10_00Z", 5}, // Ha → slot 5
		{"2020-07-12_21_20_00Z", 1}, // L  → slot 1
		{"2020-07-12_21_30_00Z", 2}, // R  → slot 2
		{"2020-07-12_21_40_00Z", 3}, // G  → slot 3
	}
	for i, s := range steps {
		sub := filepath.Join(autorun, s.ts)
		require.NoError(t, os.MkdirAll(sub, 0o755))
		writeSlotFrame(t, sub, fmt.Sprintf("2020-07-12-21%d0_6-%d-autorun_0000.FIT", i, s.slot), s.slot)
	}

	inv, err := Scan(context.Background(), filepath.Join(root, "ngc6992"))
	require.NoError(t, err)

	assert.Equal(t, map[string]int{"L": 1, "R": 1, "G": 1, "Ha": 1, "OIII": 1}, filterCounts(inv),
		"each physical slot resolves to its real filter (slot 4→OIII, slot 5→Ha); no unnamed S5")
	for _, fr := range inv.Frames {
		if fr.Type == Light {
			assert.Equal(t, SourceWheel, fr.ClassSource, fr.Path)
		}
	}
}

// A SharpCap/ASICAP session with NO sidecar text and a non-descriptive "data" folder: the filter comes
// from the filename slot, named by the default wheel order. Reproduces M27's data/ (LRGB slots 1-4).
func TestScan_NamesByFilenameSlotWithoutSidecar(t *testing.T) {
	root := t.TempDir()
	data := filepath.Join(root, "m27", "data")
	for i, slot := range []int{1, 2, 3, 4} {
		sub := filepath.Join(data, fmt.Sprintf("2019-08-29_2%d_00_00Z", i))
		require.NoError(t, os.MkdirAll(sub, 0o755))
		fitstest.Write(t, sub, fmt.Sprintf("2019-08-29-22%d0_5-%d-L_0000.FIT", i, slot),
			16, 16, uint16(1000+slot*50), map[string]string{"GAIN": "250", "EXPOINUS": "60000000"})
	}

	inv, err := Scan(context.Background(), data)
	require.NoError(t, err)

	assert.Equal(t, map[string]int{"L": 1, "R": 1, "G": 1, "B": 1}, filterCounts(inv),
		"data/ slots 1-4 → L,R,G,B by the default legend")
	for _, fr := range inv.Frames {
		assert.Equal(t, SourceWheel, fr.ClassSource)
	}
}
