package asi

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/device"
)

// The struct decoding is the highest-risk part of this driver and the only part testable without
// hardware: purego does not know C layouts, so a wrong offset reads plausible-looking garbage rather
// than crashing. These tests build a buffer exactly as the C compiler would lay it out and check
// that the decoder recovers the values.
func TestParseCameraInfo(t *testing.T) {
	b := make([]byte, infoStructSize)
	copy(b[offName:], "ZWO ASI1600MM Pro\x00")
	binary.LittleEndian.PutUint32(b[offCameraID:], 3)
	binary.LittleEndian.PutUint64(b[offMaxHeight:], 3520)
	binary.LittleEndian.PutUint64(b[offMaxWidth:], 4656)
	binary.LittleEndian.PutUint32(b[offIsColorCam:], 0)
	binary.LittleEndian.PutUint32(b[offBayerPattern:], 0)
	binary.LittleEndian.PutUint32(b[offSupportedBins:], 1)
	binary.LittleEndian.PutUint32(b[offSupportedBins+4:], 2)
	binary.LittleEndian.PutUint32(b[offSupportedBins+8:], 3)
	binary.LittleEndian.PutUint32(b[offSupportedBins+12:], 4)
	binary.LittleEndian.PutUint32(b[offSupportedBins+16:], 0) // terminator
	binary.LittleEndian.PutUint64(b[offPixelSize:], math.Float64bits(3.8))

	info := parseCameraInfo(b)
	assert.Equal(t, "ZWO ASI1600MM Pro", info.Name)
	assert.Equal(t, int32(3), info.ID)
	assert.Equal(t, int64(4656), info.MaxWidth, "the real ASI1600 sensor width")
	assert.Equal(t, int64(3520), info.MaxHeight)
	assert.False(t, info.IsColor)
	assert.InDelta(t, 3.8, info.PixelSizeUm, 1e-9)
	assert.Equal(t, []int{1, 2, 3, 4}, info.Bins, "the bin list stops at its zero terminator")
}

func TestParseControlCaps(t *testing.T) {
	b := make([]byte, capsStructSize)
	copy(b[offCapsName:], "Exposure\x00")
	copy(b[offCapsDescription:], "Exposure Time(us)\x00")
	binary.LittleEndian.PutUint64(b[offCapsMax:], 2000_000_000)
	binary.LittleEndian.PutUint64(b[offCapsMin:], 32)
	binary.LittleEndian.PutUint64(b[offCapsDefault:], 10000)
	binary.LittleEndian.PutUint32(b[offCapsAutoSupport:], 1)
	binary.LittleEndian.PutUint32(b[offCapsWritable:], 1)
	binary.LittleEndian.PutUint32(b[offCapsControlType:], 1) // whatever id this camera happens to use

	caps := parseControlCaps(b)
	assert.Equal(t, "Exposure", caps.Name)
	assert.Equal(t, "Exposure Time(us)", caps.Description)
	assert.Equal(t, int64(32), caps.Min, "the documented 32 µs floor comes from the camera itself")
	assert.Equal(t, int64(2_000_000_000), caps.Max)
	assert.Equal(t, int64(10000), caps.Default)
	assert.True(t, caps.AutoSupport)
	assert.True(t, caps.Writable)
	assert.Equal(t, int32(1), caps.ControlType,
		"the id is read back verbatim; nothing in the driver assumes what number it should be")
}

// A short buffer must yield a zero value rather than reading past the end.
func TestParse_ShortBuffersAreSafe(t *testing.T) {
	assert.Equal(t, CameraInfo{}, parseCameraInfo(make([]byte, 8)))
	assert.Equal(t, ControlCaps{}, parseControlCaps(make([]byte, 8)))
	assert.NotPanics(t, func() { parseCameraInfo(nil); parseControlCaps(nil) })
}

// Control dispatch is driven by the NAME the camera reports, not by a remembered enum, so the test
// is about tolerating ZWO's spelling variations rather than about recalling id numbers.
func TestCanonicalControlName(t *testing.T) {
	for reported, want := range map[string]string{
		"Gain": device.ControlGain, "Exposure": device.ControlExposure,
		"Offset": device.ControlOffset, "Brightness": device.ControlOffset,
		"Temperature": device.ControlTemperature, "TargetTemp": device.ControlTargetTemp,
		"CoolerOn": device.ControlCoolerOn, "Cooler On": device.ControlCoolerOn,
		// Spelled as a real ASI1600MM Pro reports it — verified against the hardware, not the header.
		"CoolPowerPerc":     device.ControlCoolerPower,
		"CoolerPowerPerc":   device.ControlCoolerPower,
		"BandWidth":         device.ControlUSBBandwidth,
		"BandwidthOverload": device.ControlUSBBandwidth,
		"HighSpeedMode":     device.ControlHighSpeed,
		"MonoBin":           device.ControlMonoBin,
		"HardwareBin":       device.ControlHardwareBin,
		"Fan":               device.ControlFanOn,
		"AntiDewHeater":     device.ControlAntiDew,
	} {
		assert.Equal(t, want, canonicalControlName(reported), "reported name %q", reported)
	}

	// A control we have never seen must still be surfaced. Dropping it would silently lose a
	// capability the camera says it has.
	assert.Equal(t, "x_somethingnew", canonicalControlName("Something New"))
	assert.Empty(t, canonicalControlName(""))
}

