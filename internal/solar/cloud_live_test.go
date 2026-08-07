package solar

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// cloud_live_test.go streams a real clip and prints, per frame, the two things that separate a clear
// frame from one behind a passing cloud: how much light reached the sensor, and how sharp what
// arrived was. Opt-in:
//
//	ASTRO_SOLAR_CLIPS='input/2026_08_04_v2/x.MOV' go test ./internal/solar -run Cloud_Live -v
func TestCloudProfile_Live(t *testing.T) {
	clips := os.Getenv("ASTRO_SOLAR_CLIPS")
	if clips == "" {
		t.Skip("set ASTRO_SOLAR_CLIPS=<comma-separated clip paths> to profile transparency")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	for _, path := range strings.Split(clips, ",") {
		if path = strings.TrimSpace(path); path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join("..", "..", path)
		}
		t.Run(filepath.Base(path), func(t *testing.T) {
			info := probeVideo(ctx, "", path)
			require.Greater(t, info.Frames, 0)

			start := time.Now()
			scan, err := scanVideo(ctx, "", path, info, defaultCropMargin)
			require.NoError(t, err)
			t.Logf("%s: %d frames scanned in %s", filepath.Base(path), len(scan.frames), time.Since(start).Round(time.Second))

			var b strings.Builder
			levels := make([]float64, 0, len(scan.frames))
			for _, f := range scan.frames {
				levels = append(levels, f.level)
			}
			med := median(levels)
			fmt.Fprintf(&b, "\n  median transparency %.5g\n", med)
			for i, f := range scan.frames {
				if i%10 != 0 {
					continue
				}
				bar := int(40 * f.level / med)
				fmt.Fprintf(&b, "  %4d  t=%5.1f%%  score=%.5g  %s\n",
					f.index, 100*f.level/med, f.score, strings.Repeat("#", clampInt(bar, 0, 60)))
			}
			t.Log(b.String())

			// How much of the clip a transparency cut would remove, at several depths.
			for _, cut := range []float64{0.99, 0.97, 0.95, 0.90, 0.80} {
				n := 0
				for _, f := range scan.frames {
					if f.level < cut*med {
						n++
					}
				}
				t.Logf("  below %.0f%% of median: %d/%d frames (%.1f%%)",
					100*cut, n, len(scan.frames), 100*float64(n)/float64(len(scan.frames)))
			}
		})
	}
}
