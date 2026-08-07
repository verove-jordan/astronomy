package scene3d

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/annotate"
)

// --- binary format ---------------------------------------------------------------------------------

func TestFormat_RoundTrip(t *testing.T) {
	in := []star{
		{
			dir: vec3{0, 0, 1}, distPc: 412.3, r: 200, g: 210, b: 255,
			absMag: -3.25, hasAbsMag: true, mag: 6.5, hasMag: true,
			source: DepthMeasured, flags: flagIdentified, nameIdx: 1,
		},
		{
			dir: vec3{0.01, -0.02, 0.9997}, distPc: 1650, r: 255, g: 180, b: 120,
			source: DepthEstimated,
		},
		{
			dir: vec3{-0.005, 0.004, 0.99998}, distPc: 136, r: 255, g: 255, b: 255,
			absMag: 12.25, hasAbsMag: true, mag: 13.875, hasMag: true,
			source: DepthMeasured, flags: flagIdentified | flagClusterMember, nameIdx: 2,
		},
	}
	names := []string{"Alnitak", "TYC 4669-731-1"}

	var buf bytes.Buffer
	require.NoError(t, writeBin(&buf, in, names))
	// header + fixed-width records + the name table's own framing (a uint32 count, then a uint16
	// length per entry). Pinned so a layout change has to be deliberate.
	wantLen := headerSize + len(in)*recordSize + 4
	for _, n := range names {
		wantLen += 2 + len(n)
	}
	assert.Equal(t, wantLen, buf.Len())

	got, gotNames, err := decodeBin(buf.Bytes())
	require.NoError(t, err)
	require.Len(t, got, len(in))
	assert.Equal(t, names, gotNames)

	for i, want := range in {
		g := got[i]
		assert.InDelta(t, want.dir.X, g.dir.X, 1e-6, "star %d dirX", i)
		assert.InDelta(t, want.dir.Y, g.dir.Y, 1e-6, "star %d dirY", i)
		assert.InDelta(t, want.dir.Z, g.dir.Z, 1e-6, "star %d dirZ", i)
		assert.InDelta(t, want.distPc, g.distPc, want.distPc*1e-6, "star %d distance", i)
		assert.Equal(t, [3]uint8{want.r, want.g, want.b}, [3]uint8{g.r, g.g, g.b}, "star %d colour", i)
		assert.Equal(t, want.source, g.source, "star %d depth source", i)
		assert.Equal(t, want.nameIdx, g.nameIdx, "star %d name index", i)
		assert.Equal(t, want.flags&flagIdentified != 0, g.flags&flagIdentified != 0, "star %d identified", i)
		assert.Equal(t, want.flags&flagClusterMember != 0, g.flags&flagClusterMember != 0, "star %d member", i)

		require.Equal(t, want.hasAbsMag, g.hasAbsMag, "star %d absmag presence", i)
		if want.hasAbsMag { // quantised to quarter magnitudes, and the fixtures sit on the grid
			assert.InDelta(t, want.absMag, g.absMag, 1e-9, "star %d absmag", i)
		}
		require.Equal(t, want.hasMag, g.hasMag, "star %d mag presence", i)
		if want.hasMag { // quantised to eighths
			assert.InDelta(t, want.mag, g.mag, 1e-9, "star %d mag", i)
		}
	}
}

// TestFormat_QuantisationSentinelsCannotCollide pins the one hazard of packing magnitudes into a
// byte: a real value must never land on the "absent" code, or a bright star silently becomes an
// unknown one.
func TestFormat_QuantisationSentinelsCannotCollide(t *testing.T) {
	for _, absMag := range []float64{-40, -31.75, -31.8, 0, 31.75, 40} {
		var buf bytes.Buffer
		require.NoError(t, writeBin(&buf, []star{{dir: vec3{0, 0, 1}, absMag: absMag, hasAbsMag: true}}, nil))
		got, _, err := decodeBin(buf.Bytes())
		require.NoError(t, err)
		assert.True(t, got[0].hasAbsMag, "absolute magnitude %v must survive as a real value", absMag)
	}
	for _, mag := range []float64{-10, -5, 0, 26, 40} {
		var buf bytes.Buffer
		require.NoError(t, writeBin(&buf, []star{{dir: vec3{0, 0, 1}, mag: mag, hasMag: true}}, nil))
		got, _, err := decodeBin(buf.Bytes())
		require.NoError(t, err)
		assert.True(t, got[0].hasMag, "apparent magnitude %v must survive as a real value", mag)
	}
}

