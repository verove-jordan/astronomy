package avf

import (
	"context"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/device"
)

// The driver's whole lifecycle, with no camera and no ffmpeg: the subprocess is injected, so what is
// under test is the framing, the warm-up, the staleness rule and the honesty of the controls.

// ffmpeg's real listing, captured from this machine. Keeping the actual text — prefixes, curly
// apostrophe, audio section and all — is the point: a parser tested against tidied-up input is a
// parser tested against something ffmpeg never emits.
const realListing = `[AVFoundation indev @ 0x865410140] AVFoundation video devices:
[AVFoundation indev @ 0x865410140] [0] FaceTime HD Camera
[AVFoundation indev @ 0x865410140] [1] Jordan’s iPhone Camera
[AVFoundation indev @ 0x865410140] [2] Jordan’s iPhone Desk View Camera
[AVFoundation indev @ 0x865410140] [3] Capture screen 0
[AVFoundation indev @ 0x865410140] AVFoundation audio devices:
[AVFoundation indev @ 0x865410140] [0] BlackHole 2ch
[AVFoundation indev @ 0x865410140] [1] MacBook Pro Microphone
[AVFoundation indev @ 0x865410140] [2] Microsoft Teams Audio
[AVFoundation indev @ 0x865410140] [3] Jordan’s iPhone Microphone
`

func TestParseDevices_ReadsTheVideoSectionOnly(t *testing.T) {
	devs := parseDevices(realListing)

	// The iPhone appears under BOTH headings. A parser that ignored them would offer a microphone as
	// a camera.
	require.Len(t, devs, 4)
	assert.Equal(t, Device{Index: 0, Name: "FaceTime HD Camera"}, devs[0])
	assert.Equal(t, Device{Index: 1, Name: "Jordan’s iPhone Camera"}, devs[1])
	assert.Equal(t, Device{Index: 3, Name: "Capture screen 0"}, devs[3])
	for _, d := range devs {
		assert.NotContains(t, strings.ToLower(d.Name), "microphone")
	}
}

func TestParseDevices_SurvivesNothingAtAll(t *testing.T) {
	assert.Empty(t, parseDevices(""))
	assert.Empty(t, parseDevices("some unrelated ffmpeg error\n"))
}

func TestCamera_Resolve_PicksTheDeviceAUserWouldMean(t *testing.T) {
	tests := []struct {
		name      string
		selector  string
		wantIndex int
		wantErr   bool
	}{
		{"a bare index pins one exactly", "1", 1, false},
		{"a name substring is what people remember", "iphone", 1, false},
		{"matching ignores case", "IPHONE", 1, false},
		{"the default selector finds a phone", "", 1, false},
		{"another camera by name", "facetime", 0, false},
		{"desk view only when asked for", "desk view", 2, false},
		{"an index that is not there", "9", 0, true},
		{"a name that is not there", "nikon", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New(tt.selector)
			c.lister = func(context.Context) ([]Device, error) { return parseDevices(realListing), nil }

			got, err := c.resolve(context.Background())
			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrNoDevice)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantIndex, got.Index)
		})
	}
}

func TestCamera_Resolve_NeverPicksDeskViewByAccident(t *testing.T) {
	// Continuity Camera publishes a second camera pointed at the desk rather than where the phone is
	// aimed. Silently choosing it would be baffling to debug from an image alone.
	c := New("iphone")
	c.lister = func(context.Context) ([]Device, error) { return parseDevices(realListing), nil }

	got, err := c.resolve(context.Background())
	require.NoError(t, err)
	assert.NotContains(t, strings.ToLower(got.Name), "desk view")
}

func TestCamera_FFmpegArgs_AskForWhatTheDeviceActuallyOffers(t *testing.T) {
	args := strings.Join(New("1", WithSize(320, 240)).ffmpegArgs(1), " ")

	// avfoundation does not offer yuv420p; it offers nv12. Letting ffmpeg guess produces a warning
	// and a format nobody chose.
	assert.Contains(t, args, "-pixel_format nv12")
	// A fixed output size is what makes the raw stream self-framing.
	assert.Contains(t, args, "scale=320:240")
	assert.Contains(t, args, "-f rawvideo -pix_fmt gray")
}

