package solar

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestProbeOverlay_Live renders the fitted limb over real frames so a human can check the geometry
// by eye. A circle fit is the one thing here that a passing number cannot vouch for. Opt-in:
//
//	ASTRO_SOLAR_OVERLAY=/tmp/out ASTRO_SOLAR_FILES=input/2026_07_30_SUN/IMG_0736.DNG \
//	  go test ./internal/solar -run Overlay_Live -v
func TestProbeOverlay_Live(t *testing.T) {
	outDir := os.Getenv("ASTRO_SOLAR_OVERLAY")
	files := os.Getenv("ASTRO_SOLAR_FILES")
	if outDir == "" || files == "" {
		t.Skip("set ASTRO_SOLAR_OVERLAY=<dir> and ASTRO_SOLAR_FILES=<comma-separated paths>")
	}
	require.NoError(t, os.MkdirAll(outDir, 0o755))
	for _, path := range strings.Split(files, ",") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		t.Run(filepath.Base(path), func(t *testing.T) {
			dst := filepath.Join(outDir, "limb_"+sanitizeName(path)+".png")
			p, err := ProbeOverlay(context.Background(), "", path, dst, false)
			require.NoError(t, err)
			t.Logf("ok=%v centre=(%.1f,%.1f) r=%.1f arc=%.0f° resid=%.2fpx points=%d partial=%v → %s",
				p.DiscOK, p.Disc.CX, p.Disc.CY, p.Disc.R, p.Disc.ArcDeg, p.Disc.ResidRMS,
				p.Disc.Points, p.Disc.Partial, dst)
		})
	}
}
