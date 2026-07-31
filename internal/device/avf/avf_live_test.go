package avf

// A manual probe of the REAL capture devices on this Mac — in practice an iPhone over Continuity
// Camera. It never runs in a normal suite.
//
//	ASTRO_TEST_CAMERA_LIVE=1 go test ./internal/device/avf -run TestLiveCamera -v
//
// Set ASTRO_TEST_CAMERA_DEVICE to pick one ("1", or a name substring like "facetime"); the default
// looks for a phone. Set ASTRO_TEST_CAMERA_DUMP=/path/frame.pgm to write a frame out and look at it.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLiveCameraList(t *testing.T) {
	if os.Getenv("ASTRO_TEST_CAMERA_LIVE") == "" {
		t.Skip("set ASTRO_TEST_CAMERA_LIVE=1 to look at this Mac's real capture devices")
	}
	devs, err := List(context.Background())
	require.NoError(t, err)
	for _, d := range devs {
		t.Logf("[%d] %s", d.Index, d.Name)
	}
	require.NotEmpty(t, devs, "no capture devices — is Camera permission granted in Privacy & Security?")
}

func TestLiveCamera(t *testing.T) {
	if os.Getenv("ASTRO_TEST_CAMERA_LIVE") == "" {
		t.Skip("set ASTRO_TEST_CAMERA_LIVE=1 to capture from a real camera")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	cam := New(os.Getenv("ASTRO_TEST_CAMERA_DEVICE"))
	require.NoError(t, cam.Connect(ctx))
	t.Cleanup(func() { _ = cam.Close() })

	caps := cam.Caps()
	t.Logf("connected to %q — %dx%d, %d-bit", caps.Name, caps.MaxWidth, caps.MaxHeight, caps.BitDepth)

	var frames []*frameStats
	var gens []uint64
	for i := 0; i < 3; i++ {
		require.NoErrorf(t, cam.StartExposure(ctx, false), "exposure %d", i+1)
		f, err := cam.Download(ctx)
		require.NoErrorf(t, err, "download %d", i+1)
		require.Len(t, f.Pix, caps.MaxWidth*caps.MaxHeight)

		st := statsOf(f.Pix)
		frames = append(frames, st)
		gens = append(gens, cam.lastGen())
		t.Logf("frame %d: gen %d  mean %d  min %d  max %d", i+1, gens[i], st.mean, st.min, st.max)
	}

	// The warm-up is the whole reason Connect is slow; if it were skipped these would be black, which
	// is exactly what the first attempt at this driver produced.
	assert.Positive(t, frames[0].mean, "a black frame means the warm-up was not paid")
	assert.Greater(t, frames[0].max, uint16(0x1000), "the frame has no signal in it at all")

	// Each exposure must come from a DIFFERENT frame off the stream. That is a property of the driver
	// and is what stops a guide loop measuring the same picture twice and correcting nothing.
	assert.Greater(t, gens[1], gens[0], "the second exposure reused the first frame")
	assert.Greater(t, gens[2], gens[1], "the third exposure reused the second frame")

	// Whether the PIXELS differ is a property of the scene, not of this code, so it is reported
	// rather than asserted: an iPhone pointed at a motionless desk applies enough temporal noise
	// reduction to emit genuinely identical frames, and failing on that would be a test that only
	// passes when somebody happens to be waving at the camera.
	if frames[0].sum == frames[1].sum {
		t.Log("consecutive frames are identical — expected for a static scene; move the phone to see them change")
	}

	if path := os.Getenv("ASTRO_TEST_CAMERA_DUMP"); path != "" {
		require.NoError(t, cam.StartExposure(ctx, false))
		f, err := cam.Download(ctx)
		require.NoError(t, err)
		require.NoError(t, writePGM(path, f.Width, f.Height, f.Pix))
		t.Logf("wrote %s", path)
	}
}

type frameStats struct {
	mean, min, max uint16
	sum            uint64
}

func statsOf(pix []uint16) *frameStats {
	st := &frameStats{min: 0xFFFF}
	for _, v := range pix {
		st.sum += uint64(v)
		if v < st.min {
			st.min = v
		}
		if v > st.max {
			st.max = v
		}
	}
	st.mean = uint16(st.sum / uint64(len(pix)))
	return st
}

// writePGM dumps a frame in the simplest format anything can open, so a human can check the driver is
// looking at what they think it is looking at.
func writePGM(path string, w, h int, pix []uint16) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "P5\n%d %d\n255\n", w, h); err != nil {
		return err
	}
	b := make([]byte, len(pix))
	for i, v := range pix {
		b[i] = byte(v >> 8)
	}
	_, err = f.Write(b)
	return err
}
