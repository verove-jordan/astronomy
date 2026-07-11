package skycat

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeCatalog(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func TestResolve(t *testing.T) {
	dir := t.TempDir()
	writeCatalog(t, dir, "messier.csv",
		"name,ra,dec,diameter,mag,alias\nM101,210.80208,54.348917,26.9,7.7,Pinwheel galaxy/NGC5457\n")
	writeCatalog(t, dir, "ngc.csv", "name,ra,dec,diameter,mag,alias\nNGC1,1.816245,27.708889,1.7,12.9,\n")

	tests := []struct {
		name, query, want string
		ok                bool
	}{
		{"primary name", "M101", "210.80208,54.348917", true},
		{"spaces + case", "m 101", "210.80208,54.348917", true},
		{"alias NGC", "NGC5457", "210.80208,54.348917", true},
		{"alias words", "Pinwheel Galaxy", "210.80208,54.348917", true},
		{"other catalog", "NGC1", "1.816245,27.708889", true},
		{"unknown", "M999", "", false},
		{"empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Resolve(tt.query, dir)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

// With no readable on-disk catalogue dir, Resolve falls back to the embedded snapshot: a well-known
// object still resolves, while an empty or unknown name still fails.
func TestResolve_EmbeddedFallback(t *testing.T) {
	_, ok := Resolve("", "")
	assert.False(t, ok, "empty name is rejected before any lookup")

	_, ok = Resolve("NotARealObject999", "")
	assert.False(t, ok, "unknown object is absent from the embedded snapshot too")

	ra, dec, ok := ResolveCoords("M101", "")
	require.True(t, ok, "M101 should resolve from the embedded catalogue with no dir given")
	assert.InDelta(t, 210.8, ra, 1.0)
	assert.InDelta(t, 54.35, dec, 1.0)
}

func TestLoad_FallsBackToEmbedded(t *testing.T) {
	// An empty dir has no on-disk catalogue, so Load returns the (non-empty) embedded snapshot,
	// whereas the pure LoadCatalog returns nothing.
	assert.NotEmpty(t, Load(t.TempDir()).Records())

	pure, err := LoadCatalog(t.TempDir())
	require.NoError(t, err)
	assert.Empty(t, pure.Records())
}

func TestLoad_PrefersOnDisk(t *testing.T) {
	// When dir has a readable catalogue, Load uses it verbatim (it does not merge in the embedded one).
	dir := t.TempDir()
	writeCatalog(t, dir, "messier.csv", "name,ra,dec\nM1,83.6,22.0\n")
	recs := Load(dir).Records()
	require.Len(t, recs, 1)
	assert.Equal(t, "M1", recs[0].Name)
}
