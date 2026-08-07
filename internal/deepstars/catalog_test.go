package deepstars

import (
	"compress/gzip"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testEpoch = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

// csvRow renders one ATHYG source row. Only the columns the builder reads are filled; the rest are
// left blank exactly as the real file leaves them.
func csvRow(vals map[string]string) string {
	out := make([]string, len(athygHeader))
	for i, name := range athygHeader {
		out[i] = vals[name]
	}
	return strings.Join(out, ",")
}

// writeSource writes a gzipped ATHYG-shaped CSV. withHeader mirrors the real release, whose SECOND
// file has no header line at all.
func writeSource(t *testing.T, path string, withHeader bool, rows []string) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	gz := gzip.NewWriter(f)
	if withHeader {
		fmt.Fprintln(gz, strings.Join(athygHeader, ","))
	}
	for _, r := range rows {
		fmt.Fprintln(gz, r)
	}
	require.NoError(t, gz.Close())
}

// buildTestCatalog writes a small catalogue through the real Build path (so the test exercises the
// production encoder, not a test-only one) and opens it.
func buildTestCatalog(t *testing.T, rows []string) *Catalog {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "src.csv.gz")
	writeSource(t, src, true, rows)
	out := filepath.Join(dir, "athyg.bin")

	// The real builder refuses a suspiciously small catalogue; the guard is bypassed here by writing
	// through the same helpers Build uses, which is what the format round-trip is actually about.
	require.NoError(t, buildSmall(t, src, out))

	c, err := Open(out)
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })
	return c
}

// buildSmall runs Build's pipeline without the minimum-row guard, which exists to catch a truncated
// 2.5-million-star download and would otherwise reject every fixture.
func buildSmall(t *testing.T, src, out string) error {
	t.Helper()
	saved := minBuildRows
	minBuildRows = 1
	defer func() { minBuildRows = saved }()
	return Build(context.Background(), BuildOptions{Sources: []string{src}, OutPath: out})
}

func TestBuildAndOpen_RoundTrip(t *testing.T) {
	// Vega, with every optional field populated, plus a Tycho-only field star with none of them.
	vega := csvRow(map[string]string{
		"tyc": "3105-2070-1", "gaia": "2091622738385230976", "hip": "91262", "hd": "172167",
		"bayer": "Alp", "flam": "3", "con": "Lyr", "proper": "Vega",
		"ra": "18.61565900", "dec": "38.78368896", "dist": "7.6748",
		"mag": "0.03", "absmag": "0.60", "ci": "0.003", "rv": "-13.5",
		"pm_ra": "200.94", "pm_dec": "286.23", "spect": "A0V",
	})
	faint := csvRow(map[string]string{
		"tyc": "3105-1234-2", "con": "Lyr",
		"ra": "18.61565900", "dec": "38.80000000", "mag": "11.42",
	})
	c := buildTestCatalog(t, []string{vega, faint})

	require.True(t, c.Deep())
	assert.Equal(t, 2, c.Count())

	got := c.InField(279.2349, 38.7837, 0.5, 10, j2000)
	require.Len(t, got, 2)

	v := got[0]
	assert.Equal(t, "Vega", v.Primary())
	assert.Equal(t, "α Lyr", v.Secondary())
	assert.InDelta(t, 279.2349, v.RADeg, 1e-4)
	assert.InDelta(t, 38.78369, v.DecDeg, 1e-4)
	assert.InDelta(t, 0.03, v.Mag, 1e-6)
	assert.InDelta(t, 7.6748, v.DistPc, 1e-3)
	assert.Equal(t, 172167, v.HD)
	assert.Equal(t, 91262, v.HIP)
	assert.Equal(t, "3105-2070-1", v.TYC)
	assert.Equal(t, uint64(2091622738385230976), v.Gaia)
	assert.Equal(t, "A0V", v.Spect)
	assert.Equal(t, "Lyr", v.Con)
	assert.Equal(t, 3, v.Flam)
	require.True(t, v.HasAbsMag)
	assert.InDelta(t, 0.60, v.AbsMag, 1e-6)
	require.True(t, v.HasCI)
	assert.InDelta(t, 0.003, v.CI, 1e-6)
	require.True(t, v.HasRV)
	assert.InDelta(t, -13.5, v.RVKmS, 1e-6)
	// Proper motion is stored to the nearest mas/yr — 1 mas/yr over a human lifetime is 0.1″, far
	// below the pixel scale, and it halves the record's proper-motion cost.
	assert.InDelta(t, 201, v.PMRA, 0.5)
	assert.InDelta(t, 286, v.PMDec, 0.5)

	f := got[1]
	assert.Equal(t, "TYC 3105-1234-2", f.Primary(), "a field star falls through to its Tycho id")
	assert.Zero(t, f.DistPc)
	assert.False(t, f.HasAbsMag)
	assert.False(t, f.HasCI, "a missing colour index must not read as B−V = 0")
	assert.False(t, f.HasRV, "a missing radial velocity must not read as 0 km/s")
	assert.Empty(t, f.Spect)
}

