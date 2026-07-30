package annotate

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/deepstars"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/skycat"
)

// --- synthetic FITS/PNG helpers -------------------------------------------------------------------

// writeFITSWithCards hand-crafts a float32 FITS (planar RGB) with arbitrary extra header cards —
// the package's own WriteFITS deliberately emits a minimal header, and this test needs WCS cards.
func writeFITSWithCards(t *testing.T, path string, im *fits.Image, extra map[string]string) {
	t.Helper()
	var cards []string
	add := func(k, v string) { cards = append(cards, fmt.Sprintf("%-8s= %s", k, v)) }
	add("SIMPLE", "T")
	add("BITPIX", "-32")
	if im.C == 3 {
		add("NAXIS", "3")
	} else {
		add("NAXIS", "2")
	}
	add("NAXIS1", fmt.Sprintf("%d", im.W))
	add("NAXIS2", fmt.Sprintf("%d", im.H))
	if im.C == 3 {
		add("NAXIS3", "3")
	}
	add("BZERO", "0.")
	add("BSCALE", "1.")
	for k, v := range extra {
		add(k, v)
	}
	cards = append(cards, "END")

	var b strings.Builder
	for _, c := range cards {
		require.LessOrEqual(t, len(c), 80, "card too long: %q", c)
		b.WriteString(c + strings.Repeat(" ", 80-len(c)))
	}
	for b.Len()%2880 != 0 {
		b.WriteString(" ")
	}
	data := make([]byte, 0, im.W*im.H*im.C*4)
	buf := make([]byte, 4)
	for c := 0; c < im.C; c++ {
		for _, v := range im.Pix[c] {
			bits := math.Float32bits(v)
			buf[0], buf[1], buf[2], buf[3] = byte(bits>>24), byte(bits>>16), byte(bits>>8), byte(bits)
			data = append(data, buf...)
		}
	}
	for len(data)%2880 != 0 {
		data = append(data, 0)
	}
	require.NoError(t, os.WriteFile(path, append([]byte(b.String()), data...), 0o644))
}

func writePNG(t *testing.T, path string, w, h int) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, png.Encode(f, image.NewGray(image.Rect(0, 0, w, h))))
}

// paintStar adds a gaussian at (x,y) on all channels.
func paintStar(im *fits.Image, x, y int, amp float32) {
	const sigma = 1.6
	for dy := -6; dy <= 6; dy++ {
		for dx := -6; dx <= 6; dx++ {
			px, py := x+dx, y+dy
			if px < 0 || px >= im.W || py < 0 || py >= im.H {
				continue
			}
			g := float32(math.Exp(-float64(dx*dx+dy*dy) / (2 * sigma * sigma)))
			for c := 0; c < im.C; c++ {
				im.Pix[c][py*im.W+px] += amp * g
			}
		}
	}
}

// wcsCards builds a simple East-left TAN solution centered on (ra0,dec0).
func wcsCards(ra0, dec0, degPerPx float64, w, h int) map[string]string {
	return map[string]string{
		"CTYPE1": "'RA---TAN'",
		"CTYPE2": "'DEC--TAN'",
		"CRVAL1": fmt.Sprintf("%.6f", ra0),
		"CRVAL2": fmt.Sprintf("%.6f", dec0),
		"CRPIX1": fmt.Sprintf("%.1f", float64(w)/2+0.5),
		"CRPIX2": fmt.Sprintf("%.1f", float64(h)/2+0.5),
		"CD1_1":  fmt.Sprintf("%.9f", -degPerPx),
		"CD1_2":  "0.",
		"CD2_1":  "0.",
		"CD2_2":  fmt.Sprintf("%.9f", degPerPx),
	}
}

// synthField paints every catalogue star of the field onto an RGB image at its projected position
// (optionally y-flipped) and returns the painted file-grid positions.
func synthField(t *testing.T, im *fits.Image, w fits.WCS, flipY bool, epoch time.Time) [][2]int {
	t.Helper()
	ra, dec := w.PixToSky(float64(im.W)/2, float64(im.H)/2)
	radius := w.ScaleArcsecPerPix() * math.Hypot(float64(im.W), float64(im.H)) / 2 / 3600
	var painted [][2]int
	for _, s := range deepstars.InField(ra, dec, radius, 0, epoch) {
		x, y, ok := w.SkyToPix(s.RADeg, s.DecDeg)
		if !ok {
			continue
		}
		if flipY {
			y = float64(im.H-1) - y
		}
		xi, yi := int(math.Round(x)), int(math.Round(y))
		if xi < 10 || xi >= im.W-10 || yi < 10 || yi >= im.H-10 {
			continue // fully inside so the gaussian is complete
		}
		tooClose := false
		for _, p := range painted {
			if dx, dy := p[0]-xi, p[1]-yi; dx*dx+dy*dy < 100 {
				tooClose = true // unresolvable pair (e.g. Trapezium) → one physical star
				break
			}
		}
		if tooClose {
			continue
		}
		paintStar(im, xi, yi, 0.5)
		painted = append(painted, [2]int{xi, yi})
	}
	return painted
}