func TestFormat_RefusesWhatItCannotRead(t *testing.T) {
	var good bytes.Buffer
	require.NoError(t, writeBin(&good, []star{{dir: vec3{0, 0, 1}, distPc: 10}}, []string{"x"}))

	tests := []struct {
		name   string
		mangle func([]byte) []byte
	}{
		{"empty", func([]byte) []byte { return nil }},
		{"wrong magic", func(b []byte) []byte { c := clone(b); copy(c, "NOTASCENE"); return c }},
		{"future version", func(b []byte) []byte { c := clone(b); c[8] = 99; return c }},
		// 24 is the v1 record size: a scene cached before the format grew must be refused, not read
		// with the new offsets against the old bytes.
		{"a v1-sized record", func(b []byte) []byte { c := clone(b); c[10] = 24; return c }},
		{"truncated", func(b []byte) []byte { return clone(b)[:headerSize+4] }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := decodeBin(tt.mangle(good.Bytes()))
			require.Error(t, err, "a reader must refuse the file rather than misread it")
		})
	}
}

func clone(b []byte) []byte { return append([]byte(nil), b...) }

// --- end to end ------------------------------------------------------------------------------------

// syntheticRun builds an annotated run the way the engine would have: a camera, stars laid on real
// image pixels with real distances, and the labels for the objects in the field.
func syntheticRun(t *testing.T, cam testCamera) *annotate.Result {
	t.Helper()
	f := cam.frame()
	res := &annotate.Result{
		Version: 1,
		Image:   annotate.Dims{Width: cam.wf, Height: cam.hf},
		Solved:  true,
		Solve:   annotate.Solve{Method: "cached", MagZeroPoint: 20, Frame: &f},
	}

	rng := rand.New(rand.NewSource(101))
	// Pixel positions are integers, as annotate ships them, and the sky position is derived from that
	// same integer — so the fixture carries no sub-pixel disagreement of its own and the geometry
	// assertions can stay tight.
	place := func(px, py int, distPc, ci float64, identified bool) {
		absMag, ok := zamsAbsMag(ci)
		if !ok {
			return
		}
		const gain, offset = 0.72, 0.31
		h := (ci - offset) / gain
		red := math.Min(1, math.Pow(10, h/2.5)*0.35)
		ra, dec := cam.starAt(float64(px), float64(py))
		p := annotate.Point{
			X: px, Y: py, Rpx: 2, RADeg: ra, DecDeg: dec,
			Mag: absMag + 5*math.Log10(distPc) - 5,
			Hex: renderHex(red, (red+0.35)/2, 0.35),
		}
		if identified {
			ciCopy, absCopy := ci, absMag
			p.Star = &annotate.StarInfo{Name: "TYC 1-1-1", DistPc: distPc, CI: &ciCopy, AbsMag: &absCopy}
		}
		res.Stars = append(res.Stars, p)
	}

	// A cluster inside its own footprint, plus a field spread over three decades.
	for i := 0; i < 90; i++ {
		place(1500+rng.Intn(600), 1000+rng.Intn(500), 136*(1+rng.NormFloat64()*0.03),
			-0.1+rng.Float64()*1.6, true)
	}
	for i := 0; i < 300; i++ {
		place(rng.Intn(cam.wf), rng.Intn(cam.hf),
			math.Pow(10, 1.5+rng.Float64()*3), -0.1+rng.Float64()*1.6, i%2 == 0)
	}

	res.Labels = []annotate.Label{
		{
			Name: "M45", Kind: "dso", Type: "open_cluster", X: 1800, Y: 1250, Mag: 1.6,
			Extent: &annotate.Extent{RXpx: 400, RYpx: 350},
		},
		{
			Name: "M42", Kind: "dso", Type: "emission_nebula", X: 900, Y: 700, Mag: 4,
			Extent: &annotate.Extent{RXpx: 300, RYpx: 220},
		},
		{
			Name: "NGC9999999", Kind: "dso", Type: "galaxy", X: 300, Y: 300, Mag: 12,
			Extent: &annotate.Extent{RXpx: 40, RYpx: 20},
		},
	}
	return res
}

