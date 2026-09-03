package pipeline

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

// TestZZGrouping prints how a real session segments, so a panel count can be checked against what
// was actually shot. Gated: it reads a specific capture folder and runs the pixel-stats classifier
// over every frame.
//
//	ASTRO_PANO_DIR=input/Iphone_10_08_2026 go test ./internal/pipeline/ -run TestZZGrouping -v
func TestZZGrouping(t *testing.T) {
	dir := os.Getenv("ASTRO_PANO_DIR")
	if dir == "" {
		t.Skip("set ASTRO_PANO_DIR to a capture folder")
	}
	frames, err := inspect.ListRawFramesMany([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	lights, darks, flats, bias := splitCalibrationFrames(context.Background(), frames)
	fmt.Printf("%d raw frames: %d lights, %d darks, %d flats, %d bias\n",
		len(frames), len(lights), len(darks), len(flats), len(bias))

	panels, warns := groupPanels(lights, nil)
	for _, w := range warns {
		fmt.Println("warning:", w)
	}
	for _, p := range panels {
		fmt.Printf("%-4s %3d frames  az %6.1f  alt %5.1f  roll %6.1f  spread %.2f deg  %s\n",
			p.label, len(p.frames), p.center.AzDeg, p.center.AltDeg, p.center.RollDeg, p.spread,
			shortName(p.frames[0])+" .. "+shortName(p.frames[len(p.frames)-1]))
	}
	fmt.Printf("%d panels\n", len(panels))
}

func shortName(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' {
			return path[i+1:]
		}
	}
	return path
}
