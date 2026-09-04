package devsrv

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/device/asi"
	"github.com/verove-jordan/astronomy/internal/device/avf"
	"github.com/verove-jordan/astronomy/internal/device/efw"
	"github.com/verove-jordan/astronomy/internal/device/nexstar"
	"github.com/verove-jordan/astronomy/internal/device/sim"
)

// Driver names. "sim" is always available; the hardware drivers register themselves when their
// vendor library can actually be loaded, so a build with no SDK present simply offers fewer.
const (
	DriverSim     = "sim"
	DriverASI     = "asi"
	DriverEFW     = "efw"
	DriverNexStar = "nexstar"
	DriverAVF     = "avf"
)

// avfListTimeout bounds the ffmpeg subprocess that enumerates AVFoundation devices.
const avfListTimeout = 10 * time.Second

// DriverStatus is one driver's availability, as reported to the UI.
type DriverStatus struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Available bool   `json:"available"`
	Detail    string `json:"detail,omitempty"` // SDK version, port, or why it is unavailable
}

// hardwareProbe lets the real drivers advertise themselves without this file importing them; nil
// entries simply do not appear.
//
// Each opener takes the SELECTOR the caller chose — the id of a discovered device, as the driver
// understands it: a serial path for the mount, an AVFoundation device name for a phone. It is a
// parameter rather than a package variable because it arrives on an HTTP request: two browser tabs
// connecting different devices used to race on one global, and the loser silently opened whatever
// the winner had asked for.
type hardwareProbe struct {
	name, kind string
	probe      func() (detail string, err error)
	// disturbs marks a driver whose probe must not run while its device is working: the ZWO ones
	// call the vendor SDK over USB, and the AVFoundation one opens the capture device to list it.
	// The NexStar probe only reads the USB tree and /dev, so it stays live — and it needs to, because
	// a hand controller whose adapter has fallen off the bus is exactly when the honest answer
	// matters and a cached "candidate: cu.usbserial-11120" sends the user looking at software.
	disturbs   bool
	openCamera func(sel string) (device.Camera, error)
	openWheel  func(sel string) (device.FilterWheel, error)
	openMount  func(sel string) (device.Mount, error)
}

var probes []hardwareProbe

// registerProbe is called from init() once a driver is implemented.
func registerProbe(p hardwareProbe) { probes = append(probes, p) }

func init() {
	// The ZWO drivers bind their SDK at runtime, so registering them costs nothing on a machine with
	// no ZWO hardware or no SDK: the probe simply reports why, and the UI offers the simulator.
	registerProbe(hardwareProbe{
		name: DriverASI, disturbs: true, kind: device.KindCamera,
		probe:      asi.Available,
		openCamera: func(string) (device.Camera, error) { return asi.New(), nil },
	})
	registerProbe(hardwareProbe{
		name: DriverEFW, disturbs: true, kind: device.KindWheel,
		probe:     efw.Available,
		openWheel: func(string) (device.FilterWheel, error) { return efw.New(), nil },
	})
	// The Mac's own capture devices, which on a machine with an iPhone nearby means Continuity
	// Camera. It costs nothing to register: the probe reports what it can see, and a machine with no
	// camera permission or no ffmpeg simply says so.
	registerProbe(hardwareProbe{
		name: DriverAVF, disturbs: true, kind: device.KindCamera,
		probe:      avf.Probe,
		openCamera: func(sel string) (device.Camera, error) { return avf.New(sel), nil },
	})
	registerProbe(hardwareProbe{
		name: DriverNexStar, kind: device.KindMount,
		probe: nexstar.Probe,
		openMount: func(sel string) (device.Mount, error) {
			path := sel
			if path == "" {
				path = nexstar.DefaultPort()
			}
			if path == "" {
				return nil, fmt.Errorf(
					"%w: no serial port found — connect the hand controller's USB cable",
					device.ErrDriverUnavailable)
			}
			return nexstar.New(path, nil), nil
		},
	})
}

// simDevices are the simulated devices always on offer, so the whole capture UI is exercisable with
// nothing plugged in.
func simDevices() []device.Info {
	return []device.Info{
		{ID: "sim-camera", Name: "Simulated ASI1600MM Pro", Driver: DriverSim, Kind: device.KindCamera},
		{ID: "sim-wheel", Name: "Simulated EFW 5×36mm", Driver: DriverSim, Kind: device.KindWheel},
		{ID: "sim-mount", Name: "Simulated Celestron AVX", Driver: DriverSim, Kind: device.KindMount},
	}
}

// enumerate lists the real devices of one kind.
//
// EVERY kind must be enumerated here, and every driver within a kind. The UI picks the first
// DISCOVERED device of a kind and only falls back to the driver list — which starts with the
// simulator — when discovery came back empty. So a driver missing from this function is a driver
// that silently connects the simulator with real hardware plugged in. That is how a live ASI camera
// once showed as "Simulated ASI1600MM Pro", and then, one driver later, how an iPhone on Continuity
// Camera could be listed in the avf driver's own detail string and still be unreachable from the
// panel.
//
// Cameras are ordered astro-camera first: a ZWO on the telescope outranks the Mac's built-in webcam
// as the thing the user meant.
func enumerate(kind string) []device.Info {
	switch kind {
	case device.KindCamera:
		return append(asiCameras(), avfCameras()...)
	case device.KindWheel:
		return efwWheels()
	case device.KindMount:
		return nexstarMounts()
	}
	return nil
}

