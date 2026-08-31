package nexstar

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The doctor is the first thing run against a new hand controller, and its whole value is being
// right about WHY there is no port. These tests pin the two halves of that: reading the USB tree
// (from recorded ioreg output, so they run anywhere) and turning the evidence into one verdict.

func loadIoreg(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "ioreg", name))
	require.NoError(t, err)
	return string(b)
}

func TestParseIoreg_ReadsDevicesAndTheirSerialNodes(t *testing.T) {
	tests := []struct {
		name    string
		fixture string
		want    []USBDevice
	}{
		{
			name:    "prolific with a working driver",
			fixture: "prolific_supported.txt",
			want: []USBDevice{{
				VendorID: 0x067B, ProductID: 0x2303, Release: 0x0400, USBSpec: 0x0110,
				Vendor: "Prolific Technology Inc.", Product: "USB-Serial Controller",
				Callouts: []string{"/dev/cu.usbserial-1420"},
			}},
		},
		{
			name:    "prolific on the bus with no driver claiming it",
			fixture: "prolific_discontinued_no_driver.txt",
			want: []USBDevice{{
				VendorID: 0x067B, ProductID: 0x2303, Release: 0x0300, USBSpec: 0x0110,
				Vendor: "Prolific Technology Inc.", Product: "USB-Serial Controller",
			}},
		},
		{
			// The reason the parser walks a tree rather than grepping: with two adapters attached,
			// each /dev/cu.* must land on the chip that actually owns it.
			name:    "two adapters keep their own ports",
			fixture: "two_adapters.txt",
			want: []USBDevice{
				{
					VendorID: 0x067B, ProductID: 0x2303, Release: 0x0400, USBSpec: 0x0110,
					Vendor: "Prolific Technology Inc.", Product: "USB-Serial Controller",
					Callouts: []string{"/dev/cu.usbserial-1420"},
				},
				{
					VendorID: 0x0403, ProductID: 0x6001, Release: 0x0600, USBSpec: 0x0200,
					Vendor: "FTDI", Product: "FT232R USB UART", Serial: "A50285BI",
					Callouts: []string{"/dev/cu.usbserial-A50285BI"},
				},
			},
		},
		{
			name:    "a device that is not a serial bridge at all",
			fixture: "no_bridge.txt",
			want: []USBDevice{{
				VendorID: 0x1050, ProductID: 0x0407, Release: 0x0543, USBSpec: 0x0200,
				Vendor: "Yubico", Product: "YubiKey OTP+FIDO+CCID",
			}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseIoreg(loadIoreg(t, tt.fixture))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseIoreg_EmptyOutputYieldsNoDevices(t *testing.T) {
	assert.Empty(t, parseIoreg(""))
	assert.Empty(t, parseIoreg("+-o Root  <class IORegistryEntry, id 0x100000100, retain 35>\n"))
}

func TestParseIoreg_IgnoresATextPlusInTheDeviceName(t *testing.T) {
	// "YubiKey OTP+FIDO+CCID" contains '+' characters; a looser scan for "+-o" anywhere on the line
	// would still work here, but a NAME containing the full "+-o" sequence must not open a node.
	devs := parseIoreg("+-o Weird +-o Thing@0  <class IOUSBHostDevice, id 0x1, retain 1>\n" +
		"  | {\n" +
		"  |   \"idVendor\" = 1659\n" +
		"  |   \"idProduct\" = 8963\n" +
		"  | }\n")
	require.Len(t, devs, 1)
	assert.Equal(t, 0x067B, devs[0].VendorID)
}

func TestUSBDevice_Support_SeparatesTheDiscontinuedProlificRevisions(t *testing.T) {
	tests := []struct {
		name string
		dev  USBDevice
		want chipSupport
	}{
		{"HXD generation", USBDevice{VendorID: 0x067B, ProductID: 0x2303, USBSpec: 0x0110, Release: 0x0400}, chipSupported},
		{"H/HXA/XA/TA generation", USBDevice{VendorID: 0x067B, ProductID: 0x2303, USBSpec: 0x0110, Release: 0x0300}, chipDiscontinued},
		{"HXN silicon reports USB 2.0", USBDevice{VendorID: 0x067B, ProductID: 0x2303, USBSpec: 0x0200, Release: 0x0605}, chipSupported},
		{"G series has its own product id", USBDevice{VendorID: 0x067B, ProductID: 0x23C3, USBSpec: 0x0200, Release: 0x0100}, chipSupported},
		{"an unseen revision is not guessed at", USBDevice{VendorID: 0x067B, ProductID: 0x2303, USBSpec: 0x0110, Release: 0x0202}, chipUnknown},
		{"other vendors are not our problem", USBDevice{VendorID: 0x0403, ProductID: 0x6001}, chipUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.dev.support())
		})
	}
}

func TestUSBDevice_BridgeName_FallsBackToTheVendor(t *testing.T) {
	tests := []struct {
		name      string
		dev       USBDevice
		want      string
		wantKnown bool
	}{
		{"known prolific part", USBDevice{VendorID: 0x067B, ProductID: 0x23C3}, "PL2303GT", true},
		{"unknown prolific part is still a bridge", USBDevice{VendorID: 0x067B, ProductID: 0x9999}, "Prolific USB-serial bridge", true},
		{"known ftdi part", USBDevice{VendorID: 0x0403, ProductID: 0x6001}, "FT232R", true},
		{"a yubikey is not a bridge", USBDevice{VendorID: 0x1050, ProductID: 0x0407}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, known := tt.dev.bridgeName()
			assert.Equal(t, tt.wantKnown, known)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestPickBridge_PrefersTheAdapterThatOwnsAPort(t *testing.T) {
	prolificNoPort := USBDevice{VendorID: 0x067B, ProductID: 0x2303}
	ftdiWithPort := USBDevice{VendorID: 0x0403, ProductID: 0x6001, Callouts: []string{"/dev/cu.usbserial-A1"}}

	// A working adapter is more useful to talk about than an idle one, even though the hand
	// controller itself is the Prolific: if a port exists, that is the port to use.
	got, ok := pickBridge([]USBDevice{prolificNoPort, ftdiWithPort})
	require.True(t, ok)
	assert.Equal(t, ftdiWithPort, got)

	// With nothing claimed, Prolific wins, because that is what a NexStar+ contains.
	got, ok = pickBridge([]USBDevice{{VendorID: 0x0403, ProductID: 0x6001}, prolificNoPort})
	require.True(t, ok)
	assert.Equal(t, prolificNoPort, got)

	_, ok = pickBridge([]USBDevice{{VendorID: 0x1050, ProductID: 0x0407}})
	assert.False(t, ok, "a non-bridge device must not be offered as the hand controller")
}

func TestDiagnose_VerdictNamesTheOneThingToDo(t *testing.T) {
	prolificClaimed := USBDevice{
		VendorID: 0x067B, ProductID: 0x2303, USBSpec: 0x0110, Release: 0x0400,
		Callouts: []string{"/dev/cu.usbserial-1420"},
	}
	prolificUnclaimedOld := USBDevice{VendorID: 0x067B, ProductID: 0x2303, USBSpec: 0x0110, Release: 0x0300}
	prolificUnclaimedNew := USBDevice{VendorID: 0x067B, ProductID: 0x2303, USBSpec: 0x0110, Release: 0x0400}
	likely := []PortInfo{{Path: "/dev/cu.usbserial-1420", Label: "cu.usbserial-1420", Likely: true}}

	tests := []struct {
		name     string
		ports    []PortInfo
		devs     []USBDevice
		scanErr  error
		probes   []PortProbe
		probed   bool
		want     string
		contains string
	}{
		{
			name:   "a mount answered",
			ports:  likely,
			devs:   []USBDevice{prolificClaimed},
			probes: []PortProbe{{Path: "/dev/cu.usbserial-1420", OK: true, Model: "Advanced VX", Firmware: "5.30"}},
			probed: true,
			want:   VerdictOK, contains: "Advanced VX",
		},
		{
			name:   "the port is held by something else",
			ports:  likely,
			devs:   []USBDevice{prolificClaimed},
			probes: []PortProbe{{Path: "/dev/cu.usbserial-1420", err: fmt.Errorf("open: %w", ErrPortBusy)}},
			probed: true,
			want:   VerdictPortBusy, contains: "astrostack device",
		},
		{
			name:   "the port may not be opened by this user",
			ports:  likely,
			devs:   []USBDevice{prolificClaimed},
			probes: []PortProbe{{Path: "/dev/cu.usbserial-1420", err: fmt.Errorf("open: %w", os.ErrPermission)}},
			probed: true,
			want:   VerdictPermissionDenied,
		},
		{
			name:   "the cable is fine but the mount is off",
			ports:  likely,
			devs:   []USBDevice{prolificClaimed},
			probes: []PortProbe{{Path: "/dev/cu.usbserial-1420", err: errors.New("no NexStar mount answered")}},
			probed: true,
			want:   VerdictNoReply, contains: "powered by the mount",
		},
		{
			// Distinct from no_reply on purpose: nothing was ever asked, so telling the user to
			// wait for the hand controller to boot sends them after a fault that is not there.
			name:   "the adapter opens but refuses every serial setting",
			ports:  likely,
			devs:   []USBDevice{prolificClaimed},
			probes: []PortProbe{{Path: "/dev/cu.usbserial-1420", err: fmt.Errorf("open: %w", ErrPortUnconfigurable)}},
			probed: true,
			want:   VerdictPortUnconfigurable, contains: "refuses every serial setting",
		},
		{
			name:  "a claimed bridge without probing is provisionally fine",
			ports: likely,
			devs:  []USBDevice{prolificClaimed},
			want:  VerdictOK, contains: "/dev/cu.usbserial-1420",
		},
		{
			name: "a discontinued chip with no port explains itself",
			devs: []USBDevice{prolificUnclaimedOld},
			want: VerdictChipUnsupported, contains: "RS-232",
		},
		{
			name: "a supported chip with no port means the driver is missing",
			devs: []USBDevice{prolificUnclaimedNew},
			want: VerdictDriverMissing, contains: "Privacy & Security",
		},
		{
			name: "nothing on the bus",
			want: VerdictNoUSBDevice, contains: "charge-only",
		},
		{
			name:    "the bus could not be read but a port exists",
			ports:   likely,
			scanErr: errUSBScanUnsupported,
			want:    VerdictUnknown, contains: "probe it",
		},
		{
			name:    "the bus could not be read and nothing is there",
			scanErr: errUSBScanUnsupported,
			want:    VerdictUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := diagnose(tt.ports, tt.devs, tt.scanErr, tt.probes, tt.probed)
			assert.Equal(t, tt.want, d.Verdict)
			if tt.contains != "" {
				assert.Contains(t, d.Detail, tt.contains)
			}
			assert.NotEmpty(t, d.Detail, "a verdict without advice is not a diagnosis")
			assert.NotPanics(t, func() { _ = d.String() })
		})
	}
}

func TestDiagnosis_ChipNamesTheRevision(t *testing.T) {
	d := diagnose(nil, []USBDevice{{VendorID: 0x067B, ProductID: 0x2303, USBSpec: 0x0110, Release: 0x0300}}, nil, nil, false)
	assert.Contains(t, d.Chip, "PL2303")
	assert.Contains(t, d.Chip, "067b:2303")
	assert.Contains(t, d.Chip, "0x0300")
	assert.Contains(t, d.Chip, "discontinued")
}
