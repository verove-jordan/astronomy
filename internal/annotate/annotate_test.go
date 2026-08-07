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
	"github.com/verove-jordan/astronomy/internal/postprocess"
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

// TestMapping_RoundTrip pins the inverses the 3D field map runs the projection chain backwards
// through to give an ANONYMOUS detection a line of sight. A sign slipped here would not fail
// loudly — it would quietly mirror the star cloud, exactly the class of bug that made the label
// overlay ship upside down for its whole life (see chooseRowFlip).
func TestMapping_RoundTrip(t *testing.T) {
	for _, tt := range []struct {
		name              string
		fileFlip, wcsFlip bool
	}{
		{"no flips", false, false},
		{"file flip", true, false},
		{"wcs flip", false, true},
		{"both flips", true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, err := newMapping(1000, 800, 900, 700, tt.fileFlip)
			require.NoError(t, err)
			m.wcsFlip = tt.wcsFlip

			for _, p := range [][2]float64{{0, 0}, {123.5, 456.25}, {899, 699}} {
				fx, fy := m.fromFinal(p[0], p[1])
				x, y, in := m.toFinal(fx, fy)
				assert.True(t, in, "a point inside the final image must map back inside the crop")
				assert.InDelta(t, p[0], x, 1e-9)
				assert.InDelta(t, p[1], y, 1e-9)

				wx, wy := m.fileToWcs(m.wcsToFile(fx, fy))
				assert.InDelta(t, fx, wx, 1e-9)
				assert.InDelta(t, fy, wy, 1e-9)
			}
		})
	}
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

// --- DSO extent ------------------------------------------------------------------------------------

// extentWCS builds a north-up/east-left TAN solution (the usual astronomical convention: CD1_1
// negative so RA increases leftwards) at the given plate scale, on a w×h master.
func extentWCS(t *testing.T, ra, dec, degPerPx float64, w, h int) fits.WCS {
	t.Helper()
	wcs, ok := fits.ParseWCS(hdrFromCards(t, wcsCards(ra, dec, degPerPx, w, h)))
	require.True(t, ok)
	return wcs
}

func TestExtentOf(t *testing.T) {
	const degPerPx = 1.0 / 3600 // 1″/px — 1 arcmin is exactly 60 px
	const ra, dec = 83.8, -5.4
	wcs := extentWCS(t, ra, dec, degPerPx, 2000, 2000)
	m, err := newMapping(2000, 2000, 2000, 2000, false)
	require.NoError(t, err)

	base := skycat.Record{Name: "X", RADeg: ra, DecDeg: dec}

	t.Run("no catalogued size means no outline", func(t *testing.T) {
		_, ok := extentOf(wcs, m, base)
		assert.False(t, ok)
	})

	t.Run("diameter alone yields a circle in pixels", func(t *testing.T) {
		rec := base
		rec.DiameterArcmin, rec.HasDiameter = 10, true // 10′ across → semi-axis 5′ → 300 px
		e, ok := extentOf(wcs, m, rec)
		require.True(t, ok)
		assert.InDelta(t, 300, e.RXpx, 1)
		assert.InDelta(t, 300, e.RYpx, 1, "no minor axis → circular")
	})

	t.Run("minor axis and position angle yield an oriented ellipse", func(t *testing.T) {
		rec := base
		rec.DiameterArcmin, rec.HasDiameter = 10, true
		rec.MinorAxisArcmin, rec.HasMinorAxis = 4, true // semi-minor 2′ → 120 px
		rec.PositionAngleDeg, rec.HasPositionAngle = 0, true
		e, ok := extentOf(wcs, m, rec)
		require.True(t, ok)
		assert.InDelta(t, 300, e.RXpx, 1)
		assert.InDelta(t, 120, e.RYpx, 1)
		// PA 0 is due North, which is the y axis in this north-up grid. An ellipse axis is a
		// direction without a sign, so assert the axis itself: no x component.
		assert.InDelta(t, 0, math.Cos(e.AngleRad), 1e-3, "major axis lies along y")
	})

	t.Run("a missing position angle falls back to a circle, never a guessed orientation", func(t *testing.T) {
		rec := base
		rec.DiameterArcmin, rec.HasDiameter = 10, true
		rec.MinorAxisArcmin, rec.HasMinorAxis = 4, true // known, but unusable without a PA
		e, ok := extentOf(wcs, m, rec)
		require.True(t, ok)
		assert.InDelta(t, e.RXpx, e.RYpx, 1e-9)
	})

	t.Run("position angle rotates the ellipse with the sky", func(t *testing.T) {
		rec := base
		rec.DiameterArcmin, rec.HasDiameter = 10, true
		rec.MinorAxisArcmin, rec.HasMinorAxis = 4, true
		rec.PositionAngleDeg, rec.HasPositionAngle = 90, true // major axis now East-West
		e, ok := extentOf(wcs, m, rec)
		require.True(t, ok)
		assert.InDelta(t, 300, e.RXpx, 1)
		assert.InDelta(t, 120, e.RYpx, 1)
		// A quarter turn on the sky is a quarter turn in the image: the axis now lies along x.
		assert.InDelta(t, 0, math.Sin(e.AngleRad), 1e-3, "major axis lies along x")
	})

	t.Run("the crop offset does not change the size", func(t *testing.T) {
		cropped, err := newMapping(2000, 2000, 1600, 1600, false)
		require.NoError(t, err)
		rec := base
		rec.DiameterArcmin, rec.HasDiameter = 10, true
		full, _ := extentOf(wcs, m, rec)
		crop, ok := extentOf(wcs, cropped, rec)
		require.True(t, ok)
		assert.InDelta(t, full.RXpx, crop.RXpx, 1e-9, "a center crop translates, it does not rescale")
	})

	t.Run("a row-order flip mirrors the angle but keeps the axes", func(t *testing.T) {
		flipped := m
		flipped.fileFlip = true
		rec := base
		rec.DiameterArcmin, rec.HasDiameter = 10, true
		rec.MinorAxisArcmin, rec.HasMinorAxis = 4, true
		rec.PositionAngleDeg, rec.HasPositionAngle = 30, true
		up, _ := extentOf(wcs, m, rec)
		down, ok := extentOf(wcs, flipped, rec)
		require.True(t, ok)
		assert.InDelta(t, up.RXpx, down.RXpx, 1e-6)
		assert.InDelta(t, up.RYpx, down.RYpx, 1e-6)
		assert.InDelta(t, -up.AngleRad, down.AngleRad, 1e-6, "flipping rows mirrors the orientation")
	})
}

