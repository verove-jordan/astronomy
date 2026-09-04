package devsrv

import (
	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/config"
	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/device/avf"
	"github.com/verove-jordan/astronomy/internal/device/sim"
)

// registerTestProbe adds a driver for the duration of one test. probes is package state by design —
// real drivers register themselves from init() — so the test puts it back exactly as it was.
func registerTestProbe(t *testing.T, p hardwareProbe) {
	t.Helper()
	before := probes
	probes = append(append([]hardwareProbe(nil), probes...), p)
	t.Cleanup(func() { probes = before })
}

// slowConnectCamera is a camera that takes its time to answer, which is what real ones do: an iPhone
// spends twenty seconds warming up before it hands over a frame that is not black.
type slowConnectCamera struct {
	device.Camera
	delay   time.Duration
	started chan struct{}
}

func (c *slowConnectCamera) Connect(ctx context.Context) error {
	close(c.started)
	select {
	case <-time.After(c.delay):
		return c.Camera.Connect(ctx)
	case <-ctx.Done():
		return ctx.Err()
	}
}

// The liveness contract: whether this process is alive must never depend on what the hardware is
// busy doing.
//
// This is the defect the whole capture page rested on. /health held the one device mutex, and so did
// every connect — so attaching a camera made the server unanswerable for as long as that camera took
// to open. The engine's probe gave up, the UI concluded the device server was not running, blanked
// the camera, wheel and mount panels and told the user to start a process that was already running.
func TestServer_HealthAnswersWhileACameraIsConnecting(t *testing.T) {
	started := make(chan struct{})
	world := sim.NewWorld(sim.Config{FocalMM: 740, PixelUm: 3.8, SensorW: 32, SensorH: 32, ApertureMM: 100})
	registerTestProbe(t, hardwareProbe{
		name: "slowcam", kind: device.KindCamera,
		probe: func() (string, error) { return "a camera that takes its time", nil },
		openCamera: func(string) (device.Camera, error) {
			return &slowConnectCamera{Camera: sim.NewCamera(world), delay: 3 * time.Second, started: started}, nil
		},
	})

	ts := testServer(t)
	// Warm the inventory, so what the assertion below measures is the lock and not the first probe.
	_, _ = get(t, ts, "/health")

	go func() {
		resp, err := http.Post(ts.URL+"/camera/connect", "application/json",
			strings.NewReader(`{"driver":"slowcam"}`))
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started

	for _, path := range []string{"/health", "/devices", "/wheel", "/mount"} {
		start := time.Now()
		resp, err := http.Get(ts.URL + path)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Less(t, time.Since(start), time.Second,
			"%s must answer while a camera is connecting — a slow device is not a dead server", path)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	}
}

// A connected device's driver is left alone. Enumerating a ZWO camera from an HTTP goroutine while
// another goroutine is mid-exposure is a concurrent vendor-SDK call that can take the process down,
// and there is nothing to learn from it: we know that camera is there, we are holding it.
func TestInventory_DoesNotReprobeADriverWhoseDeviceIsInUse(t *testing.T) {
	var calls atomic.Int32
	registerTestProbe(t, hardwareProbe{
		name: "countcam", kind: device.KindCamera, disturbs: true,
		probe: func() (string, error) { calls.Add(1); return "counted", nil },
		openCamera: func(string) (device.Camera, error) {
			return sim.NewCamera(sim.NewWorld(sim.Config{FocalMM: 740, PixelUm: 3.8, SensorW: 32, SensorH: 32})), nil
		},
	})

	srv := New(&config.Config{
		FocalLenMM: 740, PixelSizeUm: 3.8, SensorWpx: 32, SensorHpx: 32, DeviceAddr: "127.0.0.1:0",
	})
	t.Cleanup(srv.Close)

	srv.inv.get(firstProbeBudget) // the opening probe
	require.Equal(t, int32(1), calls.Load())

	cam, err := srv.openCamera(DriverSim, "")
	require.NoError(t, err)
	require.NoError(t, cam.Connect(context.Background()))
	srv.attachCamera(cam) // invalidates the inventory

	srv.inv.get(firstProbeBudget)
	settle(t, srv.inv)
	assert.Equal(t, int32(1), calls.Load(),
		"the camera driver must not be re-probed while its camera is connected")

	srv.detachCamera()
	srv.inv.get(firstProbeBudget)
	settle(t, srv.inv)
	assert.Equal(t, int32(2), calls.Load(), "and must be probed again once it is released")
}

// probeAll keeps the previous entries for a busy kind rather than dropping the device from the
// picker: not re-enumerating must not read as "the camera went away".
func TestProbeAll_KeepsTheEntriesOfABusyKind(t *testing.T) {
	prevDrivers := []DriverStatus{
		{Name: DriverSim, Kind: "all", Available: true},
		{Name: DriverASI, Kind: device.KindCamera, Available: true, Detail: "SDK loaded; 1 camera(s) connected"},
		{Name: DriverAVF, Kind: device.KindCamera, Available: true, Detail: "capture devices: [0] Jordan's iPhone Camera"},
	}
	prevDevices := []device.Info{
		{ID: "sim-camera", Name: "Simulated ASI1600MM Pro", Driver: DriverSim, Kind: device.KindCamera},
		{ID: "asi-0", Name: "ZWO ASI1600MM Pro", Driver: DriverASI, Kind: device.KindCamera},
	}

	drivers, devices := probeAll(map[string]bool{device.KindCamera: true}, prevDrivers, prevDevices)

	asi, ok := findDriver(drivers, DriverASI)
	require.True(t, ok)
	assert.Equal(t, "SDK loaded; 1 camera(s) connected", asi.Detail, "reused, not re-probed")

	var names []string
	for _, d := range devices {
		if d.Kind == device.KindCamera {
			names = append(names, d.ID)
		}
	}
	assert.Equal(t, []string{"sim-camera", "asi-0"}, names,
		"the camera in use stays in the picker while its driver is left alone")
}

// The iPhone must be reachable from the picker, and must be the camera offered first.
//
// It was not. discover() enumerated ZWO cameras, ZWO wheels and serial mounts but never the Mac's
// own capture devices, so the panel — which prefers the first DISCOVERED device of a kind and only
// falls back to the driver list, whose first entry is the simulator — could not reach a phone at
// all. The avf driver reported itself available and listed the iPhone in its own detail string the
// whole time.
func TestAVFInfos_OffersThePhoneFirstAndDropsWhatIsNotACamera(t *testing.T) {
	got := avfInfos([]avf.Device{
		{Index: 0, Name: "FaceTime HD Camera"},
		{Index: 1, Name: "Jordan’s iPhone Camera"},
		{Index: 2, Name: "Jordan’s iPhone Desk View Camera"},
		{Index: 3, Name: "Capture screen 0"},
	})

	var ids []string
	for _, d := range got {
		require.Equal(t, DriverAVF, d.Driver)
		require.Equal(t, device.KindCamera, d.Kind)
		ids = append(ids, d.ID)
	}
	assert.Equal(t, []string{"Jordan’s iPhone Camera", "FaceTime HD Camera"}, ids,
		"the phone leads; Desk View looks at the desk and a screen is not a camera")
}

// settle waits for any in-flight inventory refresh to finish.
func settle(t *testing.T, inv *inventory) {
	t.Helper()
	require.Eventually(t, func() bool {
		inv.mu.Lock()
		defer inv.mu.Unlock()
		return !inv.refreshing
	}, 10*time.Second, 5*time.Millisecond)
}

// The opposite rule, and the reason it is a per-driver flag rather than "anything in use".
//
// The NexStar probe only reads the USB tree and /dev — it opens nothing — so it must keep running
// while a mount is attached. It is exactly then that the answer matters: a hand controller whose
// adapter has fallen off the bus still showed as "candidate: cu.usbserial-11120" from the cache,
// which sends somebody debugging their cable off to read software instead.
func TestInventory_KeepsProbingADriverThatDisturbsNothing(t *testing.T) {
	var calls atomic.Int32
	registerTestProbe(t, hardwareProbe{
		name: "countmount", kind: device.KindMount, // disturbs stays false
		probe:     func() (string, error) { calls.Add(1); return "counted", nil },
		openMount: func(string) (device.Mount, error) { return nil, nil },
	})

	srv := New(&config.Config{
		FocalLenMM: 740, PixelSizeUm: 3.8, SensorWpx: 32, SensorHpx: 32, DeviceAddr: "127.0.0.1:0",
	})
	t.Cleanup(srv.Close)

	srv.inv.get(firstProbeBudget)
	require.Equal(t, int32(1), calls.Load())

	srv.mountOn.Store(true) // a mount is attached
	srv.inv.invalidate()
	srv.inv.get(firstProbeBudget)
	settle(t, srv.inv)

	assert.Equal(t, int32(2), calls.Load(),
		"a probe that opens nothing must keep telling the truth while a mount is attached")
}
