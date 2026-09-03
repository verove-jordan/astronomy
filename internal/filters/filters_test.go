package filters

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestToken_Aliases(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{"luminance long", "Luminance", "L", true},
		{"clear is luminance", "clear", "L", true},
		{"red", "RED", "R", true},
		{"johnson V is green", "V", "G", true},
		{"blue", " blue ", "B", true},
		{"ha short", "Ha", "Ha", true},
		{"ha hyphenated", "H-alpha", "Ha", true},
		{"ha underscored", "h_alpha", "Ha", true},
		{"oiii", "oiii", "OIII", true},
		{"oiii numeric alias", "O3", "OIII", true},
		{"oiii hyphenated", "O-III", "OIII", true},
		{"oiii by element", "oxygen", "OIII", true},
		{"sii", "sii", "SII", true},
		{"sii numeric alias", "S2", "SII", true},
		{"sii hyphenated", "S-II", "SII", true},
		{"sii american spelling", "sulfur", "SII", true},
		{"sii british spelling", "SULPHUR", "SII", true},
		{"unknown passes through as not-a-filter", "Baader", "", false},
		{"empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Token(tt.raw)
			assert.Equal(t, tt.ok, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNormalize_KeepsUnknownVerbatim(t *testing.T) {
	assert.Equal(t, "SII", Normalize(" s2 "))
	assert.Equal(t, "Baader UHC", Normalize("  Baader UHC  "))
	assert.Equal(t, "", Normalize("   "))
}

func TestIsNarrowband(t *testing.T) {
	for _, f := range []string{"Ha", "OIII", "SII"} {
		assert.True(t, IsNarrowband(f), f)
	}
	for _, f := range []string{"L", "R", "G", "B", "", "Baader"} {
		assert.False(t, IsNarrowband(f), f)
	}
}

func TestRank_CanonicalOrderThenUnknown(t *testing.T) {
	assert.Equal(t, 0, Rank("L"))
	assert.Equal(t, 4, Rank("Ha"))
	assert.Equal(t, 6, Rank("SII"))
	assert.Equal(t, len(Canonical), Rank("Baader"))
}

func TestLess_SortsCanonicallyThenAlphabetically(t *testing.T) {
	got := []string{"SII", "zeta", "B", "Ha", "alpha", "L", "OIII", "R", "G"}
	sort.Slice(got, func(i, j int) bool { return Less(got[i], got[j]) })
	assert.Equal(t, []string{"L", "R", "G", "B", "Ha", "OIII", "SII", "alpha", "zeta"}, got)
}

func TestList_IsACopy(t *testing.T) {
	got := List()
	got[0] = "mutated"
	assert.Equal(t, "L", Canonical[0])
}

// The narrowband set must stay a subset of the canonical order, or Rank-based sorting and the
// emission-screen table would disagree about which filters exist.
func TestNarrowband_IsSubsetOfCanonical(t *testing.T) {
	for _, f := range Narrowband {
		assert.Less(t, Rank(f), len(Canonical), f)
	}
}

func TestIsColorAndIsMono(t *testing.T) {
	tests := []struct {
		in            string
		color, isMono bool
	}{
		{"RGB", true, false},
		{"rgb", true, false},
		{"OSC", true, false},
		{"Color", true, false},
		{"colour", true, false},
		{"Bayer", true, false},
		// An empty name is neither: it means "not known yet", and the inventory decides from the
		// pixels and headers. Reading it as colour would make every unlabeled mono capture look OSC.
		{"", false, false},
		{"  ", false, false},
		{"L", false, true},
		{"Ha", false, true},
		// A custom filter nobody has mapped yet is still evidence of a filter wheel.
		{"Custom", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.color, IsColor(tt.in), "IsColor(%q)", tt.in)
			assert.Equal(t, tt.isMono, IsMono(tt.in), "IsMono(%q)", tt.in)
		})
	}
}

// Color must never join Canonical: it would take a wheel rank, get an IsNarrowband answer and claim
// a slot in the emission-screen tables, none of which mean anything for a colour sensor.
func TestColor_IsNotACanonicalFilter(t *testing.T) {
	assert.Equal(t, len(Canonical), Rank(Color), "Color must sort after every real filter")
	assert.False(t, IsNarrowband(Color))
	_, ok := Token(Color)
	assert.False(t, ok, "Color is not a filter-wheel token")
}
