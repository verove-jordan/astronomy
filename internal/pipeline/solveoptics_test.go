package pipeline

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// frameWithPixelSize writes a small FITS carrying XPIXSZ, the one optical fact a converted camera
// raw reliably keeps. um <= 0 writes no keyword at all (a header that cannot answer).
func frameWithPixelSize(t *testing.T, um float64) string {
	t.Helper()
	im := fits.NewImage(16, 16, 1)
	path := filepath.Join(t.TempDir(), "master_RGB.fits")
	var cards []string
	if um > 0 {
		cards = append(cards, fmt.Sprintf("%-80s", fmt.Sprintf("XPIXSZ  = %20.5f / [um] Pixel X axis size", um)))
	}
	require.NoError(t, im.WriteFITSWith(path, cards))
	return path
}

func TestSolveOpticsFor(t *testing.T) {
	// The engine's configured rig: Takahashi FC-100 DF + ASI1600MM Pro.
	rig := siril.SolveOptions{FocalMM: 740, PixelUm: 3.8, Catalog: "localgaia"}

	tests := []struct {
		name       string
		configured siril.SolveOptions
		headerUm   float64
		explicit   bool
		wantOptics bool // the focal length / pixel size survive
		wantWarn   bool
	}{
		{
			// The job-629 failure: a Nikon Z6 (5.94 µm) solved at the ASI1600's 3.8 µm and 740 mm —
			// 1.06 arcsec/px claimed against roughly 6. The solve could not succeed, so SPCC never
			// ran and the stars came out colourless.
			name:       "another camera's optics are dropped",
			configured: rig, headerUm: 5.94,
			wantOptics: false, wantWarn: true,
		},
		{
			name:       "the configured rig's own frames keep them",
			configured: rig, headerUm: 3.8,
			wantOptics: true,
		},
		{
			// Sensor specs get rounded — 3.76 and 3.8 are the same camera.
			name:       "a rounded pixel size is the same camera",
			configured: rig, headerUm: 3.76,
			wantOptics: true,
		},
		{
			// A run that states its optics is believed, even against the header: the user may be
			// correcting a wrong XPIXSZ, and second-guessing an explicit answer has no upside.
			name:       "an explicit run declaration is never second-guessed",
			configured: rig, headerUm: 5.94, explicit: true,
			wantOptics: true,
		},
		{
			// Nothing to compare against: the check cannot run, so behaviour is exactly as before.
			name:       "a header with no pixel size changes nothing",
			configured: rig, headerUm: 0,
			wantOptics: true,
		},
		{
			name:       "nothing configured, nothing to drop",
			configured: siril.SolveOptions{Catalog: "localgaia"}, headerUm: 5.94,
			wantOptics: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, warn := solveOpticsFor(tt.configured, frameWithPixelSize(t, tt.headerUm), tt.explicit)

			if tt.wantOptics {
				assert.Equal(t, tt.configured.FocalMM, got.FocalMM)
				assert.Equal(t, tt.configured.PixelUm, got.PixelUm)
			} else {
				assert.Zero(t, got.FocalMM, "a focal length from another telescope is worse than none")
				assert.Zero(t, got.PixelUm)
			}
			assert.Equal(t, tt.wantWarn, warn != "", "warning presence")
			// Everything that is not optics must survive untouched — dropping the catalogue would
			// take the solve offline as well.
			assert.Equal(t, tt.configured.Catalog, got.Catalog)
		})
	}
}

func TestSolveOpticsFor_UnreadableFileKeepsTheConfiguredOptics(t *testing.T) {
	rig := siril.SolveOptions{FocalMM: 740, PixelUm: 3.8}
	got, warn := solveOpticsFor(rig, filepath.Join(t.TempDir(), "absent.fits"), false)
	assert.Equal(t, rig, got, "a missing master is not evidence of a different camera")
	assert.Empty(t, warn)
}
