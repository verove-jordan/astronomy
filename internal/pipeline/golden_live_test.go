package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits/fitstest"
	"github.com/verove-jordan/astronomy/internal/mode"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// The single-night byte-identity golden: a real (host-Siril) mini deep-sky run over a synthetic
// one-night capture, whose ordered step sequence and normalized run.json are pinned under
// testdata/golden_single_night/. The multi-session work (night sessionization, per-night flats,
// photometric normalization, per-session progress) must leave this diff EMPTY — a one-night run's
// grouping, calibration, scripts, steps and result payload are a locked contract.
//
// Gated like the Siril syntax live tests: runs only with ASTRO_GOLDEN_LIVE=1 and a host siril-cli.
// Regenerate with ASTRO_UPDATE_GOLDEN=1 (only intentionally — an update IS a contract change).
const goldenDir = "testdata/golden_single_night"

func goldenRunner(t *testing.T) *siril.Runner {
	t.Helper()
	if os.Getenv("ASTRO_GOLDEN_LIVE") == "" {
		t.Skip("set ASTRO_GOLDEN_LIVE=1 (and have host siril-cli) to run the byte-identity golden")
	}
	bin := os.Getenv("SIRIL_BIN")
	if bin == "" {
		bin = "/Applications/Siril.app/Contents/MacOS/siril-cli"
	}
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("no siril-cli at %s", bin)
	}
	return siril.New(bin, siril.Limits{})
}

// goldenLCG is a deterministic pseudo-random source — the golden must be reproducible bit-for-bit.
type goldenLCG struct{ s uint64 }

func (l *goldenLCG) next() float64 {
	l.s = l.s*6364136223846793005 + 1442695040888963407
	return float64(l.s>>11) / float64(1<<53)
}