func TestBuild_SecondFileHasNoHeader(t *testing.T) {
	// The published release splits ONE csv by size, so the continuation file starts mid-document.
	// Feeding it through a header-expecting parser would silently eat its first star.
	dir := t.TempDir()
	a := filepath.Join(dir, "a.csv.gz")
	b := filepath.Join(dir, "b.csv.gz")
	writeSource(t, a, true, []string{csvRow(map[string]string{
		"tyc": "1-1-1", "ra": "1.0", "dec": "10.0", "mag": "5.0", "con": "And",
	})})
	writeSource(t, b, false, []string{csvRow(map[string]string{
		"tyc": "2-2-1", "ra": "2.0", "dec": "20.0", "mag": "6.0", "con": "And",
	})})

	out := filepath.Join(dir, "athyg.bin")
	saved := minBuildRows
	minBuildRows = 1
	defer func() { minBuildRows = saved }()
	require.NoError(t, Build(context.Background(), BuildOptions{Sources: []string{a, b}, OutPath: out}))

	c, err := Open(out)
	require.NoError(t, err)
	defer c.Close()
	assert.Equal(t, 2, c.Count(), "both halves contribute their stars")
}

func TestBuild_RejectsAChangedSchema(t *testing.T) {
	// The header-less second file forces the builder to APPLY a hard-coded column order, so an
	// upstream schema change would otherwise shift every field silently and produce a wrong sky.
	// Both shapes of change must stop the build.
	reordered := append([]string(nil), athygHeader...)
	reordered[12], reordered[13] = reordered[13], reordered[12] // ra ↔ dec

	tests := []struct {
		name   string
		header string
	}{
		{"fewer columns", "id,tyc,gaia,ra,dec,mag"},
		{"same count, a column renamed", strings.Replace(strings.Join(athygHeader, ","), "spect,", "sp_type,", 1)},
		{"same names, reordered", strings.Join(reordered, ",")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			src := filepath.Join(dir, "src.csv.gz")
			f, err := os.Create(src)
			require.NoError(t, err)
			gz := gzip.NewWriter(f)
			fmt.Fprintln(gz, tt.header)
			require.NoError(t, gz.Close())
			require.NoError(t, f.Close())

			err = Build(context.Background(), BuildOptions{Sources: []string{src}, OutPath: filepath.Join(dir, "out.bin")})
			require.Error(t, err, "a changed column set must fail the build, not produce a wrong sky")
		})
	}
}

func TestBuild_RAIsInHours(t *testing.T) {
	// The single easiest thing to get wrong: ATHYG stores RA in hours, like HYG. A star at 18h must
	// land at 270°, not at 18°.
	c := buildTestCatalog(t, []string{csvRow(map[string]string{
		"tyc": "1-1-1", "ra": "18.0", "dec": "0.0", "mag": "5.0", "con": "Sgr",
	})})
	got := c.InField(270, 0, 0.5, 10, j2000)
	require.Len(t, got, 1)
	assert.InDelta(t, 270.0, got[0].RADeg, 1e-4)
	assert.Empty(t, c.InField(18, 0, 0.5, 10, j2000), "nothing at 18° — that would be the hours/degrees bug")
}

