package pipeline

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fits/fitstest"
)

// The two-night geometry golden: a real (host-Siril) mini deep-sky run over a synthetic capture
// spanning two nights — the anchor night with all four channels and a second night, THREE YEARS
// apart in DATE-OBS, contributing only L/R at 3× the exposure with its field rotated ~35°. This is
// task #312 in miniature. Unlike the byte-pinned single-night golden, it asserts GEOMETRY and
// OUTCOME (same-canvas masters, a final image, measured rotation, seeded photometry): those must
// hold whatever Siril's noise does.
//
// Same gate as the single-night golden: ASTRO_GOLDEN_LIVE=1 + a host siril-cli.

// writeGoldenFrameRot is writeGoldenFrame for the second night: the SAME deterministic star field
// rendered rotated by rotDeg about the frame centre, at a chosen night and sky pedestal. It is a
// separate function on purpose — sharing the render loop with writeGoldenFrame would change its
// floating-point stream and break the byte-pinned single-night golden.
func writeGoldenFrameRot(t *testing.T, dir, name, imagetyp, filter string, expSec float64, seq int,
	rotDeg float64, night string, pedestal, spread float64) {
	t.Helper()
	const w, h = 256, 256
	require.NoError(t, os.MkdirAll(dir, 0o755))
	pix := make([]uint16, w*h)
	rnd := goldenLCG{s: uint64(1e6*expSec) + uint64(seq)<<32 + uint64(len(filter))*7 + 99}
	grid := make([]float64, w*h)
	for i := range grid {
		grid[i] = pedestal + spread*rnd.next()
	}
	switch imagetyp {
	case "Light":
		rad := rotDeg * math.Pi / 180
		cos, sin := math.Cos(rad), math.Sin(rad)
		dx, dy := float64(seq)*1.2, float64(seq)*-0.8
		stars := goldenLCG{s: 42} // the SAME sky as night A — only the framing rotated
		for s := 0; s < 40; s++ {
			bx := 20 + stars.next()*float64(w-40)
			by := 20 + stars.next()*float64(h-40)
			amp := 8000 + 24000*stars.next()
			cx := (bx-w/2)*cos - (by-h/2)*sin + w/2 + dx
			cy := (bx-w/2)*sin + (by-h/2)*cos + h/2 + dy
			for y := int(cy) - 6; y <= int(cy)+6; y++ {
				for x := int(cx) - 6; x <= int(cx)+6; x++ {
					if x < 0 || y < 0 || x >= w || y >= h {
						continue
					}
					d2 := (float64(x)-cx)*(float64(x)-cx) + (float64(y)-cy)*(float64(y)-cy)
					grid[y*w+x] += amp * math.Exp(-d2/(2*2.1*2.1))
				}
			}
		}
	case "Flat":
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				fx, fy := float64(x-w/2)/float64(w), float64(y-h/2)/float64(h)
				grid[y*w+x] = 30000 - 8000*(fx*fx+fy*fy)*4
			}
		}
	case "Dark":
		for i := range grid {
			grid[i] += 20
		}
	}
	for i, v := range grid {
		if v < 0 {
			v = 0
		}
		if v > 65535 {
			v = 65535
		}
		pix[i] = uint16(v)
	}
	cards := map[string]string{
		"OBJECT": "'TESTFIELD'", "IMAGETYP": "'" + imagetyp + "'",
		"EXPTIME": fmt.Sprintf("%g", expSec), "GAIN": "100", "OFFSET": "10",
		"CCD-TEMP": "-10.0", "XBINNING": "1", "YBINNING": "1",
		"DATE-OBS": fmt.Sprintf("'%sT22:%02d:%02d'", night, 10+seq, (seq*7)%60),
		"INSTRUME": "'ZWO ASI1600MM Pro'",
	}
	if filter != "" {
		cards["FILTER"] = "'" + filter + "'"
	}
	fitstest.WritePixels(t, dir, name+".fits", w, h, pix, cards)
}

// buildTwoNightCapture: night A = the single-night golden capture (4 channels, 10 s, 2024-03-01);
// night B = L/R only and SMALLER (2 subs — night A stays the photometric reference), 30 s, field
// rotated rotDeg, own flats + matching darks (2020-05-15).
func buildTwoNightCapture(t *testing.T, root string, rotDeg float64) {
	t.Helper()
	buildGoldenCapture(t, root)
	const nightB = "2020-05-15"
	for _, f := range []string{"L", "R"} {
		for i := 0; i < 2; i++ {
			writeGoldenFrameRot(t, filepath.Join(root, "lights_b"), fmt.Sprintf("light_b_%s_%d", f, i),
				"Light", f, 30, i, rotDeg, nightB, 520, 40)
		}
	}
	for i := 0; i < 3; i++ {
		writeGoldenFrameRot(t, filepath.Join(root, "darks_b"), fmt.Sprintf("dark_b_%d", i), "Dark", "", 30, i, 0, nightB, 500, 40)
		writeGoldenFrameRot(t, filepath.Join(root, "flats_b"), fmt.Sprintf("flat_b_%d", i), "Flat", "", 0.005, i, 0, nightB, 500, 40)
	}
}

