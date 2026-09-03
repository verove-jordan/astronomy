package meteor

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/trail"
)

// TestZZRaw hunts streaks in the ORIGINAL frames, before registration and before any differencing.
//
// Every earlier attempt worked in a difference image and drowned in star-registration residue —
// residue that is bright, everywhere, and single-frame, so it imitates a meteor on every axis that
// was available. An original frame has none of it: a meteor is a bright streak lying on smooth sky.
// The stars are still there, which is what trail.RawParams is for — it seeds at the bright end so
// star pixels do not flood the Hough accumulator.
//
//	ASTRO_RAW_SEQ=<work/.../01_seq> go test ./internal/meteor/ -run TestZZRaw -v
func TestZZRaw(t *testing.T) {
	seq := os.Getenv("ASTRO_RAW_SEQ")
	if seq == "" {
		t.Skip("set ASTRO_RAW_SEQ")
	}
	// light_* are converted but NOT registered; r_light_* have been resampled.
	frames, _ := filepath.Glob(filepath.Join(seq, "light_*.fits"))
	sort.Strings(frames)
	if len(frames) == 0 {
		t.Skip("no unregistered frames")
	}
	voteFrac := 0.08
	if v := os.Getenv("ASTRO_RAW_VOTEFRAC"); v != "" {
		fmt.Sscanf(v, "%f", &voteFrac)
	}
	k := 5.0
	if v := os.Getenv("ASTRO_RAW_K"); v != "" {
		fmt.Sscanf(v, "%f", &k)
	}
	// Sweep the seed threshold in ONE pass over the frames: a brighter seed means fewer pixels enter
	// the accumulator, which is what a frame full of stars and Milky Way needs.
	ks := []float64{k, 8, 12, 20, 30}
	hits := make([]int, len(ks))
	fmt.Printf("%d unregistered frames, span>=%.0f%% of the frame\n", len(frames), 100*voteFrac)

	total := 0
	for fi, path := range frames {
		im, err := fits.ReadImage(path)
		if err != nil || im == nil {
			continue
		}
		var segs []trail.Segment
		for ki, kk := range ks {
			got := trail.DetectSegments(im.Pix[1], im.W, im.H,
				trail.Params{Mode: trail.Raw, RawSeedK: kk, VoteFrac: voteFrac})
			hits[ki] += len(got)
			if ki == 0 || (len(segs) == 0 && len(got) > 0) {
				segs = got
			}
		}
		for _, s := range segs {
			length := s.T1 - s.T0
			// A line as long as the frame is the border or the band, not a meteor.
			if length > 0.85*float64(min(im.W, im.H)) {
				continue
			}
			ang := math.Mod(math.Atan2(s.Nx, -s.Ny)*180/math.Pi+180, 180)
			fmt.Printf("  frame %2d: len %5.0f px  width %4.1f  score %.2f  angle %3.0f deg\n",
				fi+1, length, s.Width, s.Score, ang)
			total++
		}
	}
	for ki, kk := range ks {
		fmt.Printf("  seed k=%-4.0f -> %d raw segment(s) across all frames\n", kk, hits[ki])
	}
	fmt.Printf("%d candidate streaks across %d frames\n", total, len(frames))
}
