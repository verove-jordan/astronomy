package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/verove-jordan/astronomy/internal/gimp"
	"github.com/verove-jordan/astronomy/internal/mode"
)

// internal/gimp is deliberately dependency-free, so it carries its own copy of the gold-tint string
// (gimp.siiTintGold, pinned by TestSIITintGoldWireValue there). This is the other half of that pin:
// a rename here that silently turned every gold render deep red would pass every other test.
func TestSIITintGoldWireValue(t *testing.T) {
	assert.Equal(t, "gold", mode.SIITintGold,
		"the wire value gimp branches on — see internal/gimp/compose.go siiTintGold")
	assert.Equal(t, "deep_red", mode.SIITintDeepRed)
}

func TestEmissionScreens_EnabledGating(t *testing.T) {
	natural := paletteResolved{
		Name: "natural", R: "R", G: "G", B: "B", Color: true,
		HaScreen: true, OIIIScreen: true, SIIScreen: true,
	}

	tests := []struct {
		name   string
		pal    paletteResolved
		preset *mode.Preset
		want   map[string]bool // filter → enabled
	}{
		{
			// The opt-in default: Ha screens as it always has, OIII and SII stay off so an existing
			// run is untouched.
			name:   "opt-in knobs default off",
			pal:    natural,
			preset: &mode.Preset{},
			want:   map[string]bool{"Ha": true, "OIII": false, "SII": false},
		},
		{
			name:   "all three on when asked",
			pal:    natural,
			preset: &mode.Preset{OIIIScreen: 0.4, SIIScreen: 0.35},
			want:   map[string]bool{"Ha": true, "OIII": true, "SII": true},
		},
		{
			// Under SHO the emission lines ARE the base channels; screening one on top of itself
			// would double-count the line.
			name: "narrowband palette screens nothing",
			pal: paletteResolved{
				Name: "sho", R: "SII", G: "Ha", B: "OIII", Color: true, Narrowband: true,
			},
			preset: &mode.Preset{OIIIScreen: 0.4, SIIScreen: 0.35},
			want:   map[string]bool{"Ha": false, "OIII": false, "SII": false},
		},
		{
			name:   "nil preset screens Ha only",
			pal:    natural,
			preset: nil,
			want:   map[string]bool{"Ha": true, "OIII": false, "SII": false},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var in gimp.Inputs
			got := map[string]bool{}
			for _, es := range emissionScreens(tt.pal, tt.preset, &in) {
				got[es.filter] = es.enabled
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// Each screen must own a DISTINCT destination, stat file and scratch base — sharing any of them
// would make one layer silently overwrite another's stretched TIFF.
func TestEmissionScreens_DistinctOutputs(t *testing.T) {
	var in gimp.Inputs
	screens := emissionScreens(paletteResolved{}, &mode.Preset{}, &in)

	bases, stats := map[string]bool{}, map[string]bool{}
	for _, es := range screens {
		assert.False(t, bases[es.base], "duplicate scratch base %q", es.base)
		assert.False(t, stats[es.statBase], "duplicate stat base %q", es.statBase)
		bases[es.base], stats[es.statBase] = true, true
	}

	// The dest pointers must address the three different Inputs fields.
	for i, es := range screens {
		*es.dest = "written"
		switch i {
		case 0:
			assert.Equal(t, "written", in.Ha)
		case 1:
			assert.Equal(t, "written", in.OIII)
		case 2:
			assert.Equal(t, "written", in.SII)
		}
		*es.dest = ""
	}
}

// The wash gate multiplies the retuned opacity, so a Tier-A re-render of an attenuated layer stays
// attenuated instead of jumping back to full strength.
func TestEmissionScreens_ApplyGateCarriesTheFactor(t *testing.T) {
	in := gimp.Inputs{}
	p := &mode.Preset{OIIIScreen: 0.5, OIIIBlackPoint: 0.06, SIIScreen: 0.4, SIIBlackPoint: 0.05, SIITint: mode.SIITintGold}
	for _, es := range emissionScreens(
		paletteResolved{HaScreen: true, OIIIScreen: true, SIIScreen: true}, p, &in) {
		if es.applyGate != nil {
			es.applyGate(0.5)
		}
	}
	assert.InDelta(t, 0.5, in.HaScreenFactor, 1e-9)
	assert.InDelta(t, 0.25, in.OIIIScreen, 1e-9, "0.5 opacity × 0.5 gate")
	assert.InDelta(t, 0.20, in.SIIScreen, 1e-9, "0.4 opacity × 0.5 gate")
	assert.InDelta(t, 0.05, in.SIIBlack, 1e-9)
	assert.Equal(t, mode.SIITintGold, in.SIITint)
}

// screenOnly is what keeps an additive emission layer from shrinking a multi-night mosaic to its own
// footprint. It used to be hardcoded to Ha, so OIII already had this bug and SII would have joined it.
func TestPaletteResolved_ScreenOnly(t *testing.T) {
	natural := paletteResolved{
		R: "R", G: "G", B: "B", HaScreen: true, OIIIScreen: true, SIIScreen: true,
	}
	assert.True(t, natural.screenOnly("Ha"))
	assert.True(t, natural.screenOnly("OIII"))
	assert.True(t, natural.screenOnly("SII"))
	assert.False(t, natural.screenOnly("R"))
	assert.False(t, natural.screenOnly("L"))

	// Under SHO these are BASE channels — they must keep constraining the crop.
	sho := paletteResolved{R: "SII", G: "Ha", B: "OIII", Narrowband: true}
	assert.False(t, sho.screenOnly("Ha"))
	assert.False(t, sho.screenOnly("OIII"))
	assert.False(t, sho.screenOnly("SII"))

	// A no-B run folds OIII into the blue slot: also a base channel, not a screen.
	noB := paletteResolved{R: "R", G: "G", B: "OIII", HaScreen: true, OIIIScreen: true}
	assert.False(t, noB.screenOnly("OIII"))
	assert.True(t, noB.screenOnly("Ha"))
}

// The natural family exposes every emission line it actually has; a narrowband palette exposes none,
// because there they are the base channels.
func TestResolvePalette_SIIScreenFlag(t *testing.T) {
	all := map[string]string{"L": "l", "R": "r", "G": "g", "B": "b", "Ha": "ha", "OIII": "o3", "SII": "s2"}

	natural, _ := resolvePalette(&mode.Preset{}, all)
	assert.True(t, natural.HaScreen)
	assert.True(t, natural.OIIIScreen)
	assert.True(t, natural.SIIScreen)

	sho, _ := resolvePalette(&mode.Preset{Palette: "sho"}, all)
	assert.Equal(t, "sho", sho.Name)
	assert.Equal(t, "SII", sho.R, "SHO maps SII to red")
	assert.False(t, sho.SIIScreen, "a narrowband palette consumes SII as a base channel")

	// No SII captured → no SII screen, whatever the knob says.
	noSII, _ := resolvePalette(&mode.Preset{SIIScreen: 0.4},
		map[string]string{"L": "l", "R": "r", "G": "g", "B": "b"})
	assert.False(t, noSII.SIIScreen)
}

// An LRGB+SII run used to report a bare "LRGB", hiding the narrowband from run.json and the UI.
func TestCompMode_NamesEveryEmissionLine(t *testing.T) {
	ch := func(fs ...string) map[string]string {
		m := map[string]string{}
		for _, f := range fs {
			m[f] = "x"
		}
		return m
	}
	tests := []struct {
		name string
		in   map[string]string
		want string
	}{
		{"plain RGB", ch("R", "G", "B"), "RGB"},
		{"LRGB", ch("L", "R", "G", "B"), "LRGB"},
		{"Ha keeps its historical spelling", ch("L", "R", "G", "B", "Ha"), "HaLRGB"},
		{"Ha without L", ch("R", "G", "B", "Ha"), "HaRGB"},
		{"SII alone is no longer invisible", ch("L", "R", "G", "B", "SII"), "SIILRGB"},
		{"every line, canonical order", ch("L", "R", "G", "B", "Ha", "OIII", "SII"), "HaOIIISIILRGB"},
		{"no RGB is mono", ch("L", "Ha"), "mono"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, compMode(tt.in, &mode.Preset{}))
		})
	}
}