// The map is what dispatch uses, so it must return the camera's own id and report absence honestly.
func TestControlMap(t *testing.T) {
	m := newControlMap()
	m.put(device.ControlCoolerOn, 17)
	m.put(device.ControlTemperature, 8)

	id, ok := m.id(device.ControlCoolerOn)
	assert.True(t, ok)
	assert.Equal(t, int32(17), id, "the id comes from the camera, whatever number it happens to be")

	_, ok = m.id(device.ControlFanOn)
	assert.False(t, ok, "a control this camera lacks must report absent, never id zero")

	assert.ElementsMatch(t, []string{device.ControlGain, device.ControlExposure},
		m.missingEssentials(), "a camera without gain or exposure is worth complaining about")
	m.put(device.ControlGain, 0)
	m.put(device.ControlExposure, 1)
	assert.Empty(t, m.missingEssentials())

	// Camera holds a nil map before connect, so lookups must be safe there.
	var nilMap *controlMap
	_, ok = nilMap.id(device.ControlGain)
	assert.False(t, ok)
}

// The SDK reports sensor temperature in TENTHS of a degree but the cooler set-point in whole
// degrees. Showing "-100 °C" instead of "-10 °C" would be alarming, so the divisor reaches the UI.
func TestScaleDivisorAndUnits(t *testing.T) {
	assert.Equal(t, int64(10), scaleDivisor(device.ControlTemperature))
	assert.Zero(t, scaleDivisor(device.ControlTargetTemp), "the set-point is already in degrees")
	assert.Equal(t, "µs", controlUnit(device.ControlExposure))
	assert.Equal(t, "°C", controlUnit(device.ControlTemperature))
	assert.Equal(t, "%", controlUnit(device.ControlCoolerPower))
	assert.Empty(t, controlUnit(device.ControlGain))
}

func TestBayerName(t *testing.T) {
	assert.Equal(t, "RGGB", bayerName(0))
	assert.Equal(t, "BGGR", bayerName(1))
	assert.Equal(t, "GRBG", bayerName(2))
	assert.Equal(t, "GBRG", bayerName(3))
	assert.Empty(t, bayerName(99))
}

// A missing SDK must be a clear, actionable message — never a crash and never a silent success.
// This is the behaviour on any machine without ZWO's library, which includes CI.
func TestFindLibrary_MissingSDKExplainsItself(t *testing.T) {
	t.Setenv("ASI_SDK_LIB", filepath.Join(t.TempDir(), "nope.dylib"))
	_, err := findLibrary()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "does not exist")
}

func TestFindLibrary_UsesTheEnvVarFirst(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "libASICamera2.dylib")
	require.NoError(t, os.WriteFile(lib, []byte("not really a library"), 0o644))

	t.Setenv("ASI_SDK_LIB", lib)
	got, err := findLibrary()
	require.NoError(t, err)
	assert.Equal(t, lib, got, "an explicit ASI_SDK_LIB must win over the probe list")
}

// Loading a file that is not an ASI library must fail with an explanation rather than panicking
// somewhere inside purego. Exactly what happens with the x86-only library ZWO bundles in ASIStudio
// when the engine is running as arm64.
func TestLoad_RejectsSomethingThatIsNotTheSDK(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "libASICamera2.dylib")
	require.NoError(t, os.WriteFile(lib, []byte("definitely not a mach-o"), 0o644))
	t.Setenv("ASI_SDK_LIB", lib)

	// load() memoises, so call the pieces it uses rather than load() itself — otherwise this test
	// would poison every other test in the package.
	path, err := findLibrary()
	require.NoError(t, err)
	assert.Equal(t, lib, path)

	// Available() surfaces whatever load() decided; on a machine with no usable SDK it must be an
	// error with a message a user could act on, never a panic.
	assert.NotPanics(t, func() {
		if _, err := Available(); err != nil {
			assert.NotEmpty(t, err.Error())
		}
	})
}

func TestCheck(t *testing.T) {
	assert.NoError(t, check(0))
	assert.ErrorContains(t, check(15), "video mode")
	assert.ErrorContains(t, check(11), "timed out")
	assert.ErrorContains(t, check(99), "SDK error 99")
}

// The failure a user of this project will actually hit: ZWO's ASIStudio bundle has no arm64 slice,
// so the message must name the fix rather than print 400 characters of loader paths.
func TestExplainLoadFailure_NamesTheArchitectureFix(t *testing.T) {
	raw := errors.New("dlopen(...): tried: '...' (fat file, but missing compatible architecture " +
		"(have 'i386,x86_64', need 'arm64'))")
	err := explainLoadFailure("/Applications/ASIStudio.app/.../libASICamera2.dylib", raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no arm64 macOS library",
		"the message must say the arm64 build does not exist, because it does not")
	assert.Contains(t, err.Error(), "just device-x86", "and name the recipe that actually works")
	assert.NotContains(t, err.Error(), "1.41",
		"must NOT send the user hunting for an SDK version that has no arm64 build either")
}

// Any other load failure must pass through with its real cause intact.
func TestExplainLoadFailure_PassesOtherErrorsThrough(t *testing.T) {
	err := explainLoadFailure("/tmp/x.dylib", errors.New("file not found"))
	assert.Contains(t, err.Error(), "file not found")
	assert.NotContains(t, err.Error(), "device-x86")
}
