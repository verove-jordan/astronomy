package nexstar

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// The connection doctor: why can — or can't — this Mac talk to the hand controller.
//
// This exists because the hardest failure on the NexStar+ USB path is not in our code at all. The
// hand controller's mini-USB socket is a Prolific PL2303 bridge, and macOS ships no driver for it:
// a /dev/cu.* device only appears once Prolific's "PL2303 Serial" system extension is installed AND
// approved in System Settings, and the current one refuses the discontinued HXA/XA/TA parts
// outright. From inside Go all of those look identical — there is simply no port to open — so the
// only way to tell the user what is wrong is to look at the USB bus itself and compare it with the
// list of serial devices. A wrong guess here sends someone hunting a cable fault for an evening.

// Verdicts. Each one maps to exactly one action, which is the point of having them.
const (
	// VerdictOK — a hand controller answered, or (without probing) a bridge exists and owns a port.
	VerdictOK = "ok"
	// VerdictNoUSBDevice — nothing that could be a hand controller is on the bus.
	VerdictNoUSBDevice = "no_usb_device"
	// VerdictDriverMissing — the bridge is on the bus but owns no serial device node.
	VerdictDriverMissing = "driver_missing"
	// VerdictChipUnsupported — as above, and the chip is one the current driver is known to refuse.
	VerdictChipUnsupported = "chip_unsupported"
	// VerdictPortBusy — the port exists but another program holds it.
	VerdictPortBusy = "port_busy"
	// VerdictPermissionDenied — the port exists but this user may not open it.
	VerdictPermissionDenied = "permission_denied"
	// VerdictNoReply — the port opens and nothing answers the echo.
	VerdictNoReply = "no_reply"
	// VerdictUnknown — the USB bus could not be read, so only the port list is evidence.
	VerdictUnknown = "unknown"
)

// USBDevice is one device on the USB bus, reduced to what a serial diagnosis needs.
type USBDevice struct {
	VendorID  int    `json:"vendor_id"`
	ProductID int    `json:"product_id"`
	Release   int    `json:"release"`  // bcdDevice — the chip revision, and on Prolific the whole story
	USBSpec   int    `json:"usb_spec"` // bcdUSB
	Vendor    string `json:"vendor"`
	Product   string `json:"product"`
	Serial    string `json:"serial"`
	// Callouts are the /dev/cu.* nodes this device owns. Empty means the OS enumerated the device
	// but no driver claimed it — which is the whole PL2303-on-macOS problem in one field.
	Callouts []string `json:"callouts"`
}

// PortProbe is the result of actually opening a port and asking whether a mount is there.
type PortProbe struct {
	Path     string `json:"path"`
	OK       bool   `json:"ok"`
	Model    string `json:"model,omitempty"`
	Firmware string `json:"firmware,omitempty"`
	Error    string `json:"error,omitempty"`

	// err keeps the original error so the verdict can match on sentinels rather than on the message
	// text. Matching text would break the moment a wrapper adds a prefix, and would break silently —
	// the diagnosis would just quietly stop recognising "another program holds the port".
	err error
}

// Diagnosis is the whole answer to "will this Mac drive the mount".
type Diagnosis struct {
	Verdict string `json:"verdict"`
	Detail  string `json:"detail"`
	Chip    string `json:"chip,omitempty"`

	Ports  []PortInfo  `json:"ports"`
	USB    []USBDevice `json:"usb"`
	Probes []PortProbe `json:"probes,omitempty"`

	// ScanError records why the USB bus could not be read (not macOS, ioreg missing). The diagnosis
	// still runs on the port list alone; it just cannot explain an absent port.
	ScanError string `json:"scan_error,omitempty"`
}

// OK reports whether the link is usable right now.
func (d Diagnosis) OK() bool { return d.Verdict == VerdictOK }

// Diagnose inspects the USB bus and the serial device list and, when probe is set, opens every
// likely port and runs the handshake. Probing is opt-in because opening a port takes it exclusively
// (macOS serial opens use TIOCEXCL), which would knock a running device server off its mount.
func Diagnose(ctx context.Context, probe bool) Diagnosis {
	ports := ListPorts()
	devs, scanErr := scanUSB(ctx)
	var probes []PortProbe
	if probe {
		probes = probePorts(ctx, ports)
	}
	return diagnose(ports, devs, scanErr, probes, probe)
}

