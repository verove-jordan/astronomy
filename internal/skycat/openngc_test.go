package skycat

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenNGC_EnrichesEmbeddedCatalog: the embedded OpenNGC overlay attaches authoritative types and
// common names onto the Siril records — including via a Messier record's NGC alias — so famous galaxies
// no longer read as "other".
func TestOpenNGC_EnrichesEmbeddedCatalog(t *testing.T) {
	cat := Load(t.TempDir()) // empty dir → embedded snapshot (with the overlay applied)

	tests := []struct {
		lookup, wantType, wantCommon string
	}{
		{"NGC6946", "G", "Fireworks Galaxy"}, // the reported "Other"-labelled galaxy
		{"NGC5194", "G", "Whirlpool Galaxy"}, // NGC name
		{"M31", "G", "Andromeda Galaxy"},     // Messier record enriched via its NGC224 alias
		{"NGC7000", "HII", "North America Nebula"},
	}
	for _, tt := range tests {
		t.Run(tt.lookup, func(t *testing.T) {
			rec, ok := cat.Lookup(tt.lookup)
			require.True(t, ok, "%s in catalogue", tt.lookup)
			assert.Equal(t, tt.wantType, rec.Type, "%s OpenNGC type", tt.lookup)
			assert.Contains(t, strings.Join(rec.CommonNames, "/"), tt.wantCommon)
		})
	}
}

// TestOpenNGC_EllipseAndSurfaceBrightness: the overlay supplies the true ellipse (major + minor axes) and
// a catalogued surface brightness where the Siril row had none.
func TestOpenNGC_EllipseAndSurfaceBrightness(t *testing.T) {
	cat := Load(t.TempDir())
	rec, ok := cat.Lookup("NGC6946")
	require.True(t, ok)
	assert.True(t, rec.HasDiameter && rec.DiameterArcmin > 0, "major axis")
	assert.True(t, rec.HasMinorAxis && rec.MinorAxisArcmin > 0, "minor axis")
	assert.Less(t, rec.MinorAxisArcmin, rec.DiameterArcmin+0.01, "minor ≤ major")
	assert.True(t, rec.HasSurfBr && rec.SurfBr > 0, "surface brightness")
	assert.NotEmpty(t, rec.Morphology, "Hubble morphology")
}

// TestParseOpenNGC_SkipsBadRows: a header-only or type-less row never produces an entry.
func TestParseOpenNGC_SkipsBadRows(t *testing.T) {
	in := "name,type,majax,minax,posang,surfbr,hubble,messier,common\n" +
		"NGC1,G,1.7,1.2,90,22.5,Sb,,Alpha\n" +
		",G,1,1,,,,,\n" + // no name → skipped
		"NGC2,,,,,,,,\n" // no type → skipped
	m, err := parseOpenNGC(strings.NewReader(in))
	require.NoError(t, err)
	assert.Len(t, m, 1)
	e := m[normalize("NGC1")]
	assert.Equal(t, "G", e.Type)
	assert.Equal(t, []string{"Alpha"}, e.CommonNames)
}