func flatRGB(w, h int, back float32) *fits.Image {
	im := fits.NewImage(w, h, 3)
	for c := 0; c < 3; c++ {
		for i := range im.Pix[c] {
			im.Pix[c][i] = back
		}
	}
	return im
}

var testEpoch = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

// --- unit tests ------------------------------------------------------------------------------------

func TestNewMapping(t *testing.T) {
	tests := []struct {
		name         string
		w, h, wf, hf int
		cx, cy       int
		wantErr      bool
	}{
		{"typical finish crop", 4000, 3000, 3720, 2790, 140, 105, false},
		{"no crop", 2000, 1500, 2000, 1500, 0, 0, false},
		{"final larger than master", 2000, 1500, 2010, 1500, 0, 0, true},
		{"odd delta is not symmetric", 2000, 1500, 1999, 1500, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := newMapping(tt.w, tt.h, tt.wf, tt.hf, false)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.cx, m.cx)
			assert.Equal(t, tt.cy, m.cy)
		})
	}
}

func TestMapping_ToFinalAndWindow(t *testing.T) {
	m, err := newMapping(1000, 800, 900, 700, false)
	require.NoError(t, err)

	x, y, in := m.toFinal(50, 50)
	assert.True(t, in)
	assert.Equal(t, 0.0, x)
	assert.Equal(t, 0.0, y)
	_, _, in = m.toFinal(49, 50)
	assert.False(t, in, "left of the crop window")
	assert.True(t, m.inWindow(50, 50))
	assert.False(t, m.inWindow(950, 50), "right of the crop window")

	// File-flip (BOTTOM-UP master): file y 50 is visual y H-1-50.
	m.fileFlip = true
	_, y, in = m.toFinal(50, 50)
	assert.True(t, in)
	assert.Equal(t, float64(800-1-50-50), y)
	assert.True(t, m.inWindow(50, 50), "the centered window is flip-invariant")
}

// --- end-to-end ------------------------------------------------------------------------------------

// setupRun builds a run dir with a painted master (M42 field) + final PNG. Returns the dir, the
// WCS used and the painted file-grid positions.
func setupRun(t *testing.T, wcsOnMaster, flipY bool, crop int) (string, fits.WCS, [][2]int) {
	t.Helper()
	const W, H = 1600, 1200
	const degPerPx = 0.0012 // ≈4.3″/px → 1.9°×1.4° field around M42
	cards := wcsCards(83.822, -5.391, degPerPx, W, H)
	w, ok := fits.ParseWCS(hdrFromCards(t, cards))
	require.True(t, ok)

	im := flatRGB(W, H, 0.05)
	painted := synthField(t, im, w, flipY, testEpoch)
	require.GreaterOrEqual(t, len(painted), 10, "the M42 field must paint enough catalogue stars")

	dir := t.TempDir()
	extra := map[string]string{"ROWORDER": "'TOP-DOWN'"}
	if wcsOnMaster {
		for k, v := range cards {
			extra[k] = v
		}
	}
	writeFITSWithCards(t, filepath.Join(dir, "rgb_base.fits"), im, extra)
	writePNG(t, filepath.Join(dir, "final.png"), W-2*crop, H-2*crop)
	return dir, w, painted
}

// hdrFromCards round-trips card values through a throwaway FITS so the test uses the same parser
// as production (fits.Header is not constructible cross-package).
func hdrFromCards(t *testing.T, cards map[string]string) *fits.Header {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "probe.fits")
	writeFITSWithCards(t, path, fits.NewImage(2, 2, 1), cards)
	f, err := fits.Open(path)
	require.NoError(t, err)
	return f.Header
}