// asiCameras lists the ZWO cameras the SDK can see. A driver whose library will not load simply
// contributes nothing — the simulator is still offered.
func asiCameras() []device.Info {
	cams, err := asi.List()
	if err != nil {
		return nil
	}
	out := make([]device.Info, 0, len(cams))
	for _, c := range cams {
		name := c.Name
		if name == "" {
			name = "ZWO camera"
		}
		out = append(out, device.Info{
			ID: fmt.Sprintf("asi-%d", c.ID), Name: name,
			Driver: DriverASI, Kind: device.KindCamera,
		})
	}
	return out
}

// avfCameras lists this Mac's AVFoundation capture devices — in practice an iPhone over Continuity
// Camera, which is the reason the driver exists.
//
// The id is the device's NAME rather than its ffmpeg index, because that is what survives a
// re-enumeration: indexes shift as a phone comes and goes, and the connect request that arrives a
// second later would then open a different camera. avf.Camera resolves a non-numeric selector as a
// name, so the full name matches itself exactly.
func avfCameras() []device.Info {
	ctx, cancel := context.WithTimeout(context.Background(), avfListTimeout)
	defer cancel()
	devs, err := avf.List(ctx)
	if err != nil {
		return nil
	}
	return avfInfos(devs)
}

// avfInfos turns ffmpeg's capture-device listing into the picker's entries: phones first, because a
// phone on Continuity Camera is what this driver is for and the Mac's own webcam is not what someone
// pointing a telescope meant.
func avfInfos(devs []avf.Device) []device.Info {
	var phones, others []device.Info
	for _, d := range devs {
		if !isCapturableCamera(d.Name) {
			continue
		}
		info := device.Info{ID: d.Name, Name: d.Name, Driver: DriverAVF, Kind: device.KindCamera}
		if isPhone(d.Name) {
			phones = append(phones, info)
			continue
		}
		others = append(others, info)
	}
	return append(phones, others...)
}

// isCapturableCamera drops the AVFoundation entries that are not a camera pointed at anything: a
// screen recorder, and Continuity's Desk View, which looks at the desk rather than where the phone
// is aimed. Offering either as the default capture device would be baffling.
func isCapturableCamera(name string) bool {
	lower := strings.ToLower(name)
	return !strings.Contains(lower, "capture screen") && !strings.Contains(lower, "desk view")
}

func isPhone(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "iphone") || strings.Contains(lower, "ipad")
}

// efwWheels lists the ZWO filter wheels the SDK can see. Discovery cannot read the model name
// without opening the wheel (see efw.List), so the name is the generic one until it connects.
func efwWheels() []device.Info {
	wheels, err := efw.List()
	if err != nil {
		return nil
	}
	out := make([]device.Info, 0, len(wheels))
	for _, wh := range wheels {
		out = append(out, device.Info{
			ID: fmt.Sprintf("efw-%d", wh.ID), Name: "ZWO EFW",
			Driver: DriverEFW, Kind: device.KindWheel,
		})
	}
	return out
}

// nexstarMounts lists the serial ports that look like a hand controller.
func nexstarMounts() []device.Info {
	var out []device.Info
	for _, port := range nexstar.ListPorts() {
		if !port.Likely {
			continue // a Bluetooth channel is not a telescope
		}
		out = append(out, device.Info{
			ID: port.Path, Name: "NexStar mount on " + port.Label,
			Driver: DriverNexStar, Kind: device.KindMount,
		})
	}
	return out
}

// SerialPorts lists the candidate serial devices, so the UI can offer a choice rather than guessing
// which adapter is the telescope.
func SerialPorts() []nexstar.PortInfo { return nexstar.ListPorts() }

// openCamera resolves a driver name and a chosen device to a camera.
func (s *Server) openCamera(driver, sel string) (device.Camera, error) {
	switch driver {
	case "", DriverSim:
		return sim.NewCamera(s.simWorld()), nil
	}
	for _, p := range probes {
		if p.name == driver && p.openCamera != nil {
			return p.openCamera(sel)
		}
	}
	return nil, fmt.Errorf("%w: no camera driver %q", device.ErrDriverUnavailable, driver)
}

func (s *Server) openWheel(driver, sel string) (device.FilterWheel, error) {
	switch driver {
	case "", DriverSim:
		return sim.NewWheel(s.simWorld()), nil
	}
	for _, p := range probes {
		if p.name == driver && p.openWheel != nil {
			return p.openWheel(sel)
		}
	}
	return nil, fmt.Errorf("%w: no filter-wheel driver %q", device.ErrDriverUnavailable, driver)
}

func (s *Server) openMount(driver, sel string) (device.Mount, error) {
	switch driver {
	case "", DriverSim:
		return sim.NewMount(s.simWorld()), nil
	}
	for _, p := range probes {
		if p.name == driver && p.openMount != nil {
			return p.openMount(sel)
		}
	}
	return nil, fmt.Errorf("%w: no mount driver %q", device.ErrDriverUnavailable, driver)
}

// errorsIs is a tiny indirection so the handlers read cleanly.
func errorsIs(err, target error) bool { return errors.Is(err, target) }
