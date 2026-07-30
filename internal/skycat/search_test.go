package skycat

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestCatalog builds a catalogue straight from records (rather than CSV fixtures) so a test can
// pin CommonNames, which only the OpenNGC overlay would otherwise supply.
func newTestCatalog(recs ...*Record) *Catalog {
	c := &Catalog{index: map[string]*Record{}}
	for _, r := range recs {
		c.add(r)
	}
	return c
}

func searchFixture() *Catalog {
	return newTestCatalog(
		&Record{
			Name: "M31", RADeg: 10.68, DecDeg: 41.27, Source: "messier",
			MagV: 3.4, HasMag: true,
			Aliases:     []string{"NGC224"},
			CommonNames: []string{"Andromeda Galaxy"},
		},
		&Record{
			Name: "M110", RADeg: 10.09, DecDeg: 41.68, Source: "messier",
			MagV: 8.1, HasMag: true,
			Aliases: []string{"NGC205"},
		},
		&Record{
			Name: "NGC7000", RADeg: 314.75, DecDeg: 44.31, Source: "ngc",
			MagV: 4, HasMag: true,
			CommonNames: []string{"North America Nebula"},
		},
		&Record{
			Name: "NGC7008", RADeg: 316.0, DecDeg: 54.5, Source: "ngc",
		},
		&Record{
			Name: "IC5070", RADeg: 313.9, DecDeg: 44.36, Source: "ic",
			CommonNames: []string{"Pelican Nebula"},
		},
		&Record{
			Name: "Sh2-131", RADeg: 315.4, DecDeg: 57.5, Source: "sh2",
		},
	)
}

func TestCatalog_Search(t *testing.T) {
	cat := searchFixture()

	tests := []struct {
		name      string
		query     string
		limit     int
		wantFirst string
		wantNames []string // exact expected result set, in order (nil = only check the first hit)
	}{
		{
			name: "exact primary name outranks substring hits", query: "M31", limit: 5,
			wantFirst: "M31",
		},
		{
			name: "punctuation and case are ignored", query: "m 31", limit: 5,
			wantFirst: "M31",
		},
		{
			name: "alias resolves to the merged record", query: "ngc224", limit: 5,
			wantNames: []string{"M31"},
		},
		{
			name: "common name is searchable", query: "andromeda", limit: 5,
			wantNames: []string{"M31"},
		},
		{
			name: "multi-word common name matches without spaces", query: "north america", limit: 5,
			wantNames: []string{"NGC7000"},
		},
		{
			name: "prefix match on a unique designation", query: "IC50", limit: 5,
			wantNames: []string{"IC5070"},
		},
		{
			name: "shared prefix returns both, catalogue order breaks the tie", query: "NGC70", limit: 5,
			wantNames: []string{"NGC7000", "NGC7008"},
		},
		{
			name: "no match yields nothing", query: "zzzz", limit: 5,
			wantNames: []string{},
		},
		{
			name: "blank query yields nothing", query: "   ", limit: 5,
			wantNames: []string{},
		},
		{
			name: "limit caps the result set", query: "NGC70", limit: 1,
			wantNames: []string{"NGC7000"},
		},
		{
			name: "zero limit yields nothing", query: "M31", limit: 0,
			wantNames: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cat.Search(tt.query, tt.limit)
			names := make([]string, len(got))
			for i, r := range got {
				names[i] = r.Name
			}
			if tt.wantNames != nil {
				assert.Equal(t, tt.wantNames, names)
				return
			}
			require.NotEmpty(t, names)
			assert.Equal(t, tt.wantFirst, names[0])
		})
	}
}

func TestCatalog_Search_TieBreaksOnCatalogueThenBrightness(t *testing.T) {
	cat := newTestCatalog(
		&Record{Name: "NGC1976", Source: "ngc", MagV: 4, HasMag: true, CommonNames: []string{"Orion Nebula"}},
		&Record{Name: "M42x", Source: "messier", MagV: 9, HasMag: true, CommonNames: []string{"Orion Nebula"}},
		&Record{Name: "IC434", Source: "ic", CommonNames: []string{"Orion Nebula"}},
	)

	got := cat.Search("orion nebula", 5)
	require.Len(t, got, 3)
	// All three match the common name exactly, so the catalogue rank decides: Messier, NGC, IC —
	// even though the Messier row is the faintest.
	assert.Equal(t, []string{"M42x", "NGC1976", "IC434"}, []string{got[0].Name, got[1].Name, got[2].Name})
}

func TestCatalog_Search_PrefersRecordsWithPhotometry(t *testing.T) {
	cat := newTestCatalog(
		&Record{Name: "NGC6960", Source: "ngc"},
		&Record{Name: "NGC6992", Source: "ngc", MagV: 7, HasMag: true},
	)

	got := cat.Search("NGC69", 5)
	require.Len(t, got, 2)
	assert.Equal(t, "NGC6992", got[0].Name, "a catalogued magnitude sorts ahead of an unknown one")
}