// writeGoldenFrame writes one synthetic 16-bit frame: a dithered star field for lights, a vignette
// for flats, near-pedestal noise for bias/darks. Headers carry the one-night DATE-OBS.
func writeGoldenFrame(t *testing.T, dir, name, imagetyp, filter string, expSec float64, seq int) {
	t.Helper()
	const w, h = 256, 256
	require.NoError(t, os.MkdirAll(dir, 0o755))
	pix := make([]uint16, w*h)
	rnd := goldenLCG{s: uint64(1e6*expSec) + uint64(seq)<<32 + uint64(len(filter))}
	grid := make([]float64, w*h)
	for i := range grid {
		grid[i] = 500 + 40*rnd.next()
	}
	switch imagetyp {
	case "Light":
		dx, dy := float64(seq)*1.5, float64(seq)*0.5 // per-frame dither so registration has work to do
		stars := goldenLCG{s: 42}
		for s := 0; s < 40; s++ {
			cx := 20 + stars.next()*float64(w-40) + dx
			cy := 20 + stars.next()*float64(h-40) + dy
			amp := 8000 + 24000*stars.next()
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
		"DATE-OBS": fmt.Sprintf("'2024-03-01T22:%02d:%02d'", 10+seq, (seq*7)%60),
		"INSTRUME": "'ZWO ASI1600MM Pro'",
	}
	if filter != "" {
		cards["FILTER"] = "'" + filter + "'"
	}
	fitstest.WritePixels(t, dir, name+".fits", w, h, pix, cards)
}

// buildGoldenCapture synthesizes the one-night capture: 3 lights × L/R/G/B + darks/flats/bias.
func buildGoldenCapture(t *testing.T, root string) {
	t.Helper()
	for _, f := range []string{"L", "R", "G", "B"} {
		for i := 0; i < 3; i++ {
			writeGoldenFrame(t, filepath.Join(root, "lights"), fmt.Sprintf("light_%s_%d", f, i), "Light", f, 10, i)
		}
	}
	for i := 0; i < 3; i++ {
		writeGoldenFrame(t, filepath.Join(root, "darks"), fmt.Sprintf("dark_%d", i), "Dark", "", 10, i)
		writeGoldenFrame(t, filepath.Join(root, "flats"), fmt.Sprintf("flat_%d", i), "Flat", "", 0.005, i)
		writeGoldenFrame(t, filepath.Join(root, "bias"), fmt.Sprintf("bias_%d", i), "Bias", "", 0.0001, i)
	}
}

// goldenPreset is the deterministic deepsky preset for the golden run: every external-tool and
// model-driven step off (GraXpert/StarNet/SPCC/supervisor/previews), pure Siril + Go math only.
func goldenPreset() *mode.Preset {
	p := mode.For(mode.Deepsky)
	p.BackgroundAI = false
	p.CombinedBackgroundAI = false
	p.ColorDenoiseAI = false
	p.ColorCalibration = false
	p.Previews = false
	p.AutoFixStars = false
	p.Supervise = false
	p.StarReduce = 0
	p.TrailMaskK = 0
	p.DenoiseChroma, p.DenoiseLum = 0, 0
	return &p
}

// GOLDEN RE-PIN 2026-07-16: the pinned steps/run.json predated EmitLuminanceMono defaulting to true
// for deepsky (mode/preset.go) — the mono side-output ("mono finish (lum_mono)" step +
// final.mono_outputs) is that feature's intended default, so the pin was refreshed to include it.
// The re-pin diff contained ONLY the mono artifacts (verified line by line); everything else stayed
// byte-identical through the windowed trail-mask + colour-ladder changes of the same day.
//
// GOLDEN RE-PIN 2026-08-03: user-selectable stacking/rejection algorithms (internal/stackalg) moved
// the hardcoded `stack … rej winsorized 3 3 -norm=addscale` literals behind a renderer. The pin was
// refreshed ONLY to add the four new provenance keys in the options block (stack_engine/combine/
// reject/norm — which algorithm actually produced the master); the run itself is byte-identical, as
// this test proved by passing UNCHANGED before those keys were added.
func TestProcess_SingleNightGolden(t *testing.T) {
	runner := goldenRunner(t)
	root := t.TempDir()
	in, work, out := filepath.Join(root, "in"), filepath.Join(root, "work"), filepath.Join(root, "out")
	buildGoldenCapture(t, in)

	// Record each DISTINCT (index, step) once, in first-seen order: progress events from different
	// sources (step boundaries, log pump, previews) can interleave differently run to run around a
	// boundary, so a raw transition log flakes; the first-seen set is stable and still pins the
	// step names, their order, and the Index/Total math.
	var steps []string
	seenStep := map[string]bool{}
	opts := Options{
		InputDir: in, WorkDir: work, OutputDir: out,
		Runner: runner, Preset: goldenPreset(),
		OnProgress: func(p Progress) {
			line := fmt.Sprintf("%d/%d %s", p.Index, p.Total, p.Step)
			if !seenStep[line] {
				seenStep[line] = true
				steps = append(steps, line)
			}
		},
	}
	res, err := Process(context.Background(), opts)
	require.NoError(t, err)
	require.NotNil(t, res)

	stepsTxt := strings.Join(steps, "\n") + "\n"
	runJSON := normalizedRunJSON(t, res, root)

	if os.Getenv("ASTRO_UPDATE_GOLDEN") != "" {
		require.NoError(t, os.MkdirAll(goldenDir, 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(goldenDir, "steps.txt"), []byte(stepsTxt), 0o644))
		require.NoError(t, os.WriteFile(filepath.Join(goldenDir, "run.json"), runJSON, 0o644))
		t.Logf("golden updated under %s", goldenDir)
		return
	}
	wantSteps, err := os.ReadFile(filepath.Join(goldenDir, "steps.txt"))
	require.NoError(t, err, "golden missing — capture it first with ASTRO_UPDATE_GOLDEN=1")
	wantJSON, err := os.ReadFile(filepath.Join(goldenDir, "run.json"))
	require.NoError(t, err)
	assert.Equal(t, string(wantSteps), stepsTxt, "single-night STEP SEQUENCE must not change")
	assert.Equal(t, string(wantJSON), string(runJSON), "single-night run RESULT must not change")
}

// normalizedRunJSON marshals the run Result with every volatile field neutralized: the timestamped
// run id, all absolute paths under the test root, and wall-clock timings (step names kept).
func normalizedRunJSON(t *testing.T, res *Result, root string) []byte {
	t.Helper()
	raw, err := json.Marshal(res)
	require.NoError(t, err)
	var v any
	require.NoError(t, json.Unmarshal(raw, &v))
	v = normalizeGolden(v, res.RunID, root)
	outJSON, err := json.MarshalIndent(v, "", "  ")
	require.NoError(t, err)
	return append(outJSON, '\n')
}

// goldenVolatileKeys are dropped wholesale: wall-clock and environment-dependent values whose
// variation is expected run to run (the step names inside timings are covered by steps.txt).
var goldenVolatileKeys = map[string]bool{
	"timings": true, "engine": true, "duration_ms": true, "elapsed_ms": true, "seconds": true,
	"created_at": true, "date_obs_ms": true,
}

func normalizeGolden(v any, runID, root string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if goldenVolatileKeys[k] {
				continue
			}
			out[k] = normalizeGolden(val, runID, root)
		}
		return out
	case []any:
		for i := range x {
			x[i] = normalizeGolden(x[i], runID, root)
		}
		return x
	case string:
		s := strings.ReplaceAll(x, root, "<ROOT>")
		return strings.ReplaceAll(s, runID, "<RUN>")
	default:
		return v
	}
}
