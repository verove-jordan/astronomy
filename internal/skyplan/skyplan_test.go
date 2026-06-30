package skyplan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/skycat"
)

// fc100 is the user's reference rig: Takahashi FC-100 DF + ZWO ASI 1600MM Pro.
var fc100 = Optics{FocalMM: 740, ApertureMM: 100, PixelUm: 3.8, SensorWpx: 4656, SensorHpx: 3520}

func TestOptics_FOV(t *testing.T) {
	assert.InDelta(t, 1.059, fc100.ImageScale(), 0.01)
	w, h := fc100.FOV()
	assert.InDelta(t, 1.370, w, 0.01)
	assert.InDelta(t, 1.036, h, 0.01)
	assert.InDelta(t, 7.4, fc100.FRatio(), 0.001)
}

func TestFramingScore(t *testing.T) {
	const fov = 60.0 // arcmin
	tests := []struct {
		name  string
		diam  float64
		known bool
		want  float64
		wantK bool
	}{
		{"unknown diameter", 0, false, 0.5, false},
		{"zero fov-relative tiny", 0.3, true, 0.10, true}, // f=0.005
		{"plateau low", 6, true, 1.0, true},               // f=0.10
		{"plateau mid", 18, true, 1.0, true},              // f=0.30
		{"plateau high", 36, true, 1.0, true},             // f=0.60
		{"oversized", 120, true, 0.20, true},              // f=2.0
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, known := framingScore(tt.diam, fov, tt.known)
			assert.InDelta(t, tt.want, got, 1e-9)
			assert.Equal(t, tt.wantK, known)
		})
	}
	// A target that just fills the frame scores between the plateau and the oversized floor.
	tight, _ := framingScore(48, fov, true) // f=0.8
	assert.Greater(t, tight, fillOversizeFloor)
	assert.Less(t, tight, 1.0)
}

func TestDetectabilityScore(t *testing.T) {
	t.Run("unknown is neutral and flagged", func(t *testing.T) {
		got, known := detectabilityScore(0, 0, 100, false)
		assert.Equal(t, 0.5, got)
		assert.False(t, known)
	})
	t.Run("monotone in aperture", func(t *testing.T) {
		small, _ := detectabilityScore(10, 10, 100, true)
		big, _ := detectabilityScore(10, 10, 200, true)
		assert.Greater(t, big, small)
	})
	t.Run("monotone in brightness", func(t *testing.T) {
		bright, _ := detectabilityScore(8, 10, 100, true)
		faint, _ := detectabilityScore(13, 10, 100, true)
		assert.Greater(t, bright, faint)
	})
}

func TestMoonScore(t *testing.T) {
	assert.Equal(t, 1.0, moonScore(false, 1.0, 50, 5, 1.0), "moon below horizon never penalizes")

	dim := moonScore(true, 0.2, 60, 30, 1.0)
	bright := moonScore(true, 0.9, 60, 30, 1.0)
	assert.Greater(t, dim, bright, "a brighter moon hurts more")

	near := moonScore(true, 0.8, 60, 15, 1.0)
	far := moonScore(true, 0.8, 60, 85, 1.0)
	assert.Greater(t, far, near, "more separation hurts less")
}

func TestDeriveType(t *testing.T) {
	tests := []struct {
		rec  skycat.Record
		want string
	}{
		{skycat.Record{Name: "Sh2-155", Source: "sh2"}, "emission_nebula"},
		{skycat.Record{Name: "LdN-1", Source: "ldn"}, "dark_nebula"},
		{skycat.Record{Name: "M101", Source: "messier", Aliases: []string{"Pinwheel galaxy"}}, "galaxy"},
		{skycat.Record{Name: "M13", Source: "messier", Aliases: []string{"Hercules globular cluster"}}, "globular"},
		{skycat.Record{Name: "NGC1", Source: "ngc"}, "other"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, deriveType(tt.rec))
		})
	}
}

