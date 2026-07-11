package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/mode"
)

// chans builds a filter→aligned-basename map for the given filters (mirrors prepGimpInputs's channels).
func chans(filters ...string) map[string]string {
	m := map[string]string{}
	for _, f := range filters {
		m[f] = "aligned_" + f
	}
	return m
}

func pal(name string, filters ...string) (paletteResolved, string) {
	return resolvePalette(&mode.Preset{Palette: name}, chans(filters...))
}

func TestResolvePalette_NaturalDefault(t *testing.T) {
	// Empty palette → natural; L present → luminance layer; Ha present → screen.
	got, note := resolvePalette(&mode.Preset{}, chans("L", "R", "G", "B", "Ha"))
	assert.Equal(t, "natural", got.Name)
	assert.Equal(t, "R", got.R)
	assert.Equal(t, "G", got.G)
	assert.Equal(t, "B", got.B)
	assert.True(t, got.Color)
	assert.True(t, got.UseLum)
	assert.True(t, got.HaScreen)
	assert.False(t, got.Narrowband)
	assert.Empty(t, note)

	// Natural without L or Ha: no luminance, no screen — still legacy R/G/B behaviour.
	noExtra, _ := pal("natural", "R", "G", "B")
	assert.False(t, noExtra.UseLum)
	assert.False(t, noExtra.HaScreen)
}

func TestResolvePalette_HaRGB(t *testing.T) {
	got, note := pal("hargb", "L", "R", "G", "B", "Ha")
	assert.Equal(t, "hargb", got.Name)
	assert.True(t, got.HaScreen)
	assert.Empty(t, note)

	// No Ha → falls back to natural, with a note.
	fb, fbNote := pal("hargb", "R", "G", "B")
	assert.Equal(t, "natural", fb.Name)
	assert.Contains(t, fbNote, "hargb")
	assert.Contains(t, fbNote, "natural")
}

func TestResolvePalette_Narrowband(t *testing.T) {
	// HOO: Ha→R, OIII→G+B; narrowband → no L luminance, no Ha screen.
	hoo, _ := pal("hoo", "Ha", "OIII")
	assert.Equal(t, "hoo", hoo.Name)
	assert.Equal(t, "Ha", hoo.R)
	assert.Equal(t, "OIII", hoo.G)
	assert.Equal(t, "OIII", hoo.B)
	assert.True(t, hoo.Narrowband)
	assert.False(t, hoo.UseLum)
	assert.False(t, hoo.HaScreen)

	// SHO (Hubble): SII→R, Ha→G, OIII→B.
	sho, _ := pal("sho", "SII", "Ha", "OIII")
	assert.Equal(t, "sho", sho.Name)
	assert.Equal(t, "SII", sho.R)
	assert.Equal(t, "Ha", sho.G)
	assert.Equal(t, "OIII", sho.B)

	// HOS (CFHT): Ha→R, OIII→G, SII→B.
	hos, _ := pal("hos", "SII", "Ha", "OIII")
	assert.Equal(t, "Ha", hos.R)
	assert.Equal(t, "OIII", hos.G)
	assert.Equal(t, "SII", hos.B)
}

func TestResolvePalette_Foraxx(t *testing.T) {
	got, _ := pal("foraxx", "Ha", "OIII")
	assert.Equal(t, "foraxx", got.Name)
	assert.Equal(t, "Ha", got.R)
	assert.Equal(t, "OIII", got.B)
	assert.Empty(t, got.G, "green comes from pixel math, not a direct channel")
	// The dynamic-green formula references the real aligned basenames.
	assert.Contains(t, got.GExpr, "$aligned_Ha$")
	assert.Contains(t, got.GExpr, "$aligned_OIII$")
	assert.NotContains(t, got.GExpr, "$Ha$", "filter tokens must be substituted to basenames")
}

func TestResolvePalette_FallbackChain(t *testing.T) {
	// SHO with only L/R/G/B/Ha (the user's current filters): sho → hoo → natural.
	got, note := pal("sho", "L", "R", "G", "B", "Ha")
	assert.Equal(t, "natural", got.Name)
	assert.Contains(t, note, "sho")
	assert.Contains(t, note, "SII") // sho's missing filters are named
	assert.Contains(t, note, "OIII")

	// SHO with no broadband at all → all the way to mono.
	monoFb, _ := pal("sho", "Ha")
	assert.True(t, monoFb.Mono)
	assert.False(t, monoFb.Color)
}

func TestResolvePalette_Mono(t *testing.T) {
	// Mono prefers L.
	withL, _ := pal("mono", "L", "R", "G", "B")
	assert.True(t, withL.Mono)
	assert.False(t, withL.Color)
	assert.Equal(t, "L", withL.R)

	// No L → prefer Ha.
	withHa, _ := pal("mono", "R", "G", "Ha")
	assert.Equal(t, "Ha", withHa.R)

	// Neither → first in the canonical order.
	rgb, _ := pal("mono", "R", "G", "B")
	assert.Equal(t, "R", rgb.R)
}

func TestIsPaletteName(t *testing.T) {
	assert.True(t, isPaletteName(""))      // natural
	assert.True(t, isPaletteName("SHO"))   // case-insensitive
	assert.True(t, isPaletteName(" hoo ")) // trimmed
	assert.False(t, isPaletteName("bogus"))
	for _, n := range PaletteNames() {
		assert.True(t, isPaletteName(n), "listed palette %q must validate", n)
	}
}

func TestResolvePalette_UnknownIsNatural(t *testing.T) {
	got, _ := pal("bogus", "R", "G", "B")
	assert.Equal(t, "natural", got.Name)
}
