package solarsystem

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
)

// The cross-language pin.
//
// The browser propagates the same elements this package does, because a time scrubber cannot afford
// a round trip per frame. Two implementations of one model is exactly the arrangement that drifts,
// so this writes a fixture the TypeScript mirror is checked against: frontend/src/utils/
// solarsystem.spec.ts reads goldenPath and must reproduce every number in it.
//
// Regenerate after any deliberate change to the model:
//
//	go test ./internal/solarsystem -run Golden -update
//
// If that changes the file, the TS spec fails until utils/solarsystem.ts is brought back in line —
// which is the point.

var update = flag.Bool("update", false, "rewrite testdata/golden.json")

const goldenPath = "testdata/golden.json"

type goldenBody struct {
	Helio   [3]float64 `json:"helio_au"`
	Local   [3]float64 `json:"local_au"`
	PoleRA  float64    `json:"pole_ra_deg"`
	PoleDec float64    `json:"pole_dec_deg"`
	W       float64    `json:"w_deg"`
}

type goldenEpoch struct {
	ISO    string                `json:"iso"`
	JD     float64               `json:"jd"`
	Bodies map[string]goldenBody `json:"bodies"`
}

type golden struct {
	Note     string        `json:"note"`
	Manifest Manifest      `json:"manifest"`
	Epochs   []goldenEpoch `json:"epochs"`
}

// goldenEpochs spans the model's whole validity, and deliberately includes instants that exercise
// the awkward parts: the epoch itself, a date far from it in each direction, and a leap day.
func goldenEpochs() []time.Time {
	return []time.Time{
		time.Date(2000, 1, 1, 12, 0, 0, 0, time.UTC),
		time.Date(1800, 1, 1, 0, 0, 0, 0, time.UTC),
		time.Date(1899, 12, 31, 18, 30, 0, 0, time.UTC),
		time.Date(1969, 7, 20, 20, 17, 0, 0, time.UTC),
		time.Date(2024, 2, 29, 6, 45, 0, 0, time.UTC),
		time.Date(2026, 8, 13, 21, 30, 0, 0, time.UTC),
		time.Date(2050, 12, 31, 23, 59, 0, 0, time.UTC),
	}
}

func buildGolden() golden {
	g := golden{
		Note: "Written by internal/solarsystem/golden_test.go; read by frontend/src/utils/" +
			"solarsystem.spec.ts. Regenerate with: go test ./internal/solarsystem -run Golden -update",
		// A manifest with no textures: which surface maps happen to be downloaded on the machine that
		// regenerated this must not change the fixture.
		Manifest: Build(nil),
	}
	for _, when := range goldenEpochs() {
		jd := astro.JulianDate(when)
		e := goldenEpoch{ISO: when.Format(time.RFC3339), JD: jd, Bodies: map[string]goldenBody{}}
		for _, b := range All() {
			o := b.Pole.OrientationAt(jd)
			e.Bodies[b.Key] = goldenBody{
				Helio:   heliocentricOf(b, jd),
				Local:   LocalAt(b, jd),
				PoleRA:  o.PoleRA,
				PoleDec: o.PoleDec,
				W:       o.WDeg,
			}
		}
		g.Epochs = append(g.Epochs, e)
	}
	// The engine build stamp would churn the fixture on every rebuild without saying anything about
	// the model, so it is pinned out.
	g.Manifest.Engine = "(pinned)"
	return g
}

func TestGolden(t *testing.T) {
	want := buildGolden()

	if *update {
		require.NoError(t, os.MkdirAll(filepath.Dir(goldenPath), 0o755))
		data, err := json.MarshalIndent(want, "", "  ")
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(goldenPath, append(data, '\n'), 0o644))
		t.Log("wrote " + goldenPath)
		return
	}

	raw, err := os.ReadFile(goldenPath)
	require.NoError(t, err, "run with -update to create the fixture")
	var have golden
	require.NoError(t, json.Unmarshal(raw, &have))

	require.Equal(t, len(want.Epochs), len(have.Epochs), "the fixture is stale — regenerate it")
	assert.Equal(t, ManifestVersion, have.Manifest.Version)
	for i, epoch := range want.Epochs {
		got := have.Epochs[i]
		require.Equal(t, epoch.ISO, got.ISO)
		for key, b := range epoch.Bodies {
			g, ok := got.Bodies[key]
			require.Truef(t, ok, "%s missing from the fixture at %s", key, epoch.ISO)
			for axis := 0; axis < 3; axis++ {
				assert.InDeltaf(t, b.Helio[axis], g.Helio[axis], 1e-12, "%s helio[%d] at %s", key, axis, epoch.ISO)
			}
			assert.InDeltaf(t, b.W, g.W, 1e-9, "%s prime meridian at %s", key, epoch.ISO)
		}
	}
}
