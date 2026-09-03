package panelgroup

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/verove-jordan/astronomy/internal/pointing"
	"github.com/verove-jordan/astronomy/internal/rawmeta"
)

// TestZZSurvey reports what a folder of raw stills actually contains: when each frame was shot, where
// it was aimed, and how the grouper splits them into pointings.
//
// This is the first thing to run on a new session. Whether frames can join an existing panorama is a
// question about their POINTING and their TIME, and both are in the files — reading them beats
// guessing from filenames or from what the folder is called.
//
//	ASTRO_SURVEY_DIR=<abs input/...> go test ./internal/panelgroup/ -run TestZZSurvey -v
func TestZZSurvey(t *testing.T) {
	dir := os.Getenv("ASTRO_SURVEY_DIR")
	if dir == "" {
		t.Skip("set ASTRO_SURVEY_DIR to a folder of raw stills")
	}
	var paths []string
	for _, ext := range []string{"*.DNG", "*.dng", "*.HEIC", "*.heic"} {
		g, _ := filepath.Glob(filepath.Join(dir, ext))
		paths = append(paths, g...)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		t.Skipf("no raw stills under %s", dir)
	}

	var frames []Frame
	for _, p := range paths {
		m := rawmeta.Read(p)
		f, ok := pointing.FromMeta(m)
		flag := ""
		if !ok {
			flag = "  (no pointing: needs both the gravity vector and the compass)"
		}
		at := time.UnixMilli(m.TakenAtMs).UTC()
		fmt.Printf("%-16s  %s  ISO %-5d %7.3fs  %dx%d  ori %d  lat %8.4f lon %8.4f  az %6.1f alt %6.1f roll %6.1f%s\n",
			filepath.Base(p), at.Format("2006-01-02 15:04:05"), m.ISO, float64(m.ExposureMs)/1000,
			m.Width, m.Height, m.Orientation, m.LatDeg, m.LonDeg, f.AzDeg, f.AltDeg, f.RollDeg, flag)
		if ok {
			frames = append(frames, Frame{Path: p, Pointing: f, At: at})
		}
	}
	if len(frames) == 0 {
		t.Skip("nothing carried a usable pointing")
	}
	groups := Group(frames, DefaultOptions())
	fmt.Printf("\n%d pointing(s):\n", len(groups))
	for _, g := range groups {
		ra, dec, pa, ok := g.Center.Equatorial()
		sky := "  (no sky position: needs the site and the time)"
		if ok {
			sky = fmt.Sprintf("  RA %6.2f Dec %+6.2f  PA %6.1f", ra, dec, pa)
		}
		fmt.Printf("  %-6s %3d frames  %s -> %s  az %6.1f  alt %6.1f  spread %.2f deg%s\n",
			g.Label, len(g.Frames), g.Start.UTC().Format("15:04:05"), g.End.UTC().Format("15:04:05"),
			g.Center.AzDeg, g.Center.AltDeg, g.SpreadDeg, sky)
	}
}
