package asi

import (
	"fmt"
	"sort"
	"strings"

	"github.com/verove-jordan/astronomy/internal/device"
)

// Working out which SDK control is which — from the camera, not from a table.
//
// ASICamera2.h assigns each control a number (ASI_GAIN = 0, ASI_EXPOSURE = 1, …). Hardcoding those
// numbers is the obvious approach and it is a trap: the enum has grown between SDK versions, the
// header is not shipped with the binary library, and a number that is off by one does not fail —
// it silently drives the WRONG control. Asking for the cooler and getting image flip, or reading
// "temperature" and getting the auto-gain ceiling, is the kind of bug that only shows up at 2am
// with a camera attached.
//
// So nothing here trusts a number. ASIGetControlCaps reports, for every control the camera actually
// has, both a human name ("Exposure", "CoolerOn", "BandWidth") and its numeric id. The driver reads
// that list at connect time and builds the mapping from it, so the ids come from the device itself
// and stay correct across SDK versions.

// controlAliases maps the SDK's reported control names onto this engine's canonical names. Keys are
// normalised (lower-case, no spaces/underscores) because ZWO's spelling varies between models —
// "CoolerOn" / "Cooler On", "BandWidth" / "BandwidthOverload".
//
// Several spellings map to one canonical name on purpose: the alternative is a control silently
// disappearing from the UI because a camera called it something slightly different.
var controlAliases = map[string]string{
	"gain":        device.ControlGain,
	"exposure":    device.ControlExposure,
	"offset":      device.ControlOffset,
	"brightness":  device.ControlOffset, // older SDKs called offset "Brightness"
	"temperature": device.ControlTemperature,
	"targettemp":  device.ControlTargetTemp,
	"cooleron":    device.ControlCoolerOn,
	// A real ASI1600MM Pro reports "CoolPowerPerc" — no "er" on "Cool". Guessing the spelling from
	// the header's enum name (ASI_COOLER_POWER_PERC) sent it to the unmapped x_ bucket, so cooler
	// power never reached the cooling panel. Every plausible spelling is listed rather than assumed.
	"coolpowerperc":           device.ControlCoolerPower,
	"coolerpowerperc":         device.ControlCoolerPower,
	"coolpower":               device.ControlCoolerPower,
	"coolerpower":             device.ControlCoolerPower,
	"bandwidth":               device.ControlUSBBandwidth,
	"bandwidthoverload":       device.ControlUSBBandwidth,
	"usbtraffic":              device.ControlUSBBandwidth,
	"highspeedmode":           device.ControlHighSpeed,
	"monobin":                 device.ControlMonoBin,
	"hardwarebin":             device.ControlHardwareBin,
	"fanon":                   device.ControlFanOn,
	"fan":                     device.ControlFanOn,
	"antidewheater":           device.ControlAntiDew,
	"anti-dewheater":          device.ControlAntiDew,
	"gamma":                   device.ControlGamma,
	"flip":                    device.ControlFlip,
	"whitebalancer":           device.ControlWBRed,
	"wb_r":                    device.ControlWBRed,
	"whitebalanceb":           device.ControlWBBlue,
	"wb_b":                    device.ControlWBBlue,
	"overclock":               device.ControlOverclock,
	"autoexpmaxgain":          device.ControlAutoMaxGain,
	"autoexpmaxexpms":         device.ControlAutoMaxExp,
	"autoexptargetbrightness": device.ControlAutoTargetBrightness,
}

// normaliseControlName strips the punctuation and case that vary between camera models.
func normaliseControlName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch r {
		case ' ', '\t', '_', '(', ')', '%', '.':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// canonicalControlName maps one reported control onto our vocabulary. Anything unrecognised keeps a
// slug of its own name rather than being dropped: an unknown control the camera says is writable is
// still worth exposing, and hiding it would be a silent loss of capability.
func canonicalControlName(reported string) string {
	key := normaliseControlName(reported)
	if name, ok := controlAliases[key]; ok {
		return name
	}
	if key == "" {
		return ""
	}
	return "x_" + key // "x_" marks a control this engine has no first-class handling for
}

// controlMap remembers which SDK id backs each canonical control on THIS camera.
type controlMap struct {
	byName map[string]int32
}

func newControlMap() *controlMap { return &controlMap{byName: map[string]int32{}} }

func (m *controlMap) put(name string, id int32) {
	if name != "" {
		m.byName[name] = id
	}
}

// id returns the SDK control number for a canonical name, and whether this camera has it.
func (m *controlMap) id(name string) (int32, bool) {
	if m == nil {
		return 0, false
	}
	id, ok := m.byName[name]
	return id, ok
}

// describe renders the resolved name → SDK id mapping. Logged once at connect: it is the only record
// of which numeric control the driver decided backs each name, and the bring-up question "is the
// cooler really the cooler?" is answered by reading it rather than by trusting the header.
func (m *controlMap) describe() string {
	if m == nil || len(m.byName) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(m.byName))
	for n := range m.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf("%s=%d", n, m.byName[n]))
	}
	return strings.Join(parts, " ")
}

// missingEssentials lists the controls the engine relies on that this camera did not report, so the
// gap is stated at connect time rather than discovered when a capture behaves oddly.
func (m *controlMap) missingEssentials() []string {
	var out []string
	for _, name := range []string{device.ControlGain, device.ControlExposure} {
		if _, ok := m.id(name); !ok {
			out = append(out, name)
		}
	}
	return out
}

// controlUnit labels a control so the UI need not guess; the SDK gives no units at all.
func controlUnit(name string) string {
	switch name {
	case device.ControlExposure:
		return "µs"
	case device.ControlTemperature, device.ControlTargetTemp:
		return "°C"
	case device.ControlCoolerPower:
		return "%"
	case device.ControlUSBBandwidth:
		return "%"
	default:
		return ""
	}
}

// scaleDivisor reports how to turn a raw SDK value into real units. ASI cameras report the sensor
// temperature in TENTHS of a degree while the cooler set-point is in whole degrees — a genuine
// inconsistency in the SDK, and showing "-100 °C" instead of "-10 °C" would be alarming.
func scaleDivisor(name string) int64 {
	if name == device.ControlTemperature {
		return 10
	}
	return 0
}