func TestRun_SolvedFieldEndToEnd(t *testing.T) {
	const crop = 40
	dir, w, painted := setupRun(t, true, false, crop)

	res, err := Run(context.Background(), Options{RunDir: dir, Mode: "deepsky", Now: func() time.Time { return testEpoch }})
	require.NoError(t, err)

	inWindow := 0
	for _, p := range painted {
		if p[0] >= crop && p[0] < 1600-crop && p[1] >= crop && p[1] < 1200-crop {
			inWindow++
		}
	}
	assert.Equal(t, inWindow, res.Count, "count = painted stars inside the crop window")
	require.True(t, res.Solved, "reason=%s matched=%d/%d", res.Solve.Reason, res.Solve.Matched, res.Solve.Tried)
	assert.Equal(t, "pipeline", res.Solve.Method)
	assert.False(t, res.Solve.Flip)
	assert.GreaterOrEqual(t, res.Solve.Matched, 5)

	// Every star label must sit on a painted star (crop-shifted), data-driven.
	starLabels, dsoNames := 0, map[string]Label{}
	for _, l := range res.Labels {
		switch l.Kind {
		case "star":
			starLabels++
			onPainted := false
			for _, p := range painted {
				if math.Hypot(l.X-(float64(p[0])-crop), l.Y-(float64(p[1])-crop)) <= 1.5 {
					onPainted = true
					break
				}
			}
			assert.True(t, onPainted, "star label %q at (%.1f,%.1f) is not on a painted star", l.Name, l.X, l.Y)
			assert.NotEmpty(t, l.Name)
		case "dso":
			dsoNames[l.Name] = l
		}
	}
	assert.GreaterOrEqual(t, starLabels, 3)

	// The field's headline DSO must be labeled at its projected position.
	m42, ok := dsoNames["M42"]
	require.True(t, ok, "M42 must be labeled (got DSOs: %v)", keys(dsoNames))
	ra, dec, okc := skycat.ResolveCoords("M42", "")
	require.True(t, okc)
	px, py, okp := w.SkyToPix(ra, dec)
	require.True(t, okp)
	assert.InDelta(t, px-crop, m42.X, 1.0)
	assert.InDelta(t, py-crop, m42.Y, 1.0)

	// Persisted + loadable.
	loaded, ok := Load(dir)
	require.True(t, ok)
	assert.Equal(t, res.Count, loaded.Count)
	assert.Equal(t, len(res.Labels), len(loaded.Labels))
}

func TestRun_YFlippedWCSIsDetectedAndCorrected(t *testing.T) {
	// Stars painted at flipped y: the WCS no longer matches file rows directly — the empirical
	// validation must choose flip=true and labels must still land on the painted stars.
	dir, _, painted := setupRun(t, true, true, 0)

	res, err := Run(context.Background(), Options{RunDir: dir, Mode: "deepsky", Now: func() time.Time { return testEpoch }})
	require.NoError(t, err)
	require.True(t, res.Solved, "reason=%s matched=%d/%d", res.Solve.Reason, res.Solve.Matched, res.Solve.Tried)
	assert.True(t, res.Solve.Flip)

	for _, l := range res.Labels {
		if l.Kind != "star" {
			continue
		}
		onPainted := false
		for _, p := range painted {
			if math.Hypot(l.X-float64(p[0]), l.Y-float64(p[1])) <= 1.5 {
				onPainted = true
				break
			}
		}
		assert.True(t, onPainted, "star label %q not on a painted star after flip correction", l.Name)
	}
}

func TestRun_NoWCSNoRunnerIsCountOnly(t *testing.T) {
	dir, _, painted := setupRun(t, false, false, 0)

	res, err := Run(context.Background(), Options{RunDir: dir, Mode: "deepsky", Now: func() time.Time { return testEpoch }})
	require.NoError(t, err)
	assert.False(t, res.Solved)
	assert.Equal(t, "none", res.Solve.Method)
	assert.Equal(t, reasonNoWCS, res.Solve.Reason)
	assert.Equal(t, len(painted), res.Count)
	assert.Empty(t, res.Labels)

	loaded, ok := Load(dir)
	require.True(t, ok)
	assert.False(t, loaded.Solved)
}

func TestRun_PlanetaryUnsupported(t *testing.T) {
	_, err := Run(context.Background(), Options{RunDir: t.TempDir(), Mode: "planetary"})
	require.ErrorIs(t, err, ErrUnsupportedMode)
}

func TestRun_MissingArtifacts(t *testing.T) {
	dir := t.TempDir()
	_, err := Run(context.Background(), Options{RunDir: dir, Mode: "deepsky"})
	require.ErrorIs(t, err, ErrNoMaster)

	writeFITSWithCards(t, filepath.Join(dir, "rgb_base.fits"), flatRGB(64, 64, 0.05), nil)
	_, err = Run(context.Background(), Options{RunDir: dir, Mode: "deepsky"})
	require.ErrorIs(t, err, ErrNoFinal)
}

func keys(m map[string]Label) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestFlipProbeThreshold pins the sparse-field validation bar: 40% of probes in [3,5] — a 7-star
// galaxy field validates at 3 matches (with 2× dominance), dense fields keep the historical 5.
func TestFlipProbeThreshold(t *testing.T) {
	tests := []struct {
		n    int
		want int
	}{
		{0, 0}, {2, 0}, // unverifiable
		{3, 3}, {5, 3}, {7, 3},
		{10, 4},
		{13, 5}, {30, 5},
	}
	for _, tt := range tests {
		if got := flipProbeThreshold(tt.n); got != tt.want {
			t.Fatalf("flipProbeThreshold(%d) = %d, want %d", tt.n, got, tt.want)
		}
	}
}