// diagnose is the whole decision, with every input passed in. Keeping it separate from the two
// things that touch the machine — reading the USB bus and opening ports — is what lets the verdict
// table be tested exhaustively from recorded evidence instead of only on a Mac with a mount wired up.
func diagnose(ports []PortInfo, devs []USBDevice, scanErr error, probes []PortProbe, probed bool) Diagnosis {
	d := Diagnosis{Ports: ports, USB: devs, Probes: probes}
	if scanErr != nil {
		d.ScanError = scanErr.Error()
	}
	bridge, haveBridge := pickBridge(devs)
	if haveBridge {
		d.Chip = bridge.chip()
	}
	d.Verdict, d.Detail = verdict(d, bridge, haveBridge, probed)
	return d
}

// probePorts opens each plausible port in turn and asks the mount to identify itself.
//
// Only ports that look like a USB-serial adapter are tried: opening a Bluetooth channel to see
// whether a telescope is behind it wakes the user's headphones for no reason, and on some macOS
// versions blocks for several seconds.
func probePorts(ctx context.Context, ports []PortInfo) []PortProbe {
	out := make([]PortProbe, 0, len(ports))
	for _, p := range ports {
		if !p.Likely {
			continue
		}
		res := PortProbe{Path: p.Path}
		m := New(p.Path, nil)
		if err := m.Connect(ctx); err != nil {
			res.err, res.Error = err, err.Error()
		} else {
			res.OK = true
			res.Model, res.Firmware = m.Model(), m.Firmware()
		}
		_ = m.Close()
		out = append(out, res)
	}
	return out
}

// verdict turns the evidence into one named state and one sentence of advice.
//
// The order matters: a probe that succeeded settles the question no matter what the bus looks like,
// and a probe that failed for a nameable reason (busy, permissions) is more informative than
// anything the bus can say. Only when nothing was probed do we fall back to reasoning about the
// chip, and only then does the PL2303 driver story matter.
func verdict(d Diagnosis, bridge USBDevice, haveBridge, probed bool) (string, string) {
	for _, p := range d.Probes {
		if p.OK {
			return VerdictOK, fmt.Sprintf("%s answered on %s (firmware %s).", nonEmpty(p.Model, "a mount"), p.Path, nonEmpty(p.Firmware, "unknown"))
		}
	}
	for _, p := range d.Probes {
		switch {
		case errors.Is(p.err, ErrPortBusy):
			return VerdictPortBusy, fmt.Sprintf(
				"%s exists but another program holds it — stop `astrostack device` (or CPWI, or a planetarium app still connected) and try again.", p.Path)
		case errors.Is(p.err, os.ErrPermission):
			return VerdictPermissionDenied, fmt.Sprintf(
				"%s exists but this user may not open it. Check the file's permissions, and that no security tool is blocking serial access.", p.Path)
		}
	}
	if probed && len(d.Probes) > 0 {
		return VerdictNoReply, fmt.Sprintf(
			"%s opens but nothing answers. The hand controller is powered by the mount, not by USB: switch the mount on, let the hand controller finish booting past its splash screen, and make sure the cable goes to the socket on the hand controller — not the mount's own AUX port.",
			d.Probes[0].Path)
	}

	switch {
	case haveBridge && len(bridge.Callouts) > 0:
		return VerdictOK, fmt.Sprintf(
			"%s is on the bus and owns %s. Re-run with a probe to confirm the mount answers.",
			d.Chip, strings.Join(bridge.Callouts, ", "))

	case haveBridge && bridge.support() == chipDiscontinued:
		return VerdictChipUnsupported, fmt.Sprintf(
			"%s is on the USB bus but macOS created no serial device for it. This revision is one of the discontinued Prolific parts that the current \"PL2303 Serial\" driver refuses. Either install a legacy Prolific driver, or use the hand controller's RJ-11 RS-232 socket with an FTDI USB-serial cable — macOS drives those with no extra software, and the mount protocol is identical.",
			d.Chip)

	case haveBridge:
		return VerdictDriverMissing, fmt.Sprintf(
			"%s is on the USB bus but macOS created no serial device for it. Install \"PL2303 Serial\" from the Mac App Store and approve it in System Settings → Privacy & Security, then unplug and replug the cable. If it is already installed and approved, this chip is likely one of the discontinued Prolific revisions the driver refuses — fall back to the hand controller's RJ-11 RS-232 socket with an FTDI USB-serial cable.",
			d.Chip)

	case d.ScanError != "":
		if likelyPorts(d.Ports) > 0 {
			return VerdictUnknown, "The USB bus could not be read here, but a USB-serial port is present — probe it to find out whether a mount answers."
		}
		return VerdictUnknown, "The USB bus could not be read here and no USB-serial port is present."

	default:
		return VerdictNoUSBDevice, "No USB-serial adapter is on the bus. The hand controller is powered by the mount, not by USB, so switch the mount on first and let the hand controller boot; then check that the cable is a data cable (many are charge-only) and plugged into the hand controller's own USB socket."
	}
}