// writeFinalPNG puts a final image in the run dir so the backdrop has something to cut from.
func writeFinalPNG(t *testing.T, dir string, w, h int) {
	t.Helper()
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			im.SetRGBA(x, y, color.RGBA{uint8(x % 251), uint8(y % 241), 40, 255})
		}
	}
	f, err := os.Create(filepath.Join(dir, "final.png"))
	require.NoError(t, err)
	defer f.Close()
	require.NoError(t, png.Encode(f, im))
}

func TestBuild_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	cam := rotatedCamera(56.75, 24.12, 17, false, 2400, 1800)
	writeFinalPNG(t, dir, cam.wf, cam.hf)
	res := syntheticRun(t, cam)

	m, err := Build(res, Options{RunDir: dir, Now: func() time.Time { return time.Unix(0, 0).UTC() }})
	require.NoError(t, err)
	require.True(t, m.Available, "reason: %s", m.Reason)

	t.Run("the manifest describes the camera that took the picture", func(t *testing.T) {
		assert.InDelta(t, cam.tanHalfW, m.Camera.TanHalfW, 1e-9)
		assert.InDelta(t, cam.tanHalfH, m.Camera.TanHalfH, 1e-9)
		assert.True(t, m.Camera.RightHanded)
		assert.Equal(t, Dims{Width: cam.wf, Height: cam.hf}, m.Image)
	})

	t.Run("every plotted star is accounted for", func(t *testing.T) {
		assert.Equal(t, len(res.Stars), m.Stars.Plotted)
		assert.Equal(t, m.Stars.Plotted, m.Stars.Placed+m.Stars.Unknown)
		assert.Equal(t, m.Stars.Placed, m.Stars.Measured+m.Stars.Estimated)
		assert.Greater(t, m.Stars.Measured, 0)
		assert.Greater(t, m.Stars.Estimated, 0)
	})

	t.Run("the depth range covers every object, not just the stars", func(t *testing.T) {
		// The bug this pins: the warp clamps anything outside [near, far] onto an end plane, so a
		// range taken from the stars alone drew a 7 Mpc galaxy on the same plane as a 600 pc star.
		for _, b := range m.Billboards {
			assert.GreaterOrEqual(t, b.DistPc, m.Depth.NearPc,
				"%s falls in front of the drawing range", b.Name)
			assert.LessOrEqual(t, b.DistPc, m.Depth.FarPc,
				"%s falls beyond the drawing range and would be clamped onto the far plane", b.Name)
		}
	})

	t.Run("the depth range brackets the field without chasing its extremes", func(t *testing.T) {
		assert.Greater(t, m.Depth.NearPc, 0.0)
		assert.Less(t, m.Depth.NearPc, m.Depth.MedianPc)
		assert.Less(t, m.Depth.MedianPc, m.Depth.FarPc)
		// Min/Max describe the stars; Near/Far are the drawing range, which may reach past them to
		// take in an object.
		assert.LessOrEqual(t, m.Depth.MinPc, m.Depth.NearPc)
		assert.LessOrEqual(t, m.Depth.FarPc, math.Max(m.Depth.MaxPc, farthestObject(m)))
	})

	t.Run("the cluster distance is measured, the nebula's looked up", func(t *testing.T) {
		byName := map[string]Billboard{}
		for _, b := range m.Billboards {
			byName[b.Name] = b
		}
		require.Contains(t, byName, "M45")
		require.Contains(t, byName, "M42")
		assert.NotContains(t, byName, "NGC9999999", "an object with no known distance gets no billboard")

		m45 := byName["M45"]
		assert.Equal(t, "measured", m45.DistSource)
		assert.InDelta(t, 136, m45.DistPc, 136*0.1, "measured from the frame's own member stars")
		assert.Greater(t, m45.Members, minClusterStars)
		assert.InDelta(t, 136, m45.TableDistPc, 5, "and the catalogued value is kept beside it")

		m42 := byName["M42"]
		assert.Equal(t, "table", m42.DistSource)
		assert.InDelta(t, 412, m42.DistPc, 10)

		// The footprint is carried through verbatim: it is what places the quad, so anything else here
		// would hang the object off the pixels it is cut from.
		assert.Equal(t, res.Labels[1].X, m42.X)
		assert.Equal(t, res.Labels[1].Y, m42.Y)
		assert.Equal(t, res.Labels[1].Extent.RXpx, m42.RXpx)
		assert.Equal(t, res.Labels[1].Extent.RYpx, m42.RYpx)
	})

	t.Run("the artifacts are written and readable", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(dir, m.Points))
		require.NoError(t, err)
		stars, _, err := decodeBin(raw)
		require.NoError(t, err)
		assert.Len(t, stars, m.Stars.Placed)

		bg, err := os.Open(filepath.Join(dir, m.Backdrop))
		require.NoError(t, err)
		defer bg.Close()
		cfg, err := png.DecodeConfig(bg)
		require.NoError(t, err)
		assert.Equal(t, backdropMaxEdge, cfg.Width, "the long edge is capped")
		assert.InDelta(t, float64(cam.wf)/float64(cam.hf), float64(cfg.Width)/float64(cfg.Height), 0.01,
			"and the aspect ratio survives, or every billboard would be stretched")

		var onDisk Manifest
		b, err := os.ReadFile(filepath.Join(dir, manifestFileName))
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(b, &onDisk))
		assert.Equal(t, m.Stars, onDisk.Stars)

		loaded, ok := Load(dir)
		require.True(t, ok)
		assert.True(t, loaded.Available)
	})

	t.Run("cluster members are flagged in the binary", func(t *testing.T) {
		raw, err := os.ReadFile(filepath.Join(dir, m.Points))
		require.NoError(t, err)
		stars, _, err := decodeBin(raw)
		require.NoError(t, err)
		var members int
		for _, s := range stars {
			if s.flags&flagClusterMember != 0 {
				members++
				assert.InDelta(t, 136, s.distPc, 136*0.25)
			}
		}
		assert.Greater(t, members, minClusterStars)
	})
}

