package skycat

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadCatalog(t *testing.T) {
	dir := t.TempDir()
	// M1 = NGC1952 = Sh2-244 (three designations, one object). Messier is loaded first.
	writeCatalog(t, dir, "messier.csv",
		"name,ra,dec,diameter,mag,alias\nM1,83.63322,22.014467,6,8.4,Crab nebula/NGC1952/Sh2-244\n")
	writeCatalog(t, dir, "ngc.csv",
		"name,ra,dec,diameter,mag,alias\n"+
			"NGC1952,83.63,22.01,5,8.9,\n"+ // duplicate of M1 with a different mag (must NOT win)
			"NGC7000,314.75,44.31,120,4,North America Nebula\n")
	writeCatalog(t, dir, "ic.csv",
		"name,ra,dec,diameter,mag,alias\n"+
			"IC1,2.113755,27.719167,,,\n"+ // empty diameter AND mag
			"IC2,2.75334,-12.822222,0.8,14.7,\n")
	writeCatalog(t, dir, "sh2.csv", // 4-column file (no diameter/mag)
		"name,ra,dec,alias\nSh2-244,83.6,22.0,\nSh2-1,239.71335,-26.120551,\n")
	writeCatalog(t, dir, "ldn.csv", // 3-column file
		"name,ra,dec\nLdN-1,247.2144,-16.109431\n")

	cat, err := LoadCatalog(dir)
	require.NoError(t, err)

	t.Run("dedup merges duplicates across catalogs", func(t *testing.T) {
		recs := cat.Records()
		assert.Len(t, recs, 6) // M1, NGC7000, IC1, IC2, Sh2-1, LdN-1
		for _, r := range recs {
			assert.NotEqual(t, "NGC1952", r.Name, "NGC1952 must be merged into M1")
			assert.NotEqual(t, "Sh2-244", r.Name, "Sh2-244 must be merged into M1")
		}
	})

	t.Run("merged record keeps the Messier identity and photometry", func(t *testing.T) {
		m1, ok := cat.Lookup("M1")
		require.True(t, ok)
		assert.Equal(t, "messier", m1.Source)
		assert.True(t, m1.HasMag)
		assert.InDelta(t, 8.4, m1.MagV, 1e-9) // messier value, not NGC's 8.9
		assert.True(t, m1.HasDiameter)
	})

	t.Run("aliases resolve to the merged record", func(t *testing.T) {
		viaNGC, ok := cat.Lookup("ngc1952")
		require.True(t, ok)
		assert.Equal(t, "M1", viaNGC.Name)
		viaSh2, ok := cat.Lookup("Sh2-244")
		require.True(t, ok)
		assert.Equal(t, "M1", viaSh2.Name)
		viaWords, ok := cat.Lookup("North America Nebula")
		require.True(t, ok)
		assert.Equal(t, "NGC7000", viaWords.Name)
	})

	t.Run("empty photometry yields Has* false, not zero values", func(t *testing.T) {
		ic1, ok := cat.Lookup("IC1")
		require.True(t, ok)
		assert.False(t, ic1.HasMag)
		assert.False(t, ic1.HasDiameter)
		ic2, ok := cat.Lookup("IC2")
		require.True(t, ok)
		assert.True(t, ic2.HasMag)
	})

	t.Run("header-driven parsing handles 4- and 3-column files", func(t *testing.T) {
		sh2, ok := cat.Lookup("Sh2-1")
		require.True(t, ok)
		assert.Equal(t, "sh2", sh2.Source)
		assert.False(t, sh2.HasMag)
		ldn, ok := cat.Lookup("LdN-1")
		require.True(t, ok)
		assert.Equal(t, "ldn", ldn.Source)
	})
}

func TestLoadCatalog_MissingDir(t *testing.T) {
	cat, err := LoadCatalog(t.TempDir()) // empty dir: no catalog files
	require.NoError(t, err)
	assert.Empty(t, cat.Records())
}