// pickBridge chooses the device the diagnosis should talk about: a recognised USB-serial bridge,
// preferring one that already owns a port, then Prolific (what a NexStar+ actually contains) over
// any other adapter the user happens to have plugged in.
func pickBridge(devs []USBDevice) (USBDevice, bool) {
	best, found := USBDevice{}, false
	score := func(d USBDevice) int {
		s := 0
		if len(d.Callouts) > 0 {
			s += 4
		}
		if d.VendorID == vendorProlific {
			s += 2
		}
		return s
	}
	for _, d := range devs {
		if _, known := d.bridgeName(); !known {
			continue
		}
		if !found || score(d) > score(best) {
			best, found = d, true
		}
	}
	return best, found
}

func likelyPorts(ports []PortInfo) int {
	n := 0
	for _, p := range ports {
		if p.Likely {
			n++
		}
	}
	return n
}

// USB-serial bridge vendors. Prolific is the one that matters — it is what Celestron put in the
// NexStar+ hand controller — but the others appear on the RS-232 cables people fall back to, and
// naming them is the difference between "your adapter is fine" and a wasted evening.
const (
	vendorProlific = 0x067B
	vendorFTDI     = 0x0403
	vendorSiLabs   = 0x10C4
	vendorWCH      = 0x1A86
)

type chipSupport int

const (
	chipUnknown chipSupport = iota
	chipSupported
	chipDiscontinued
)

var bridgeProducts = map[int]map[int]string{
	vendorProlific: {
		0x2303: "PL2303", // refined by revision below
		0x23A3: "PL2303GC",
		0x23B3: "PL2303GB",
		0x23C3: "PL2303GT",
		0x23D3: "PL2303GL",
		0x23E3: "PL2303GE",
		0x23F3: "PL2303GS",
	},
	vendorFTDI: {
		0x6001: "FT232R",
		0x6010: "FT2232",
		0x6011: "FT4232",
		0x6014: "FT232H",
		0x6015: "FT230X",
	},
	vendorSiLabs: {
		0xEA60: "CP2102",
		0xEA70: "CP2105",
		0xEA71: "CP2108",
	},
	vendorWCH: {
		0x7523: "CH340",
		0x5523: "CH341",
		0x55D4: "CH9102",
	},
}

// bridgeName reports the chip's name and whether this device is a USB-serial bridge at all.
//
// An unrecognised product from a known bridge vendor still counts: Prolific ship variants faster
// than any table is updated, and "some Prolific bridge" is a far more useful thing to tell the user
// than "no adapter found".
func (d USBDevice) bridgeName() (string, bool) {
	byVendor, ok := bridgeProducts[d.VendorID]
	if !ok {
		return "", false
	}
	if name, ok := byVendor[d.ProductID]; ok {
		return name, true
	}
	switch d.VendorID {
	case vendorProlific:
		return "Prolific USB-serial bridge", true
	case vendorFTDI:
		return "FTDI USB-serial bridge", true
	case vendorSiLabs:
		return "Silicon Labs USB-serial bridge", true
	case vendorWCH:
		return "WCH USB-serial bridge", true
	}
	return "", false
}

