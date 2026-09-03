package calib

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/inspect"
)

// writeFrame puts a w×h FITS at dir/name and returns its path.
func writeFrame(t *testing.T, dir, name string, w, h int) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, fits.Write16(p, w, h, make([]uint16, w*h), nil))
	return p
}

func TestKeepMatchingDims(t *testing.T) {
	dir := t.TempDir()
	const lw, lh = 64, 48
	light := writeFrame(t, dir, "light.fits", lw, lh)
	fitting := writeFrame(t, dir, "master_fit.fits", lw, lh)
	foreign := writeFrame(t, dir, "master_foreign.fits", 32, 24)
	other := writeFrame(t, dir, "master_other.fits", 100, 100)
	missing := filepath.Join(dir, "nowhere.fits")

	tests := []struct {
		name      string
		masters   []Master
		lightPath string
		wantPaths []string
		wantNote  string
	}{
		{
			name:      "same sensor keeps everything and says nothing",
			masters:   []Master{{Path: fitting}, {Path: fitting}},
			lightPath: light,
			wantPaths: []string{fitting, fitting},
		},
		{
			name:      "another sensor's master is excluded and named",
			masters:   []Master{{Path: fitting}, {Path: foreign}},
			lightPath: light,
			wantPaths: []string{fitting},
			wantNote:  "1 calibration master(s) from another sensor (32×24) were not considered — these lights are 64×48",
		},
		{
			name:      "the note groups sizes, commonest first",
			masters:   []Master{{Path: foreign}, {Path: other}, {Path: foreign}},
			lightPath: light,
			wantPaths: []string{},
			wantNote:  "3 calibration master(s) from another sensor (32×24 (×2), 100×100) were not considered — these lights are 64×48",
		},
		{
			// A master still to be built from this capture has no path yet; one not pulled from the S3
			// mirror is fetched after selection. Neither may be discarded on a failed open.
			name:      "a master with no readable file is kept",
			masters:   []Master{{Path: ""}, {Path: missing}},
			lightPath: light,
			wantPaths: []string{"", missing},
		},
		{
			// A camera raw is not a FITS: with no reference size the check cannot run.
			name:      "an unreadable light leaves the pool untouched",
			masters:   []Master{{Path: foreign}, {Path: other}},
			lightPath: filepath.Join(dir, "raw.dng"),
			wantPaths: []string{foreign, other},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kept, note := KeepMatchingDims(tt.masters, tt.lightPath)

			got := make([]string, 0, len(kept))
			for _, m := range kept {
				got = append(got, m.Path)
			}
			assert.Equal(t, tt.wantPaths, got, "kept masters")
			assert.Equal(t, tt.wantNote, note, "note")
		})
	}
}

// TestKeepMatchingDims_LeavesTheFallbackIntact is the regression from the first ASI2600MC capture.
// bestFlat's filter-matched pass found exactly one flat recorded as "RGB" — a master from a DIFFERENT
// camera — while the capture's own flats were invisible to it, because nameColorChannel labels only
// LIGHTS "RGB" and leaves calibration frames' filter empty. Removing the foreign flat from the
// finished Selection left the set with no flat at all; removing it from the POOL first lets the
// second, filter-blind pass reach the capture's own.
func TestKeepMatchingDims_LeavesTheFallbackIntact(t *testing.T) {
	dir := t.TempDir()
	light := writeFrame(t, dir, "light.fits", 6248, 4176)
	ownFlat := writeFrame(t, dir, "own_flat.fits", 6248, 4176)
	foreignFlat := writeFrame(t, dir, "foreign_flat.fits", 6064, 4040)

	pool := []Master{
		{Type: MasterFlat, Filter: "RGB", Path: foreignFlat, FrameCount: 30, FromLibrary: true},
		{Type: MasterFlat, Filter: "", Path: ownFlat, FrameCount: 50},
	}
	key := inspect.SetKey{Type: inspect.Light, Filter: "RGB", Color: true}

	// Without the pool filter, the filter-matched pass wins with the other camera's flat.
	assert.Equal(t, foreignFlat, MatchForLight(key, pool).Flat.Path,
		"precondition: the foreign RGB flat is what the matcher prefers")

	usable, note := KeepMatchingDims(pool, light)
	sel := MatchForLight(key, usable)

	require.NotNil(t, sel.Flat, "the capture's own flat must survive as the fallback")
	assert.Equal(t, ownFlat, sel.Flat.Path)
	assert.Contains(t, note, "6064×4040")
}

func TestSameDims(t *testing.T) {
	dir := t.TempDir()
	a := writeFrame(t, dir, "a.fits", 64, 48)
	b := writeFrame(t, dir, "b.fits", 64, 48)
	c := writeFrame(t, dir, "c.fits", 48, 64) // transposed: same pixel count, different sensor
	absent := filepath.Join(dir, "absent.fits")

	tests := []struct {
		name       string
		a, b       string
		same, know bool
	}{
		{"equal dimensions", a, b, true, true},
		{"transposed dimensions differ", a, c, false, true},
		{"unreadable left", absent, b, false, false},
		{"unreadable right", a, absent, false, false},
		{"empty path", "", b, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			same, known := SameDims(tt.a, tt.b)
			assert.Equal(t, tt.know, known, "known")
			assert.Equal(t, tt.same, same, "same")
		})
	}
}
