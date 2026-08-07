package scene3d_test

// Live contract test for the 3D field map: build a scene from a REAL finished run and check that
// the physics comes out right. Skipped unless ASTRO_LIVE_RUN_DIR points at a run directory, so it
// costs nothing in CI and on a fresh clone.
//
// The check that matters is not "does it render" — it is whether the stars the frame resolved
// inside a catalogued object pile up at that object's known distance. If the Trapezium does not
// land on the Orion Nebula, the depth ladder is wrong somewhere between the plate solution, the
// catalogue cross-match and the scene basis, and no amount of looking at the render would say so.
//
//	ASTRO_LIVE_RUN_DIR=output/M42/20260723_180917 \
//	ASTRO_LIVE_OBJECT=M42 ASTRO_LIVE_DIST_PC=412 \
//	go test ./internal/scene3d/ -run TestLive -v

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/annotate"
	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/scene3d"
)

func liveRunDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("ASTRO_LIVE_RUN_DIR")
	if dir == "" {
		t.Skip("set ASTRO_LIVE_RUN_DIR to a finished run directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "final.png")); err != nil {
		t.Skipf("no final.png in %s", dir)
	}
	return dir
}

func TestLive_SceneFromRealRun(t *testing.T) {
	dir := liveRunDir(t)
	cfg := config.Load()

	res, err := annotate.Run(context.Background(), annotate.Options{
		RunDir:      dir,
		Mode:        os.Getenv("ASTRO_LIVE_MODE"),
		CatalogDir:  cfg.SirilCatalogDir,
		StarCatalog: cfg.DeepStarCat,
	})
	require.NoError(t, err)
	t.Logf("annotation: %d stars, solved=%v (%s), catalogue=%s, identified=%d, zp=%.2f",
		res.Count, res.Solved, res.Solve.Reason, res.Solve.StarCatalog, res.Solve.Identified,
		res.Solve.MagZeroPoint)
	require.True(t, res.Solved, "the field must solve for a 3D scene to exist: %s", res.Solve.Reason)
	require.NotNil(t, res.Solve.Frame, "a solved run must carry the scene frame")

	m, err := scene3d.Build(res, scene3d.Options{RunDir: dir})
	require.NoError(t, err)
	require.True(t, m.Available, "reason: %s", m.Reason)

	t.Logf("scene: %d plotted → %d placed (%d measured, %d estimated, %d unplaceable)",
		m.Stars.Plotted, m.Stars.Placed, m.Stars.Measured, m.Stars.Estimated, m.Stars.Unknown)
	t.Logf("depth: %.0f … %.0f pc (median %.0f, extremes %.0f … %.0f)",
		m.Depth.NearPc, m.Depth.FarPc, m.Depth.MedianPc, m.Depth.MinPc, m.Depth.MaxPc)
	t.Logf("camera: %.4f° × %.4f° field, centre RA %.4f Dec %.4f, right-handed=%v",
		2*math.Atan(m.Camera.TanHalfW)*180/math.Pi, m.Camera.FovYDeg,
		m.Camera.CenterRA, m.Camera.CenterDec, m.Camera.RightHanded)
	t.Logf("photometry: calibrated=%v pairs=%d rms=%.3f holdout=%d ×%.2f ±%.2f dex (%s)",
		m.Photometric.Calibrated, m.Photometric.Pairs, m.Photometric.RMS,
		m.Photometric.HoldoutN, m.Photometric.HoldoutMedianRatio, m.Photometric.HoldoutScatterDex,
		m.Photometric.Reason)
	for _, b := range m.Billboards {
		t.Logf("object: %-12s %10.1f pc  (%s, %d members, ±%.3f dex, table %.1f pc)",
			b.Name, b.DistPc, b.DistSource, b.Members, b.SigmaDex, b.TableDistPc)
	}

	assert.Greater(t, m.Stars.Placed, 0, "no star could be placed in depth")
	assert.Greater(t, m.Stars.Measured, 0, "no star carried a catalogued parallax")
	assert.FileExists(t, filepath.Join(dir, m.Points))
	assert.FileExists(t, filepath.Join(dir, m.Backdrop))

	// The hold-out grade is the quality gate for the estimated layer. It is reported rather than
	// asserted hard, because a narrowband or poorly colour-calibrated stack legitimately fails it —
	// that is exactly what the UI warning exists for.
	if m.Photometric.HoldoutN > 0 {
		assert.InDelta(t, 1.0, m.Photometric.HoldoutMedianRatio, 0.9,
			"estimated distances are wildly off on this frame")
	}
}

// TestLive_ObjectMembersLandOnTheObject is the physics check. Set ASTRO_LIVE_OBJECT to a catalogued
// object in the field and ASTRO_LIVE_DIST_PC to its accepted distance; the stars inside its
// footprint that carry a MEASURED parallax must cluster there.
func TestLive_ObjectMembersLandOnTheObject(t *testing.T) {
	dir := liveRunDir(t)
	object := os.Getenv("ASTRO_LIVE_OBJECT")
	wantPc, _ := strconv.ParseFloat(os.Getenv("ASTRO_LIVE_DIST_PC"), 64)
	if object == "" || wantPc <= 0 {
		t.Skip("set ASTRO_LIVE_OBJECT and ASTRO_LIVE_DIST_PC")
	}
	cfg := config.Load()

	res, err := annotate.Run(context.Background(), annotate.Options{
		RunDir:      dir,
		Mode:        os.Getenv("ASTRO_LIVE_MODE"),
		CatalogDir:  cfg.SirilCatalogDir,
		StarCatalog: cfg.DeepStarCat,
	})
	require.NoError(t, err)
	require.True(t, res.Solved, res.Solve.Reason)

	var label *annotate.Label
	for i := range res.Labels {
		if res.Labels[i].Name == object && res.Labels[i].Extent != nil {
			label = &res.Labels[i]
			break
		}
	}
	require.NotNil(t, label, "%s is not a labelled object with a footprint in this field", object)

	// Stars inside the object's own ellipse that the catalogue actually measured.
	var dists []float64
	for _, p := range res.Stars {
		if p.Star == nil || p.Star.DistPc <= 0 {
			continue
		}
		dx, dy := float64(p.X)-label.X, float64(p.Y)-label.Y
		sin, cos := math.Sincos(label.Extent.AngleRad)
		u := (dx*cos + dy*sin) / label.Extent.RXpx
		v := (-dx*sin + dy*cos) / label.Extent.RYpx
		if u*u+v*v <= 1 {
			dists = append(dists, p.Star.DistPc)
		}
	}
	require.GreaterOrEqual(t, len(dists), 5,
		"only %d stars inside %s carry a measured parallax", len(dists), object)

	sort.Float64s(dists)
	median := dists[len(dists)/2]
	// Count how many sit within a factor of two of the accepted distance — the members, as opposed
	// to the foreground and background the same line of sight also passes through.
	near := 0
	for _, d := range dists {
		if d > wantPc/2 && d < wantPc*2 {
			near++
		}
	}
	t.Logf("%s: %d measured stars in the footprint, median %.0f pc, accepted %.0f pc, %d within a factor of 2",
		object, len(dists), median, wantPc, near)

	assert.Greater(t, near, len(dists)/4,
		"the stars inside %s do not cluster at its distance — the depth ladder is wrong", object)
}