// TestBuild_ReproducesThePhotograph closes the loop through the real pipeline: take the binary the
// engine actually wrote, project each star the way the vertex shader will at depth zero, and land
// back on the pixel it was detected at.
func TestBuild_ReproducesThePhotograph(t *testing.T) {
	dir := t.TempDir()
	cam := rotatedCamera(56.75, 24.12, 17, false, 2400, 1800)
	writeFinalPNG(t, dir, cam.wf, cam.hf)
	res := syntheticRun(t, cam)

	m, err := Build(res, Options{RunDir: dir})
	require.NoError(t, err)
	require.True(t, m.Available)

	raw, err := os.ReadFile(filepath.Join(dir, m.Points))
	require.NoError(t, err)
	stars, _, err := decodeBin(raw)
	require.NoError(t, err)

	// Only the placed stars are in the binary, so walk the source list in the same order.
	var placed []annotate.Point
	for _, p := range res.Stars {
		if p.RADeg != 0 || p.DecDeg != 0 {
			placed = append(placed, p)
		}
	}
	require.GreaterOrEqual(t, len(placed), len(stars))

	// The record stores directions as float32, which costs about 0.01 px at this focal length — far
	// tighter than the pixel it has to land on.
	const tolPx = 0.05
	matched := 0
	for _, s := range stars {
		x := (s.dir.X/s.dir.Z/m.Camera.TanHalfW)*(float64(cam.wf-1)/2) + float64(cam.wf-1)/2
		y := (s.dir.Y/s.dir.Z/m.Camera.TanHalfH)*(float64(cam.hf-1)/2) + float64(cam.hf-1)/2
		for _, p := range placed {
			if math.Abs(x-float64(p.X)) < tolPx && math.Abs(y-float64(p.Y)) < tolPx {
				matched++
				break
			}
		}
	}
	assert.Equal(t, len(stars), matched, "every star must project back onto the pixel it came from")
}

