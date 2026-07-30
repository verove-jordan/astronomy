package deepstars

import (
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoad_EmbeddedCatalogueSanity(t *testing.T) {
	stars, err := load()
	require.NoError(t, err)
	require.Greater(t, len(stars), 50_000, "the embedded catalogue must be the full extract")
	assert.Equal(t, len(stars), Count())

	for _, s := range stars {
		require.GreaterOrEqual(t, s.RADeg, 0.0)
		require.Less(t, s.RADeg, 360.0)
		require.GreaterOrEqual(t, s.DecDeg, -90.0)
		require.LessOrEqual(t, s.DecDeg, 90.0)
		require.LessOrEqual(t, s.Mag, 9.01)
	}
	assert.True(t, sort.SliceIsSorted(stars, func(i, j int) bool { return stars[i].Mag < stars[j].Mag }),
		"catalogue must be magnitude-ascending after load")
}

func TestInField(t *testing.T) {
	epoch := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	t.Run("Orion contains its bright stars, mag-ascending", func(t *testing.T) {
		got := InField(83.0, 0.0, 10.0, 0, epoch)
		require.NotEmpty(t, got)
		names := map[string]bool{}
		for _, s := range got {
			names[s.Proper] = true
		}
		assert.True(t, names["Rigel"], "Rigel in a 10° Orion field")
		assert.True(t, names["Betelgeuse"], "Betelgeuse in a 10° Orion field")
		assert.True(t, sort.SliceIsSorted(got, func(i, j int) bool { return got[i].Mag < got[j].Mag }))
	})

	t.Run("cap honored and keeps the brightest", func(t *testing.T) {
		all := InField(83.0, 0.0, 10.0, 0, epoch)
		capped := InField(83.0, 0.0, 10.0, 5, epoch)
		require.Len(t, capped, 5)
		assert.Equal(t, all[:5], capped)
	})

	t.Run("field straddling RA=0 finds stars on both sides", func(t *testing.T) {
		got := InField(359.9, 28.1, 2.0, 0, epoch)
		require.NotEmpty(t, got)
		low, high := false, false
		for _, s := range got {
			if s.RADeg < 180 {
				high = true // wrapped past 0h
			} else {
				low = true
			}
		}
		assert.True(t, low && high, "expected stars on both sides of RA=0 (got low=%v high=%v)", low, high)
	})

	t.Run("polar field is non-empty and includes Polaris", func(t *testing.T) {
		got := InField(0, 89.5, 2.0, 0, epoch)
		require.NotEmpty(t, got)
		found := false
		for _, s := range got {
			if s.Proper == "Polaris" {
				found = true
			}
		}
		assert.True(t, found, "Polaris within 2° of the pole")
	})

	t.Run("tiny deep-sky field is mostly labelable", func(t *testing.T) {
		// M101-sized field: ~0.8° radius. A few % of catalogue rows are Hipparcos-only
		// (no designation at all — the label stage filters those); the rest must label.
		got := InField(210.8, 54.35, 0.8, 0, epoch)
		require.NotEmpty(t, got)
		labeled := 0
		for _, s := range got {
			if s.Primary() != "" {
				labeled++
			}
		}
		assert.GreaterOrEqual(t, labeled, len(got)*8/10, "≥80%% of field stars carry a designation")
	})
}

func TestInField_ProperMotion(t *testing.T) {
	// Barnard's Star (HIP 87937, mag 9.5) is fainter than the mag-9 cut — use the fastest bright
	// mover instead: 61 Cygni (~5.2″/yr combined) or Kapteyn's star. Verify via 61 Cyg A (HD 201091,
	// pm ≈ (4133, 3201) mas/yr): 26.5 years must move it ≈ 2.3′ from J2000.
	stars, err := load()
	require.NoError(t, err)
	var cyg61 Star
	for _, s := range stars {
		if s.HD == 201091 {
			cyg61 = s
			break
		}
	}
	require.NotZero(t, cyg61.HD, "61 Cyg A (HD 201091) must be in the catalogue")

	epoch := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	got := InField(cyg61.RADeg, cyg61.DecDeg, 0.2, 0, epoch)
	var moved Star
	for _, s := range got {
		if s.HD == 201091 {
			moved = s
			break
		}
	}
	require.NotZero(t, moved.HD)

	// Expected displacement after ~26.5 years: pm total ≈ √(4133²+3201²) ≈ 5227 mas/yr → ≈ 2.31′.
	dDec := (moved.DecDeg - cyg61.DecDeg) * 3600
	assert.InDelta(t, float64(cyg61.PMDec)/1000*26.5, dDec, 6, "dec advanced by pmdec·Δt (arcsec)")
	assert.Greater(t, dDec, 60.0, "61 Cyg must have moved > 1′ in dec since J2000")
}

func TestStar_Designations(t *testing.T) {
	tests := []struct {
		name      string
		star      Star
		primary   string
		secondary string
	}{
		{
			name:      "proper name wins, Bayer second",
			star:      Star{Proper: "Vega", Bayer: "Alp", Con: "Lyr", Flam: 3, HD: 172167},
			primary:   "Vega",
			secondary: "α Lyr",
		},
		{
			name:      "Bayer with component superscript",
			star:      Star{Bayer: "The-2", Con: "Ori", HD: 37041},
			primary:   "θ² Ori",
			secondary: "HD 37041",
		},
		{
			name:      "Flamsteed when no proper/Bayer",
			star:      Star{Flam: 61, Con: "Cyg", HD: 201091},
			primary:   "61 Cyg",
			secondary: "HD 201091",
		},
		{
			name:      "HD-only field star",
			star:      Star{HD: 122064},
			primary:   "HD 122064",
			secondary: "",
		},
		{
			name:      "nothing at all",
			star:      Star{},
			primary:   "",
			secondary: "",
		},
		{
			name:      "unknown Bayer token degrades to the next tier",
			star:      Star{Bayer: "Zzz", Con: "Ori", HD: 1},
			primary:   "HD 1",
			secondary: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.primary, tt.star.Primary())
			assert.Equal(t, tt.secondary, tt.star.Secondary())
		})
	}
}

func TestEmbedded_KnownStars(t *testing.T) {
	stars, err := load()
	require.NoError(t, err)
	byHD := map[int]Star{}
	for _, s := range stars {
		if s.HD != 0 {
			byHD[s.HD] = s
		}
	}
	sirius := byHD[48915]
	require.NotZero(t, sirius.HD)
	assert.Equal(t, "Sirius", sirius.Proper)
	assert.Equal(t, "α CMa", sirius.Secondary())
	assert.InDelta(t, -1.44, sirius.Mag, 0.1)
}