func TestCatalog_InFieldGeometry(t *testing.T) {
	var rows []string
	// A ring of stars every 10° in declination along RA 6h, plus two near RA 0 to exercise the wrap.
	// Every magnitude is distinct so "brightest first" has exactly one right answer.
	for dec := -80; dec <= 80; dec += 10 {
		rows = append(rows, csvRow(map[string]string{
			"tyc": fmt.Sprintf("1-%d-1", dec+100), "ra": "6.0",
			"dec": fmt.Sprintf("%d.0", dec), "mag": fmt.Sprintf("%d.0", 10+dec/10),
		}))
	}
	rows = append(rows,
		csvRow(map[string]string{"tyc": "9-1-1", "ra": "23.99", "dec": "28.0", "mag": "0.7"}),
		csvRow(map[string]string{"tyc": "9-2-1", "ra": "0.02", "dec": "28.1", "mag": "0.6"}),
		csvRow(map[string]string{"tyc": "9-3-1", "ra": "12.0", "dec": "89.5", "mag": "0.5"}),
	)
	c := buildTestCatalog(t, rows)

	t.Run("declination band is inclusive at both ends", func(t *testing.T) {
		got := c.InField(90, 0, 10.5, 100, j2000)
		var decs []float64
		for _, s := range got {
			decs = append(decs, s.DecDeg)
		}
		sort.Float64s(decs)
		assert.Equal(t, []float64{-10, 0, 10}, decs)
	})

	t.Run("field straddling RA=0 finds stars on both sides", func(t *testing.T) {
		got := c.InField(0, 28.05, 1.0, 100, j2000)
		require.Len(t, got, 2, "the RA=0 seam is not a boundary on the sky")
	})

	t.Run("polar field works", func(t *testing.T) {
		got := c.InField(0, 89.9, 1.0, 100, j2000)
		require.Len(t, got, 1)
		assert.InDelta(t, 89.5, got[0].DecDeg, 1e-4)
	})

	t.Run("results are magnitude-ascending and the cap keeps the brightest", func(t *testing.T) {
		all := c.InField(90, 0, 100, 0, j2000) // 100° reaches every fixture star
		require.Len(t, all, len(rows))
		assert.True(t, sort.SliceIsSorted(all, func(i, j int) bool { return all[i].Mag < all[j].Mag }))
		capped := c.InField(90, 0, 100, 3, j2000)
		require.Len(t, capped, 3)
		assert.Equal(t, all[:3], capped, "a capped query is the prefix of the uncapped one")
	})
}

func TestCatalog_ProperMotion(t *testing.T) {
	// 1000 mas/yr = 1″/yr in declination: 26.5 years must move the star ≈ 26.5″.
	c := buildTestCatalog(t, []string{csvRow(map[string]string{
		"tyc": "1-1-1", "ra": "6.0", "dec": "0.0", "mag": "5.0", "pm_dec": "1000", "pm_ra": "0",
	})})
	at2000 := c.InField(90, 0, 0.5, 10, j2000)
	require.Len(t, at2000, 1)
	moved := c.InField(90, 0, 0.5, 10, testEpoch)
	require.Len(t, moved, 1)

	years := testEpoch.Sub(j2000).Hours() / 24 / 365.25
	assert.InDelta(t, years, (moved[0].DecDeg-at2000[0].DecDeg)*3600, 0.5)
}

func TestLoad_FallsBackToTheEmbeddedCatalogue(t *testing.T) {
	tests := []struct {
		name string
		path func(t *testing.T) string
	}{
		{"no path configured", func(*testing.T) string { return "" }},
		{"file missing", func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent.bin") }},
		{"file is not a catalogue", func(t *testing.T) string {
			p := filepath.Join(t.TempDir(), "junk.bin")
			require.NoError(t, os.WriteFile(p, []byte("this is not a star catalogue at all"), 0o644))
			return p
		}},
		{"header is a future version", func(t *testing.T) string {
			p := filepath.Join(t.TempDir(), "future.bin")
			h := header{count: 1, strOff: headerSize, recOff: headerSize}.marshal()
			h[8] = fileVersion + 1
			require.NoError(t, os.WriteFile(p, h, 0o644))
			return p
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, deep := Load(tt.path(t))
			defer c.Close()
			assert.False(t, deep, "a missing or unreadable deep catalogue is not an error")
			assert.Greater(t, c.Count(), 50_000, "the embedded catalogue answers instead")

			got := c.InField(83.0, 0.0, 10.0, 0, testEpoch)
			require.NotEmpty(t, got)
			names := map[string]bool{}
			for _, s := range got {
				names[s.Proper] = true
			}
			assert.True(t, names["Rigel"] && names["Betelgeuse"], "the fallback still names Orion")
		})
	}
}

func TestLoad_PrefersTheDeepCatalogue(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.csv.gz")
	writeSource(t, src, true, []string{csvRow(map[string]string{
		"tyc": "1-1-1", "ra": "6.0", "dec": "0.0", "mag": "5.0",
	})})
	out := filepath.Join(dir, "athyg.bin")
	require.NoError(t, buildSmall(t, src, out))

	c, deep := Load(out)
	defer c.Close()
	assert.True(t, deep)
	assert.Equal(t, 1, c.Count())
}

func TestPackTYC(t *testing.T) {
	tests := []struct {
		in   string
		want string // "" when the input is not a Tycho-2 designation
	}{
		{"1-1-1", "1-1-1"},
		{"9537-12121-3", "9537-12121-3"}, // the observed maximum in ATHYG v3.2
		{"4669-731-1", "4669-731-1"},
		{"3105-2070-1", "3105-2070-1"},
		{"", ""},
		{"not-a-tyc", ""},
		{"1-2", ""},
		{"1-2-3-4", ""},
		{"0-1-1", ""}, // region 0 does not exist, and 0 is the "absent" encoding
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.want, unpackTYC(packTYC(tt.in)))
		})
	}
}