func farthestObject(m *Manifest) float64 {
	far := 0.0
	for _, b := range m.Billboards {
		far = math.Max(far, b.DistPc)
	}
	return far
}

// TestDepthRange_ReachesPastTheStarsForADistantObject is the M51 case in miniature: a galaxy
// thousands of times farther than any star in the frame. Its distance has to widen the drawing
// range, or the warp flattens it onto the same plane as the background stars.
func TestDepthRange_ReachesPastTheStarsForADistantObject(t *testing.T) {
	stars := []star{
		{distPc: 100}, {distPc: 200}, {distPc: 400}, {distPc: 600},
	}
	t.Run("no objects — the stars set the range", func(t *testing.T) {
		r := depthRange(stars, nil)
		assert.LessOrEqual(t, r.FarPc, 600.0)
	})
	t.Run("a distant galaxy widens it", func(t *testing.T) {
		r := depthRange(stars, []Billboard{{Name: "M51", DistPc: 7.05e6}})
		assert.Equal(t, 7.05e6, r.FarPc, "the range must reach the galaxy")
		assert.LessOrEqual(t, r.NearPc, 100.0)
		// And the galaxy must then land strictly beyond every star rather than on top of them.
		logT := func(d float64) float64 {
			return (math.Log(d) - math.Log(r.NearPc)) / (math.Log(r.FarPc) - math.Log(r.NearPc))
		}
		assert.Equal(t, 1.0, logT(7.05e6))
		assert.Less(t, logT(600), 0.5, "the whole star field sits well in front of it")
	})
	t.Run("a foreground object widens the near end", func(t *testing.T) {
		r := depthRange(stars, []Billboard{{Name: "near", DistPc: 12}})
		assert.Equal(t, 12.0, r.NearPc)
	})
	t.Run("a degenerate span never divides by zero", func(t *testing.T) {
		r := depthRange([]star{{distPc: 500}}, nil)
		assert.Greater(t, r.FarPc, r.NearPc)
	})
}

func TestBuild_RunsWithoutASceneSayWhy(t *testing.T) {
	tests := []struct {
		name   string
		res    *annotate.Result
		reason string
	}{
		{"no annotation at all", nil, reasonUnsolved},
		{"the field never solved", &annotate.Result{Solved: false}, reasonUnsolved},
		{"solved before the frame existed", &annotate.Result{Solved: true}, reasonNoFrame},
		{
			"solved, framed, but nothing could be placed",
			&annotate.Result{Solved: true, Solve: annotate.Solve{Frame: func() *annotate.Frame {
				f := rotatedCamera(10, 20, 0, false, 100, 100).frame()
				return &f
			}()}},
			reasonNoStars,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			m, err := Build(tt.res, Options{RunDir: dir})
			require.NoError(t, err, "a run without a scene is not an error")
			assert.False(t, m.Available)
			assert.Equal(t, tt.reason, m.Reason)

			// The manifest is still persisted, so the viewer can explain itself rather than go blank.
			_, ok := Load(dir)
			assert.True(t, ok)

			// A run whose annotation is merely OLD is fixable by recomputing it, and must say so —
			// that is the difference between offering the user a button and leaving the 3D view
			// mysteriously absent on a run that has 957 detected stars.
			assert.Equal(t, tt.reason == reasonNoFrame, m.NeedsRecompute,
				"only a stale annotation is recoverable by recomputing")
		})
	}
}