func TestComposition(t *testing.T) {
	t.Run("curated planetary is OIII-strong HOO", func(t *testing.T) {
		c := compositionFor(skycat.Record{Name: "M27"}, "planetary_nebula")
		assert.Equal(t, "curated", c.Source)
		assert.Equal(t, "HOO", c.Palette)
		assert.Greater(t, c.OIII, c.SII)
	})
	t.Run("curated match by designation", func(t *testing.T) {
		c := compositionFor(skycat.Record{Name: "NGC6960"}, "supernova_remnant")
		assert.Equal(t, "curated", c.Source)
	})
	t.Run("galaxy falls back to broadband", func(t *testing.T) {
		c := compositionFor(skycat.Record{Name: "NGC9999"}, "galaxy")
		assert.Equal(t, "typical", c.Source)
		assert.True(t, c.Broadband)
		assert.Contains(t, c.Filters, "L")
	})
	t.Run("emission nebula heuristic is Hα-dominant SHO", func(t *testing.T) {
		c := compositionFor(skycat.Record{Name: "Sh2-999", Source: "sh2"}, "emission_nebula")
		assert.Equal(t, "SHO", c.Palette)
		assert.GreaterOrEqual(t, c.Ha, c.OIII)
		assert.False(t, c.Broadband)
	})
}

func TestPlan_GatesAndRanks(t *testing.T) {
	dir := t.TempDir()
	// M81: high-declination spring galaxy, well placed from Paris. 47 Tuc: far-southern, never up.
	writeFile(t, dir, "messier.csv",
		"name,ra,dec,diameter,mag,alias\nM81,148.888,69.065,26.9,6.9,Bode's Galaxy/NGC3031\n")
	writeFile(t, dir, "ngc.csv",
		"name,ra,dec,diameter,mag,alias\nNGC104,6.0238,-72.081,50,4.0,47 Tucanae\n")

	planner := New(dir)
	res, err := planner.Plan(context.Background(), Params{
		At:        time.Date(2026, time.March, 15, 23, 0, 0, 0, time.UTC),
		Lat:       48.8566,
		Lon:       2.3522,
		Optics:    fc100,
		MinAltDeg: 30,
		Twilight:  "astro",
		Limit:     50,
		Weights:   DefaultWeights(),
		Location:  time.UTC,
	})
	require.NoError(t, err)
	require.Equal(t, 2, res.Count)

	assert.Equal(t, "astronomical", res.Darkness.Kind)
	assert.False(t, res.Darkness.NoAstroDark)
	assert.Greater(t, res.Darkness.DawnUTCMs, res.Darkness.DuskUTCMs)

	byName := map[string]Target{}
	for _, tg := range res.Targets {
		byName[tg.Name] = tg
	}

	m81 := byName["M81"]
	assert.True(t, m81.Flags.Visible)
	assert.Greater(t, m81.Score, 0)
	assert.Greater(t, m81.DarkHoursAboveMin, 0.0)
	assert.NotEmpty(t, m81.AltSeries)

	tuc := byName["NGC104"]
	assert.False(t, tuc.Flags.Visible)
	assert.Equal(t, 0, tuc.Score)

	// Ranked by score descending: the visible galaxy outranks the gated cluster.
	assert.Equal(t, "M81", res.Targets[0].Name)
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644))
}

func TestComputeNight(t *testing.T) {
	prm := Params{
		At:       time.Date(2026, time.January, 15, 23, 0, 0, 0, time.UTC),
		Lat:      48.8566,
		Lon:      2.3522,
		Location: time.UTC,
	}
	dark := astro.NightWindow(prm.At, prm.Lat, prm.Lon, -18)
	nc := computeNight(prm, dark)

	// The chart window brackets the astronomical dark window.
	assert.True(t, nc.start.Before(dark.Start), "night should start before dusk")
	assert.True(t, dark.End.Before(nc.end), "night should end after dawn")
	assert.NotEmpty(t, nc.sunSeries)
	assert.NotEmpty(t, nc.moonSeries)
	// A mid-latitude winter night has a real sunset before sunrise.
	require.True(t, nc.hasSunSet)
	require.True(t, nc.hasSunRise)
	assert.True(t, nc.sunSet.Before(nc.sunRise))
}
