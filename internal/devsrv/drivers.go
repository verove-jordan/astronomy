package devsrv

import (
	"errors"
	"fmt"

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

// DriverStatus is one driver's availability, as reported to the UI.
type DriverStatus struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Available bool   `json:"available"`
	Detail    string `json:"detail,omitempty"` // SDK version, port, or why it is unavailable
}

// hardwareProbe lets the real drivers (added in later phases) advertise themselves without this
// file importing them; nil entries simply do not appear.
type hardwareProbe struct {
	name, kind string
	probe      func() (detail string, err error)
	openCamera func() (device.Camera, error)
	openWheel  func() (device.FilterWheel, error)
	openMount  func() (device.Mount, error)
}

var probes []hardwareProbe

// registerProbe is called from a driver's init() once it is implemented.
func registerProbe(p hardwareProbe) { probes = append(probes, p) }

// cameraDevice selects which AVFoundation capture device the avf driver opens: an ffmpeg index, or a
// substring of the device's name. Empty means "find a phone".
var cameraDevice string

// mountPort is the serial device the NexStar driver will open. It is set by the connect request
// (the UI lists the candidates), defaulting to whatever looks like a USB-serial adapter.
var mountPort string

func init() {
	// The ZWO drivers bind their SDK at runtime, so registering them costs nothing on a machine with
	// no ZWO hardware or no SDK: the probe simply reports why, and the UI offers the simulator.
	registerProbe(hardwareProbe{
		name: DriverASI, kind: device.KindCamera,
		probe:      asi.Available,
		openCamera: func() (device.Camera, error) { return asi.New(), nil },
	})
	registerProbe(hardwareProbe{
		name: DriverEFW, kind: device.KindWheel,
		probe:     efw.Available,
		openWheel: func() (device.FilterWheel, error) { return efw.New(), nil },
	})
	// The Mac's own capture devices, which on a machine with an iPhone nearby means Continuity Camera.
	// It costs nothing to register: the probe reports what it can see, and a machine with no camera
	// permission or no ffmpeg simply says so.
	registerProbe(hardwareProbe{
		name: DriverAVF, kind: device.KindCamera,
		probe:      avf.Probe,
		openCamera: func() (device.Camera, error) { return avf.New(cameraDevice), nil },
	})
	registerProbe(hardwareProbe{
		name: DriverNexStar, kind: device.KindMount,
		probe: nexstar.Probe,
		openMount: func() (device.Mount, error) {
			path := mountPort
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

// driverReport lists every driver and whether it can be used right now.
func driverReport() []DriverStatus {
	out := []DriverStatus{
		{Name: DriverSim, Kind: "all", Available: true, Detail: "built-in simulator"},
	}
	for _, p := range probes {
		st := DriverStatus{Name: p.name, Kind: p.kind}
		detail, err := p.probe()
		if err != nil {
			st.Detail = err.Error()
		} else {
			st.Available = true
			st.Detail = detail
		}
		out = append(out, st)
	}
	return out
}

// discover lists the devices that could be connected. The simulator always offers one of each, so
// the whole capture UI is exercisable with nothing plugged in; real hardware is appended when a
// driver can actually see it.
//
// Every kind must be enumerated here, not just the mount. The UI picks the first DISCOVERED device
// of a kind and only falls back to the driver list — which starts with the simulator — when
// discovery came back empty. So a kind missing from this function is a kind that silently connects
// the simulator with real hardware plugged in, which is exactly how a live ASI camera ended up
// showing as "Simulated ASI1600MM Pro".
func discover() []device.Info {
	out := []device.Info{
		{ID: "sim-camera", Name: "Simulated ASI1600MM Pro", Driver: DriverSim, Kind: device.KindCamera},
		{ID: "sim-wheel", Name: "Simulated EFW 5×36mm", Driver: DriverSim, Kind: device.KindWheel},
		{ID: "sim-mount", Name: "Simulated Celestron AVX", Driver: DriverSim, Kind: device.KindMount},
	}
	out = append(out, asiCameras()...)
	out = append(out, efwWheels()...)
	out = append(out, nexstarMounts()...)
	return out
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

// openCamera resolves a driver name to a camera.
func (s *Server) openCamera(driver string) (device.Camera, error) {
	switch driver {
	case "", DriverSim:
		return sim.NewCamera(s.simWorld()), nil
	}
	for _, p := range probes {
		if p.name == driver && p.openCamera != nil {
			return p.openCamera()
		}
	}
	return nil, fmt.Errorf("%w: no camera driver %q", device.ErrDriverUnavailable, driver)
}

func (s *Server) openWheel(driver string) (device.FilterWheel, error) {
	switch driver {
	case "", DriverSim:
		return sim.NewWheel(s.simWorld()), nil
	}
	for _, p := range probes {
		if p.name == driver && p.openWheel != nil {
			return p.openWheel()
		}
	}
	return nil, fmt.Errorf("%w: no filter-wheel driver %q", device.ErrDriverUnavailable, driver)
}

func (s *Server) openMount(driver string) (device.Mount, error) {
	switch driver {
	case "", DriverSim:
		return sim.NewMount(s.simWorld()), nil
	}
	for _, p := range probes {
		if p.name == driver && p.openMount != nil {
			return p.openMount()
		}
	}
	return nil, fmt.Errorf("%w: no mount driver %q", device.ErrDriverUnavailable, driver)
}

// errorsIs is a tiny indirection so the handlers read cleanly.
func errorsIs(err, target error) bool { return errors.Is(err, target) }