// support says whether the current macOS driver will claim this chip.
//
// Only Prolific's classic 067b:2303 is ambiguous, and the ambiguity is the whole problem: the same
// USB id covers parts that work and parts that Prolific discontinued and their current driver
// refuses. The device descriptor is what separates them — bcdUSB 2.00 marks the newer HXN/G silicon,
// and within the older USB 1.1 family bcdDevice 0x300 is the discontinued H/HXA/XA/TA generation
// while 0x400 is the HXD/EA/RA/SA generation that is still supported. Anything else is reported as
// unknown rather than guessed at, because a confident wrong answer here costs a night.
func (d USBDevice) support() chipSupport {
	if d.VendorID != vendorProlific {
		return chipUnknown
	}
	if d.ProductID != 0x2303 {
		return chipSupported // the G-series product ids are all current parts
	}
	switch {
	case d.USBSpec >= 0x0200:
		return chipSupported
	case d.USBSpec == 0x0110 && d.Release == 0x0300:
		return chipDiscontinued
	case d.USBSpec == 0x0110 && d.Release == 0x0400:
		return chipSupported
	}
	return chipUnknown
}

// chip renders the chip for a human: name, ids, revision, and the support note when there is one.
func (d USBDevice) chip() string {
	name, ok := d.bridgeName()
	if !ok {
		name = nonEmpty(d.Product, "unknown device")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%04x:%04x rev %#04x", name, d.VendorID, d.ProductID, d.Release)
	switch d.support() {
	case chipDiscontinued:
		b.WriteString(", discontinued")
	case chipUnknown:
		if d.VendorID == vendorProlific {
			b.WriteString(", revision not in our table")
		}
	}
	b.WriteString(")")
	return b.String()
}

// String renders the diagnosis for a terminal.
func (d Diagnosis) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "NexStar hand controller — %s\n", d.Verdict)
	if d.Chip != "" {
		fmt.Fprintf(&b, "  chip      %s\n", d.Chip)
	}
	for _, u := range d.USB {
		if _, known := u.bridgeName(); !known {
			continue
		}
		fmt.Fprintf(&b, "  usb       %s %s%s\n",
			nonEmpty(u.Vendor, "?"), nonEmpty(u.Product, "?"), calloutSuffix(u.Callouts))
	}
	if len(d.Ports) == 0 {
		b.WriteString("  ports     none\n")
	}
	for _, p := range d.Ports {
		star := ""
		if p.Likely {
			star = "  <- looks like a USB-serial adapter"
		}
		fmt.Fprintf(&b, "  port      %s%s\n", p.Path, star)
	}
	for _, p := range d.Probes {
		if p.OK {
			fmt.Fprintf(&b, "  probe     %s: %s, firmware %s\n", p.Path, nonEmpty(p.Model, "?"), nonEmpty(p.Firmware, "?"))
		} else {
			fmt.Fprintf(&b, "  probe     %s: %s\n", p.Path, p.Error)
		}
	}
	if d.ScanError != "" {
		fmt.Fprintf(&b, "  usb scan  %s\n", d.ScanError)
	}
	fmt.Fprintf(&b, "\n%s\n", wrap(d.Detail, 92, "  "))
	return b.String()
}

func calloutSuffix(callouts []string) string {
	if len(callouts) == 0 {
		return " (no serial device)"
	}
	return " -> " + strings.Join(callouts, ", ")
}

func nonEmpty(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

// wrap breaks advice onto terminal-width lines. The detail strings are long on purpose — they are
// the instructions — and an unwrapped paragraph is where people stop reading.
func wrap(s string, width int, indent string) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}
	var b strings.Builder
	line := indent
	for _, w := range words {
		if len(line)+len(w)+1 > width && line != indent {
			b.WriteString(line + "\n")
			line = indent
		}
		if line != indent {
			line += " "
		}
		line += w
	}
	b.WriteString(line)
	return b.String()
}

