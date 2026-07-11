package inspect

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fits/fitstest"
)

// setSummary reduces an inventory's sets to a comparable {SetKey → frame count} map.
func setSummary(inv *Inventory) map[SetKey]int {
	m := map[SetKey]int{}
	for _, s := range inv.Sets {
		m[s.Key] = s.Count
	}
	return m
}

// TestAssembleInventory_MatchesScanMany proves the remote path (ranged header read → FrameFromHeader →
// AssembleInventory) yields the SAME grouping as a local ScanMany over the same header-classifiable
// captures — the guarantee that low-disk mode plans a run identically to a full local scan.
func TestAssembleInventory_MatchesScanMany(t *testing.T) {
	dir := t.TempDir()
	cards := func(imgtyp, filter string) map[string]string {
		c := map[string]string{"IMAGETYP": imgtyp, "GAIN": "200", "OFFSET": "50", "XBINNING": "1", "EXPTIME": "120.0"}
		if filter != "" {
			c["FILTER"] = filter
		}
		return c
	}
	// A small mono LRGB set + darks + flats, all header-classifiable (no pixel heuristics needed).
	fitstest.Write(t, dir, "light_L_1.fits", 32, 32, 1000, cards("LIGHT", "L"))
	fitstest.Write(t, dir, "light_L_2.fits", 32, 32, 1000, cards("LIGHT", "L"))
	fitstest.Write(t, dir, "light_R_1.fits", 32, 32, 1000, cards("LIGHT", "R"))
	fitstest.Write(t, dir, "dark_1.fits", 32, 32, 50, cards("DARK", ""))
	fitstest.Write(t, dir, "dark_2.fits", 32, 32, 50, cards("DARK", ""))
	fitstest.Write(t, dir, "flat_L_1.fits", 32, 32, 3000, cards("FLAT", "L"))

	opts := DefaultScanOptions()
	opts.DetectChannels = false // no pixel-based channel detection (the remote path can't run it)

	local, err := ScanMany(context.Background(), []string{dir}, opts)
	require.NoError(t, err)

	// Remote path: read each header (as a ranged reader would), classify with FrameFromHeader, assemble.
	names := []string{"light_L_1.fits", "light_L_2.fits", "light_R_1.fits", "dark_1.fits", "dark_2.fits", "flat_L_1.fits"}
	var frames []*Frame
	for _, n := range names {
		p := filepath.Join(dir, n)
		f, ferr := fits.Open(p)
		require.NoError(t, ferr)
		frames = append(frames, FrameFromHeader(p, f.Header))
	}
	remote := AssembleInventory(context.Background(), []string{dir}, map[string][]*Frame{dir: frames}, opts)

	assert.Equal(t, setSummary(local), setSummary(remote), "remote assembly must group identically to a local scan")
	assert.Len(t, remote.Frames, len(names))
	// Spot-check a couple of frames classified purely from the header.
	assert.Equal(t, Light, frames[0].Type)
	assert.Equal(t, "L", frames[0].Filter)
	assert.Equal(t, Dark, frames[3].Type)
}

func TestFrameFromHeader_ReadsCoreCards(t *testing.T) {
	dir := t.TempDir()
	p := fitstest.Write(t, dir, "x.fits", 64, 48, 500, map[string]string{
		"IMAGETYP": "LIGHT", "FILTER": "Ha", "GAIN": "180", "OFFSET": "30",
		"XBINNING": "2", "EXPTIME": "300.0", "OBJECT": "M42",
	})
	f, err := fits.Open(p)
	require.NoError(t, err)
	fr := FrameFromHeader(p, f.Header)

	assert.Equal(t, Light, fr.Type)
	assert.Equal(t, SourceHeader, fr.ClassSource)
	assert.Equal(t, "Ha", fr.Filter)
	assert.EqualValues(t, 180, fr.Gain)
	assert.EqualValues(t, 30, fr.Offset)
	assert.Equal(t, 2, fr.BinX)
	assert.EqualValues(t, 300_000, fr.ExposureMs)
	assert.Equal(t, 64, fr.Width)
	assert.Equal(t, 48, fr.Height)
	assert.Equal(t, "M42", fr.Object)
}