func TestProcess_TwoNightAnchoredGeometry(t *testing.T) {
	runner := goldenRunner(t)
	root := t.TempDir()
	in, work, out := filepath.Join(root, "in"), filepath.Join(root, "work"), filepath.Join(root, "out")
	const rot = 35.0
	buildTwoNightCapture(t, in, rot)

	opts := Options{
		InputDir: in, WorkDir: work, OutputDir: out,
		Runner: runner, Preset: goldenPreset(),
	}
	res, err := Process(context.Background(), opts)
	require.NoError(t, err, "a two-night uneven-channel run must produce a final image, not a silent no-op")
	require.NotNil(t, res)
	require.NotNil(t, res.Final, "the combine must run to the end")

	assert.Equal(t, "2024-03-01", res.AnchorNight, "the 4-channel night anchors the run")
	for _, w := range res.Warnings {
		assert.NotContains(t, w, "could not be co-registered", "channel co-registration must succeed")
		assert.NotContains(t, w, "mixed dimensions")
	}

	// Every channel master shares the anchor canvas — the invariant whose violation killed task #312.
	byFilter := map[string]ChannelResult{}
	for _, ch := range res.Channels {
		require.Empty(t, ch.Err, "channel %s failed", ch.Filter)
		w, h := frameDims(ch.OutputPath)
		assert.Equal(t, 256, w, "master %s canvas width", ch.Filter)
		assert.Equal(t, 256, h, "master %s canvas height", ch.Filter)
		byFilter[ch.Filter] = ch
	}

	// The two-night L channel: both groups recorded, the 2020 night's rotation measured at the
	// planted angle with a big overlap, and its photometry measured honestly against the night-A
	// reference. (Both nights' dark-subtracted skies sit near the same level under one shared flat
	// shape, so the true affine is slope ≈ 1 — far from the 3× exposure prediction, exercising the
	// MetaDisagree flag with the measurement winning; the seeding physics itself is pinned by the
	// photom unit tests.)
	l := byFilter["L"]
	require.Len(t, l.Groups, 2, "L merges two nights")
	gb := l.Groups[0] // find the 2020 group whichever way the spans ordered them
	if gb.Session != "2020-05-15" {
		gb = l.Groups[1]
	}
	require.Equal(t, "2020-05-15", gb.Session)
	require.NotNil(t, gb.RotationDeg, "the rotated night's field rotation is measured")
	assert.InDelta(t, rot, math.Abs(*gb.RotationDeg), 3.0)
	require.NotNil(t, gb.OverlapFrac)
	assert.Greater(t, *gb.OverlapFrac, 0.5)
	require.NotNil(t, gb.Photom, "the 30 s night is measured against the 10 s reference")
	assert.False(t, gb.Photom.Ref, "the bigger night A group is the photometric reference")
	assert.InDelta(t, 1.0, gb.Photom.Scale, 0.15, "same-level fixtures measure slope ≈ 1")
	assert.True(t, gb.Photom.MetaDisagree, "measured ≈1 vs the 3× exposure prediction is flagged (measured wins)")
	// Whether the ~identity transform is physically APPLIED is an epsilon call (offset vs reference
	// noise) — the apply mechanics are pinned by the photom unit tests, not re-asserted here.

	// Single-night G/B channels ride the grouped path too (uniform canvas + provenance).
	g := byFilter["G"]
	require.Len(t, g.Groups, 1, "a lone group still records its provenance")
	assert.Equal(t, "2024-03-01", g.Groups[0].Session)
	assert.Nil(t, g.Groups[0].RotationDeg, "a lone group is trivially self-anchored — no telemetry noise")

	// The rotated night really contributes to the stack (not gated away as absurd).
	assert.GreaterOrEqual(t, l.StackedFrames, 4, "both nights' frames survive grading (%d stacked)", l.StackedFrames)

	// Per-night flats: night B's lights were flat-fielded with the 2020-05-15 flat, not night A's.
	assert.Equal(t, "run", gb.FlatSource)
	assert.Contains(t, gb.Flat, "n2020-05-15", "the night-stamped flat master was applied")

	// Background sanity on the anchor canvas: the stacked L master's median must sit near the
	// single-night G master's (same sky, same normalization target) — a skewed Siril addscale over
	// the rotated night's black borders would drag it far off.
	lMed := goldenMasterMedian(t, l.OutputPath)
	gMed := goldenMasterMedian(t, byFilter["G"].OutputPath)
	assert.InEpsilon(t, gMed, lMed, 0.25, "stacked background level stays sane (L %.5f vs G %.5f)", lMed, gMed)
}

// goldenMasterMedian reads a stacked master and returns its median pixel value.
func goldenMasterMedian(t *testing.T, path string) float64 {
	t.Helper()
	im, err := fits.ReadImage(path)
	require.NoError(t, err)
	require.NotEmpty(t, im.Pix)
	vals := make([]float64, 0, len(im.Pix[0]))
	for _, v := range im.Pix[0] {
		vals = append(vals, float64(v))
	}
	return medianOf(vals)
}

// Guard: the two-night fixtures must never leak into the byte-pinned single-night golden dir.
func TestTwoNightFixturesSeparateFromGolden(t *testing.T) {
	entries, err := os.ReadDir(goldenDir)
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.Contains(e.Name(), "two_night"), "unexpected file %s", e.Name())
	}
}