// parseIoreg turns `ioreg -r -c IOUSBHostDevice -l -w0` into the USB devices it describes.
//
// The output is a tree drawn with `+-o` node lines whose indentation is two characters per level,
// each node followed by its own properties. A serial bridge's /dev/cu.* path is not a property of
// the USB device at all: it lives on an IOSerialBSDClient several levels below, so the parser
// tracks the ancestry and attributes a callout to the nearest USB device above it. That attribution
// is the entire reason for parsing a tree rather than grepping — "there is a Prolific chip AND
// there is a serial port" is not the same statement as "the Prolific chip owns that serial port",
// and with two adapters plugged in the difference decides which port we tell the user to use.
//
// It is kept pure and platform-independent so it can be tested from recorded fixtures anywhere.
func parseIoreg(out string) []USBDevice {
	type node struct {
		depth int
		dev   *USBDevice // non-nil when this node is the USB device itself
	}
	var (
		stack []node
		devs  []*USBDevice
	)
	// nearestDevice walks back up the open nodes for the USB device that owns the current one.
	nearestDevice := func() *USBDevice {
		for i := len(stack) - 1; i >= 0; i-- {
			if stack[i].dev != nil {
				return stack[i].dev
			}
		}
		return nil
	}

	for _, line := range strings.Split(out, "\n") {
		if idx := strings.Index(line, "+-o "); idx >= 0 && isTreePrefix(line[:idx]) {
			depth := idx / 2
			for len(stack) > 0 && stack[len(stack)-1].depth >= depth {
				stack = stack[:len(stack)-1]
			}
			n := node{depth: depth}
			if strings.Contains(line, "<class IOUSBHostDevice,") {
				dev := &USBDevice{}
				devs = append(devs, dev)
				n.dev = dev
			}
			stack = append(stack, n)
			continue
		}

		key, value, ok := ioregProperty(line)
		if !ok || len(stack) == 0 {
			continue
		}
		owner := nearestDevice()
		if owner == nil {
			continue
		}
		// Properties belong to the node they follow. Only IOCalloutDevice is read from a descendant
		// (the serial client); everything else is read only while the USB device itself is on top,
		// because interfaces repeat their parent's ids and would otherwise overwrite them.
		onDevice := stack[len(stack)-1].dev == owner
		switch key {
		case "IOCalloutDevice":
			if p := unquote(value); p != "" {
				owner.Callouts = append(owner.Callouts, p)
			}
		case "idVendor":
			if onDevice {
				owner.VendorID = atoiOr(value, owner.VendorID)
			}
		case "idProduct":
			if onDevice {
				owner.ProductID = atoiOr(value, owner.ProductID)
			}
		case "bcdDevice":
			if onDevice {
				owner.Release = atoiOr(value, owner.Release)
			}
		case "bcdUSB":
			if onDevice {
				owner.USBSpec = atoiOr(value, owner.USBSpec)
			}
		case "USB Vendor Name", "kUSBVendorString":
			if onDevice && owner.Vendor == "" {
				owner.Vendor = unquote(value)
			}
		case "USB Product Name", "kUSBProductString":
			if onDevice && owner.Product == "" {
				owner.Product = unquote(value)
			}
		case "kUSBSerialNumberString", "USB Serial Number":
			if onDevice && owner.Serial == "" {
				owner.Serial = unquote(value)
			}
		}
	}

	out2 := make([]USBDevice, 0, len(devs))
	for _, d := range devs {
		if d.VendorID == 0 && d.ProductID == 0 {
			continue // a node that carried no descriptor is not a device we can reason about
		}
		sort.Strings(d.Callouts)
		out2 = append(out2, *d)
	}
	return out2
}

// isTreePrefix reports whether everything before a "+-o" is tree drawing rather than text, so a
// device whose NAME contains "+-o" cannot be mistaken for a node header.
func isTreePrefix(s string) bool {
	return strings.Trim(s, " |") == ""
}

// ioregProperty extracts `"key" = value` from a property line, ignoring the tree drawing in front.
func ioregProperty(line string) (key, value string, ok bool) {
	t := strings.TrimLeft(line, " |")
	if !strings.HasPrefix(t, `"`) {
		return "", "", false
	}
	end := strings.Index(t[1:], `"`)
	if end < 0 {
		return "", "", false
	}
	key = t[1 : 1+end]
	rest := strings.TrimSpace(t[1+end+1:])
	if !strings.HasPrefix(rest, "=") {
		return "", "", false
	}
	return key, strings.TrimSpace(strings.TrimPrefix(rest, "=")), true
}

func unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && strings.HasPrefix(v, `"`) && strings.HasSuffix(v, `"`) {
		return v[1 : len(v)-1]
	}
	return ""
}

func atoiOr(v string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fallback
	}
	return n
}

// errUSBScanUnsupported is returned by scanUSB on platforms with no ioreg. The diagnosis still runs
// on the serial port list; it just cannot explain why a port is absent.
var errUSBScanUnsupported = errors.New("reading the USB bus is only implemented on macOS")