// plentyFrames is more frames than any test consumes, and few enough that the per-frame fill value
// (a byte) never wraps — so "this frame is newer than that one" stays a valid comparison.
const plentyFrames = 250

// fakeStream feeds the driver frames without a camera. Each frame is filled with a distinct value so
// a test can tell WHICH frame it received — the difference between "a frame arrived" and "the right
// frame arrived".
type fakeStream struct {
	r      *io.PipeReader
	w      *io.PipeWriter
	frame  int
	frames int
	size   int
}

func newFakeStream(t *testing.T, size, frames int) *fakeStream {
	t.Helper()
	r, w := io.Pipe()
	s := &fakeStream{r: r, w: w, frames: frames, size: size}
	go func() {
		defer w.Close()
		for i := 0; i < frames; i++ {
			buf := make([]byte, size)
			for j := range buf {
				buf[j] = byte(i)
			}
			if _, err := w.Write(buf); err != nil {
				return
			}
		}
	}()
	return s
}

func fakeCamera(t *testing.T, w, h, frames int) *Camera {
	t.Helper()
	c := New("1", WithSize(w, h))
	c.lister = func(context.Context) ([]Device, error) { return parseDevices(realListing), nil }
	stream := newFakeStream(t, w*h, frames)
	c.runner = func(context.Context, []string) (*exec.Cmd, io.ReadCloser, error) {
		return nil, stream.r, nil
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestCamera_Connect_ThrowsAwayTheBlackWarmUpFrames(t *testing.T) {
	// An iPhone returns black for about a second while auto-exposure engages. Paying that once at
	// connect, rather than per exposure, is the difference between a usable guide cadence and a
	// second of dead time on every frame.
	c := fakeCamera(t, 8, 8, plentyFrames)
	require.NoError(t, c.Connect(context.Background()))

	require.NoError(t, c.StartExposure(context.Background(), false))
	frame, err := c.Download(context.Background())
	require.NoError(t, err)
	// Frames 0..warmupFrames-1 were discarded. Connect itself consumes the first good one to prove a
	// frame arrives at all, so the assertion is on the property that matters — this came from after
	// the warm-up — rather than on an exact index, which is an implementation detail of that check.
	assert.GreaterOrEqual(t, frame.Pix[0], uint16(warmupFrames)<<8|uint16(warmupFrames),
		"the first frame handed out must be from AFTER the warm-up")
	assert.NotZero(t, frame.Pix[0], "a black warm-up frame must never reach a caller")
}

func TestCamera_Connect_FailsWhenTheStreamDiesDuringWarmUp(t *testing.T) {
	// A camera that goes away mid-warm-up must fail to connect rather than report itself ready and
	// then hand out nothing.
	c := fakeCamera(t, 8, 8, 3)
	err := c.Connect(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, device.ErrDriverUnavailable)
	assert.False(t, c.Connected())
}

func TestCamera_StartExposure_KeepsTheFreshestFrame(t *testing.T) {
	// Frames pile up in the pipe while nobody reads. Handing over the one at the FRONT would apply a
	// guide correction to where the sky was several seconds ago.
	c := fakeCamera(t, 4, 4, plentyFrames)
	require.NoError(t, c.Connect(context.Background()))

	// Let the producer run well ahead of any consumer, the way ffmpeg does at 30 fps while a guide
	// loop is busy correlating.
	require.Eventually(t, func() bool { return c.DroppedFrames() > 2 },
		3*time.Second, 10*time.Millisecond,
		"the reader must keep draining the stream rather than letting frames queue")

	require.NoError(t, c.StartExposure(context.Background(), false))
	frame, err := c.Download(context.Background())
	require.NoError(t, err)

	assert.Greater(t, frame.Pix[0], uint16(warmupFrames)<<8|uint16(warmupFrames),
		"a stale queued frame must be skipped in favour of the newest one")
	assert.Positive(t, c.DroppedFrames(), "and the skipped frames must be counted, not hidden")
}

func TestCamera_StartExposure_NeverReturnsTheSamePictureTwice(t *testing.T) {
	// Handing back "the latest frame" would let two exposures in quick succession return the same
	// picture, and a guide loop would then measure a drift of exactly zero and correct nothing.
	c := fakeCamera(t, 4, 4, plentyFrames)
	require.NoError(t, c.Connect(context.Background()))

	require.NoError(t, c.StartExposure(context.Background(), false))
	first, err := c.Download(context.Background())
	require.NoError(t, err)

	require.NoError(t, c.StartExposure(context.Background(), false))
	second, err := c.Download(context.Background())
	require.NoError(t, err)

	assert.NotEqual(t, first.Pix[0], second.Pix[0])
}

func TestCamera_Download_RefusesWithoutAnExposure(t *testing.T) {
	c := fakeCamera(t, 8, 8, plentyFrames)
	require.NoError(t, c.Connect(context.Background()))

	_, err := c.Download(context.Background())
	require.Error(t, err, "downloading a frame nobody asked for would hand back the previous one")
}

func TestCamera_ExposureState_FollowsTheLifecycle(t *testing.T) {
	c := fakeCamera(t, 8, 8, plentyFrames)
	require.NoError(t, c.Connect(context.Background()))

	st, err := c.ExposureState()
	require.NoError(t, err)
	assert.Equal(t, device.ExposureIdle, st)

	require.NoError(t, c.StartExposure(context.Background(), false))
	st, _ = c.ExposureState()
	assert.Equal(t, device.ExposureSuccess, st)

	_, err = c.Download(context.Background())
	require.NoError(t, err)
	st, _ = c.ExposureState()
	assert.Equal(t, device.ExposureIdle, st)
}

func TestCamera_SetControl_RefusesWhatThePhoneWillNotHonour(t *testing.T) {
	c := fakeCamera(t, 8, 8, plentyFrames)
	require.NoError(t, c.Connect(context.Background()))

	// Accepting a gain the sensor never uses would write a false gain into every FITS header, and
	// nobody could interpret the data afterwards.
	err := c.SetControl("gain", 139, false)
	require.Error(t, err)
	assert.ErrorIs(t, err, device.ErrUnsupported)

	require.NoError(t, c.SetControl("exposure", 5000, false))
	ctrl, ok := c.Control("exposure")
	require.True(t, ok)
	assert.Equal(t, int64(5000), ctrl.Value)
	assert.Contains(t, ctrl.Description, "NOT an integration time")

	gain, ok := c.Control("gain")
	require.True(t, ok)
	assert.False(t, gain.Writable, "an unsettable control must say so rather than look settable")
}

func TestCamera_SetROI_RefusesACropItCannotDeliver(t *testing.T) {
	c := fakeCamera(t, 64, 48, plentyFrames)
	require.NoError(t, c.Connect(context.Background()))

	_, err := c.SetROI(device.ROI{Width: 32, Height: 24})
	require.Error(t, err, "returning the full frame after being asked for a crop misplaces every measured position")
	assert.ErrorIs(t, err, device.ErrUnsupported)

	roi, err := c.SetROI(device.ROI{Width: 64, Height: 48})
	require.NoError(t, err)
	assert.Equal(t, 64, roi.Width)
}

func TestCamera_Caps_DoesNotInventAPixelSize(t *testing.T) {
	c := fakeCamera(t, 8, 8, plentyFrames)
	require.NoError(t, c.Connect(context.Background()))

	caps := c.Caps()
	// ffmpeg has already scaled the frame, so any pixel pitch would be fiction — and a plate-solve
	// would trust it.
	assert.Zero(t, caps.PixelSizeUm)
	assert.Equal(t, 8, caps.BitDepth)
	assert.False(t, caps.HasCooler)
	assert.Contains(t, caps.Name, "iPhone")
}

func TestExpandTo16_KeepsWhiteAtFullScale(t *testing.T) {
	got := expandTo16([]byte{0x00, 0x7F, 0xFF})
	// A plain v<<8 would cap white at 0xFF00, and every saturation test downstream — flat clipping,
	// star-core rejection — would then never fire.
	assert.Equal(t, []uint16{0x0000, 0x7F7F, 0xFFFF}, got)
}

func TestCamera_Close_IsSafeTwice(t *testing.T) {
	c := fakeCamera(t, 8, 8, plentyFrames)
	require.NoError(t, c.Connect(context.Background()))
	require.NoError(t, c.Close())
	require.NoError(t, c.Close())
	assert.False(t, c.Connected())
}
