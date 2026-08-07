package solar

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
)

// readFrame loads a materialised frame back off disk.
func readFrame(path string) (*fits.Image, error) { return fits.ReadImage(path) }

// TestIngestVideo_Live runs the real two-pass ingest over real clips. Opt-in:
//
//	ASTRO_SOLAR_CLIPS=input/2026_07_30_SUN/IMG_0734.MOV go test ./internal/solar -run Ingest_Live -v
func TestIngestVideo_Live(t *testing.T) {
	clips := os.Getenv("ASTRO_SOLAR_CLIPS")
	if clips == "" {
		t.Skip("set ASTRO_SOLAR_CLIPS=<comma-separated clip paths> to run the live ingest")
	}
	work := t.TempDir()
	if keep := os.Getenv("ASTRO_SOLAR_WORK"); keep != "" {
		work = keep
		require.NoError(t, os.MkdirAll(work, 0o755))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	for _, path := range strings.Split(clips, ",") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join("..", "..", path) // go test runs in the package dir
		}
		t.Run(filepath.Base(path), func(t *testing.T) {
			info := probeVideo(ctx, "", path)
			require.Greater(t, info.Frames, 0, "the clip must probe")

			start := time.Now()
			frames, warnings, err := IngestVideo(ctx, path, info, IngestOptions{WorkDir: work})
			require.NoError(t, err)
			elapsed := time.Since(start)

			require.NotEmpty(t, frames)
			for _, w := range warnings {
				t.Logf("  ! %s", w)
			}

			var bytes int64
			for _, f := range frames {
				st, err := os.Stat(f.Path)
				require.NoError(t, err, "every reported frame must exist on disk")
				bytes += st.Size()
			}
			first, last := frames[0], frames[len(frames)-1]
			t.Logf("%s: %d/%d frames kept in %s, %.2f GB written, r=%.1f px, score %.4g..%.4g",
				filepath.Base(path), len(frames), info.Frames, elapsed.Round(time.Second),
				float64(bytes)/1e9, first.Limb.R, last.Score, first.Score)

			// Frames come back in capture order, which the session windowing and the time-lapse both
			// depend on.
			for i := 1; i < len(frames); i++ {
				assert.Greater(t, frames[i].Index, frames[i-1].Index, "frames must be in capture order")
			}
			// Selection must actually select, and the cap must hold.
			assert.LessOrEqual(t, len(frames), defaultMaxFrames)
			if info.Frames > defaultMaxFrames {
				assert.Less(t, len(frames), info.Frames, "a long clip must be thinned")
			}
			// The whole point of cropping: the materialised raster is a fraction of the source.
			dw, dh := displayDims(info)
			im, err := readFrame(frames[0].Path)
			require.NoError(t, err)
			t.Logf("  crop %dx%d from %dx%d (%.1f%% of the pixels)",
				im.W, im.H, dw, dh, 100*float64(im.W*im.H)/float64(dw*dh))
			assert.LessOrEqual(t, im.W, dw)
			assert.LessOrEqual(t, im.H, dh)
			// Every kept frame must still carry a usable limb in its own cropped coordinates.
			assert.Greater(t, frames[0].Limb.R, 0.0, "the crop must not lose the limb")
		})
	}
}

// TestIngestStills_Live runs the real still ingest over a set of camera raws. Opt-in:
//
//	ASTRO_SOLAR_STILLS='input/2026_07_30_SUN/IMG_0736.DNG,...' go test ./internal/solar -run Stills_Live -v
func TestIngestStills_Live(t *testing.T) {
	list := os.Getenv("ASTRO_SOLAR_STILLS")
	if list == "" {
		t.Skip("set ASTRO_SOLAR_STILLS=<comma-separated still paths> to run the live still ingest")
	}
	var paths []string
	for _, p := range strings.Split(list, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join("..", "..", p)
		}
		paths = append(paths, p)
	}
	require.NotEmpty(t, paths)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	work := t.TempDir()

	start := time.Now()
	frames, warnings, err := IngestStills(ctx, paths, IngestOptions{WorkDir: work})
	require.NoError(t, err)
	for _, w := range warnings {
		t.Logf("  ! %s", w)
	}
	require.NotEmpty(t, frames)
	t.Logf("%d/%d stills ingested in %s", len(frames), len(paths), time.Since(start).Round(time.Second))

	im, err := readFrame(frames[0].Path)
	require.NoError(t, err)
	t.Logf("  crop %dx%d, r=%.1f px", im.W, im.H, frames[0].Limb.R)

	// Every frame must land on the same raster and be centred on its own disc — that is what makes
	// the set stackable at all.
	for _, f := range frames {
		fi, err := readFrame(f.Path)
		require.NoError(t, err)
		assert.Equal(t, im.W, fi.W, "%s: all frames share one raster", filepath.Base(f.Source))
		assert.Equal(t, im.H, fi.H)
		require.Greater(t, f.Limb.R, 0.0, "%s: limb lost in the crop", filepath.Base(f.Source))
		assert.InDelta(t, float64(im.W)/2, f.Limb.CX, 0.08*f.Limb.R, "%s: disc off-centre", filepath.Base(f.Source))
		assert.InDelta(t, float64(im.H)/2, f.Limb.CY, 0.08*f.Limb.R, "%s: disc off-centre", filepath.Base(f.Source))
	}

	t.Run("photometric normalisation converges the bracketed exposures", func(t *testing.T) {
		spread := func() float64 {
			var meds []float64
			for _, f := range frames {
				fi, err := readFrame(f.Path)
				require.NoError(t, err)
				meds = append(meds, discStats(fi, f.Limb.CX, f.Limb.CY, f.Limb.R).median)
			}
			lo, hi := meds[0], meds[0]
			for _, m := range meds {
				lo, hi = math.Min(lo, m), math.Max(hi, m)
			}
			return hi / lo
		}
		before := spread()
		warns, err := Normalize(frames)
		require.NoError(t, err)
		for _, w := range warns {
			t.Logf("  ! %s", w)
		}
		after := spread()
		t.Logf("  on-disc brightness spread: %.2fx before, %.2fx after", before, after)
		assert.Less(t, after, before, "normalisation must bring the frames together")
		assert.Less(t, after, 1.15, "and leave them within a few percent of each other")
	})
}