func TestPackBayer(t *testing.T) {
	// Every token the real ATHYG file contains must survive the round trip; the builder hard-fails
	// on any that does not, so this is the list that keeps that promise honest.
	for _, tok := range greekTokens {
		assert.Equal(t, tok, unpackBayer(packBayer(tok)))
		for c := 1; c <= 9; c++ {
			withComp := fmt.Sprintf("%s-%d", tok, c)
			assert.Equal(t, withComp, unpackBayer(packBayer(withComp)))
		}
	}
	assert.Zero(t, packBayer(""))
	assert.Zero(t, packBayer("Zzz"), "an unknown token encodes as absent, and the builder rejects it")
	assert.Empty(t, unpackBayer(0))
}

func TestGreekTablesAgree(t *testing.T) {
	require.Len(t, greekLetters, len(greekTokens))
	require.Len(t, greekByToken, len(greekTokens), "the runtime map is derived from the same tables")
	for i, tok := range greekTokens {
		assert.Equal(t, greekLetters[i], greekByToken[tok])
	}
}

func TestEncodeRecord_Sentinels(t *testing.T) {
	// The three optional measurements must survive as ABSENT, not as zero: a star with B−V = 0 and a
	// star with no measured colour are different facts, and only one of them may be shown.
	var b [recordSize]byte
	encodeRecord(b[:], Star{RADeg: 1, DecDeg: 2, Mag: 5}, 0, 0, 0, 0, 0)
	got := decodeRecord(b[:], nil, nil, nil)
	assert.False(t, got.HasAbsMag)
	assert.False(t, got.HasCI)
	assert.False(t, got.HasRV)

	encodeRecord(b[:], Star{RADeg: 1, DecDeg: 2, Mag: 5, HasCI: true, CI: 0, HasRV: true, RVKmS: 0}, 0, 0, 0, 0, 0)
	got = decodeRecord(b[:], nil, nil, nil)
	assert.True(t, got.HasCI)
	assert.Zero(t, got.CI)
	assert.True(t, got.HasRV)
	assert.Zero(t, got.RVKmS)
}

func TestEncodeRecord_ImplausibleDistanceIsDropped(t *testing.T) {
	// A negative or near-zero parallax yields a "distance" of hundreds of kiloparsecs. That is a
	// measurement artefact, and a made-up number on the hover card is worse than a blank.
	var b [recordSize]byte
	encodeRecord(b[:], Star{RADeg: 1, DecDeg: 2, Mag: 5, DistPc: 196078}, 0, 0, 0, 0, 0)
	assert.Zero(t, decodeRecord(b[:], nil, nil, nil).DistPc)

	encodeRecord(b[:], Star{RADeg: 1, DecDeg: 2, Mag: 5, DistPc: 512.5}, 0, 0, 0, 0, 0)
	assert.InDelta(t, 512.5, decodeRecord(b[:], nil, nil, nil).DistPc, 1e-2)
}

func TestEncodeRecord_PositionPrecision(t *testing.T) {
	// Positions are stored in micro-degrees (0.0036″) — three orders of magnitude finer than the
	// 4 px match tolerance at any plate scale this app sees.
	var b [recordSize]byte
	const ra, dec = 279.234748, -38.784369
	encodeRecord(b[:], Star{RADeg: ra, DecDeg: dec, Mag: 1}, 0, 0, 0, 0, 0)
	got := decodeRecord(b[:], nil, nil, nil)
	assert.Less(t, math.Abs(got.RADeg-ra), 1e-6)
	assert.Less(t, math.Abs(got.DecDeg-dec), 1e-6)
}
