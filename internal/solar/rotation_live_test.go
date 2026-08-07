package solar

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// rotation_live_test.go measures the one registration term the limb cannot supply.
//
// Scale and translation come out of the circle fit exactly. Rotation does not — a circle is
// rotation-invariant — so it is correlated off disc structure, and that estimate is the only place a
// silent, isotropic error can enter the stack. It matters most exactly where a session is most
// valuable: across two clips shot a minute apart, over which an alt-az field turns by a third of a
// degree a minute, which at a 900 px radius is seven pixels of motion at the limb.
//
//	ASTRO_SOLAR_FRAMES=<dir of ingested *.fits> go test ./internal/solar -run Rotation_Live -v
func TestRotation_Live(t *testing.T) {
	dir := os.Getenv("ASTRO_SOLAR_FRAMES")
	if dir == "" {
		t.Skip("set ASTRO_SOLAR_FRAMES=<dir of ingested frames>")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.fits"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	sort.Strings(paths)

	type shot struct {
		path   string
		source string
		im     *fits.Image
		limb   Limb
		score  float64
		deg    float64
		ok     bool
	}
	shots := make([]shot, 0, len(paths))
	for _, p := range paths {
		im, err := fits.ReadImage(p)
		require.NoError(t, err)
		mono := firstPlane(im)
		l, ok := FitLimb(mono)
		if !ok {
			continue
		}
		base := filepath.Base(p)
		shots = append(shots, shot{
			path: p, source: base[:strings.LastIndex(base, "_")],
			im: mono, limb: l, score: FrameSharpness(mono, l),
		})
	}
	require.NotEmpty(t, shots)

	// The reference the stack itself would pick: the sharpest frame.
	ref := 0
	for i := range shots {
		if shots[i].score > shots[ref].score {
			ref = i
		}
	}
	t.Logf("%d frames, reference %s", len(shots), filepath.Base(shots[ref].path))

	fails := 0
	for i := range shots {
		d, ok := EstimateRotation(shots[ref].im, shots[i].im, shots[ref].limb, shots[i].limb)
		shots[i].deg, shots[i].ok = d, ok
		if !ok {
			fails++
		}
	}
	t.Logf("rotation solved on %d/%d frames (%d refused)", len(shots)-fails, len(shots), fails)

	// Per source: the systematic part is what a stack of two clips has to get right.
	bySource := map[string][]float64{}
	for _, s := range shots {
		if s.ok {
			bySource[s.source] = append(bySource[s.source], s.deg)
		}
	}
	names := make([]string, 0, len(bySource))
	for k := range bySource {
		names = append(names, k)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, n := range names {
		v := bySource[n]
		med := median(v)
		dev := make([]float64, len(v))
		for i, x := range v {
			dev[i] = math.Abs(x - med)
		}
		lo, hi := v[0], v[0]
		for _, x := range v {
			lo, hi = math.Min(lo, x), math.Max(hi, x)
		}
		fmt.Fprintf(&b, "\n  %-24s n=%3d  median %+.3f°  scatter(MAD) %.3f°  range %+.3f..%+.3f°",
			n, len(v), med, 1.4826*median(dev), lo, hi)
		fmt.Fprintf(&b, "\n      => at the limb (R=%.0f): median %.2f px, scatter %.2f px",
			shots[ref].limb.R, math.Abs(med)*math.Pi/180*shots[ref].limb.R,
			1.4826*median(dev)*math.Pi/180*shots[ref].limb.R)
	}
	if len(names) == 2 {
		gap := median(bySource[names[1]]) - median(bySource[names[0]])
		fmt.Fprintf(&b, "\n\n  between the two clips: %+.3f°  =  %.2f px at the limb",
			gap, math.Abs(gap)*math.Pi/180*shots[ref].limb.R)
	}
	t.Log(b.String())

	// A walk through the clip, so a drift is visible as a drift rather than as scatter.
	var w strings.Builder
	for i, s := range shots {
		if i%15 != 0 {
			continue
		}
		fmt.Fprintf(&w, "\n  %-40s %+.3f° ok=%v", filepath.Base(s.path), s.deg, s.ok)
	}
	t.Log(w.String())
}