// --- master → PNG row order --------------------------------------------------------------------

// starPNG renders a star field into a PNG the way a finish would deliver it: the master's peaks,
// centre-cropped, optionally mirrored top-to-bottom.
func starPNG(t *testing.T, path string, w, h int, pts [][2]int, mirror bool) {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 8 // faint background so the detector has a noise floor to beat
	}
	for _, p := range pts {
		x, y := p[0], p[1]
		if mirror {
			y = h - 1 - y
		}
		for dy := -2; dy <= 2; dy++ {
			for dx := -2; dx <= 2; dx++ {
				px, py := x+dx, y+dy
				if px < 0 || py < 0 || px >= w || py >= h {
					continue
				}
				v := 255 - 40*(abs(dx)+abs(dy))
				if v > int(img.Pix[py*w+px]) {
					img.Pix[py*w+px] = uint8(v)
				}
			}
		}
	}
	f, err := os.Create(path)
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, png.Encode(f, img))
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func TestChooseRowFlip(t *testing.T) {
	// A master whose peaks sit at known places, and a final PNG of the same size (no crop) rendered
	// either the same way up or mirrored.
	const w, h = 300, 240
	im := fits.NewImage(w, h, 1)
	for i := range im.Pix[0] {
		im.Pix[0][i] = 0.02
	}
	var pts [][2]int
	for _, p := range [][2]int{{40, 30}, {120, 60}, {200, 45}, {80, 150}, {250, 190}, {160, 200},
		{60, 90}, {230, 110}, {100, 20}, {270, 70}, {30, 200}, {190, 130}, {140, 95}, {210, 165}} {
		paintStar(im, p[0], p[1], 0.9)
		pts = append(pts, p)
	}
	peaks := postprocess.DetectStarPeaks(im, countDetect)
	require.GreaterOrEqual(t, len(peaks), 12, "fixture must give the probe enough stars")

	run := func(mirror bool, cardSaysFlip bool) (bool, int, int, bool) {
		dir := t.TempDir()
		p := filepath.Join(dir, "final.png")
		starPNG(t, p, w, h, pts, mirror)
		m, err := newMapping(w, h, w, h, cardSaysFlip)
		require.NoError(t, err)
		return chooseRowFlip(m, peaks, p)
	}

	t.Run("agrees with a correct ROWORDER card", func(t *testing.T) {
		flip, matched, _, ok := run(false, false)
		require.True(t, ok, "an unambiguous field must be decidable")
		assert.False(t, flip)
		assert.Greater(t, matched, 10)
	})

	t.Run("OVERRIDES a card that disagrees with the delivered pixels", func(t *testing.T) {
		// The regression this exists for: the master claimed TOP-DOWN, the PNG was mirrored, and
		// every label landed reflected because the card was trusted blindly.
		flip, matched, _, ok := run(true, false)
		require.True(t, ok)
		assert.True(t, flip, "the pixels say mirrored, so the card must lose")
		assert.Greater(t, matched, 10)
	})

	t.Run("also corrects the card in the other direction", func(t *testing.T) {
		flip, _, _, ok := run(false, true)
		require.True(t, ok)
		assert.False(t, flip)
	})

	t.Run("falls back to the card when the final image cannot decide", func(t *testing.T) {
		dir := t.TempDir()
		p := filepath.Join(dir, "final.png")
		starPNG(t, p, w, h, nil, false) // featureless: no evidence either way
		m, err := newMapping(w, h, w, h, true)
		require.NoError(t, err)
		flip, _, _, ok := chooseRowFlip(m, peaks, p)
		assert.False(t, ok, "no stars in the final image is not a verdict")
		assert.True(t, flip, "and the card's answer is kept")
	})

	t.Run("a missing final image is not fatal", func(t *testing.T) {
		m, err := newMapping(w, h, w, h, false)
		require.NoError(t, err)
		_, _, _, ok := chooseRowFlip(m, peaks, filepath.Join(t.TempDir(), "nope.png"))
		assert.False(t, ok)
	})
}
