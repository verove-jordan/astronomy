package nexstar

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/device"
)

// The mount driver itself: one command in flight at a time, every reply read to its '#' terminator.
//
// That serialisation is not caution, it is the protocol. Celestron's own developer notes say a hand
// controller may take up to 3.5 seconds to answer and that firing commands without waiting makes it
// drop some and mis-attribute others — so one goroutine owns the port and everything else queues
// behind a mutex.

const (
	// replyTimeout is Celestron's documented worst-case response time.
	replyTimeout = 3500 * time.Millisecond
	// BaudRate is fixed by the protocol: 9600 8N1 on the hand controller's serial link.
	BaudRate = 9600
)

// ErrNotAligned is returned when a GoTo is attempted on an unaligned mount. Celestron is explicit
// that GoTo does not work before alignment; the coordinates it would slew to are meaningless, and on
// a German equatorial that can put the tube into the tripod.
var ErrNotAligned = errors.New("the mount is not aligned — GoTo would point somewhere arbitrary")

// Port is the serial link, narrowed to what the driver needs so tests can supply a fake.
type Port interface {
	io.ReadWriteCloser
	SetReadTimeout(d time.Duration) error
}

// Opener creates a port for a device path; swapped out in tests.
type Opener func(path string) (Port, error)

// Mount is a NexStar-protocol mount.
type Mount struct {
	mu     sync.Mutex
	port   Port
	opener Opener
	path   string

	model    string
	firmware string
	// modelCode is the raw byte behind model. It is kept because the PEC rate scale and worm length
	// branch on it, and the display name cannot be mapped back — an unrecognised mount reports
	// "model 42" and would otherwise lose the 42.
	modelCode byte

	// The mount cannot be asked whether PEC is playing or seeking, so the driver remembers what it
	// last commanded. A mount power-cycled behind our back reads as "not playing", which is the safe
	// direction to be wrong in.
	pecPlaying bool
	pecSeeking bool

	// now is the clock used for precession; injectable so tests are not time-dependent.
	now func() time.Time
}

// New builds a driver for a serial device path. Nothing is opened until Connect.
func New(path string, opener Opener) *Mount {
	if opener == nil {
		opener = openSerial
	}
	return &Mount{path: path, opener: opener, now: time.Now}
}

// SetPort chooses the serial device (e.g. /dev/cu.usbserial-1420) before connecting.
func (m *Mount) SetPort(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.path = path
}

// Connect opens the port and proves there is really a hand controller on the other end.
func (m *Mount) Connect(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.path == "" {
		return fmt.Errorf("%w: no serial port selected", device.ErrDriverUnavailable)
	}
	port, err := m.opener(m.path)
	if err != nil {
		return fmt.Errorf("%w: %v", device.ErrDriverUnavailable, err)
	}
	m.port = port
	_ = port.SetReadTimeout(replyTimeout)

	// The echo command is the handshake: a serial port that opens proves nothing (a USB adapter with
	// nothing attached opens happily), but a mount echoes back exactly what it was sent.
	if reply, err := m.commandLocked("Kx"); err != nil || !strings.HasPrefix(reply, "x") {
		_ = port.Close()
		m.port = nil
		return fmt.Errorf("%w: no NexStar mount answered on %s", device.ErrDriverUnavailable, m.path)
	}
	if v, err := m.commandLocked("V"); err == nil {
		m.firmware = parseVersion(v)
	}
	if mm, err := m.commandLocked("m"); err == nil {
		m.model, m.modelCode = parseModel(mm), parseModelCode(mm)
	}
	return nil
}

func (m *Mount) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return nil
	}
	err := m.port.Close()
	m.port = nil
	return err
}

func (m *Mount) Connected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.port != nil
}

// State reports where the mount is, in J2000 — converted from the equinox of date the mount speaks.
func (m *Mount) State(ctx context.Context) (device.MountState, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return device.MountState{}, device.ErrNotConnected
	}
	reply, err := m.commandLocked("e")
	if err != nil {
		return device.MountState{}, err
	}
	raNow, decNow, err := DecodeRADec(reply)
	if err != nil {
		return device.MountState{}, err
	}
	raJ2000, decJ2000 := astro.PrecessToJ2000(raNow, decNow, m.now())

	st := device.MountState{
		Info:  device.Info{ID: "nexstar", Name: m.model, Driver: "nexstar", Kind: device.KindMount},
		RADeg: raJ2000, DecDeg: decJ2000,
		Firmware: m.firmware, Model: m.model,
	}
	if s, err := m.commandLocked("L"); err == nil {
		st.Slewing = parseSlewing(s)
	}
	if a, err := m.commandLocked("J"); err == nil {
		st.Aligned = parseAligned(a)
	}
	if t, err := m.commandLocked("t"); err == nil {
		st.TrackingRate, st.Tracking = trackingName(t)
	}
	if p, err := m.commandLocked("p"); err == nil {
		st.PierSide = parsePierSide(p)
	}
	return st, nil
}

func trackingName(reply string) (string, bool) {
	body := strings.TrimSuffix(reply, "#")
	if len(body) == 0 {
		return "", false
	}
	switch body[0] {
	case TrackingAltAz:
		return "alt-az", true
	case TrackingEQNorth:
		return "sidereal", true
	case TrackingEQSouth:
		return "sidereal-south", true
	}
	return "off", false
}

// GotoRADec slews to J2000 coordinates, converting to the mount's own equinox on the way out.
func (m *Mount) GotoRADec(ctx context.Context, raDeg, decDeg float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return device.ErrNotConnected
	}
	if aligned, err := m.commandLocked("J"); err == nil && !parseAligned(aligned) {
		return ErrNotAligned
	}
	raNow, decNow := astro.PrecessFromJ2000(raDeg, decDeg, m.now())
	_, err := m.commandLocked("r" + EncodeRADec(raNow, decNow))
	return err
}

// Sync tells the mount it is really pointing at these J2000 coordinates — the plate-solve correction.
func (m *Mount) Sync(ctx context.Context, raDeg, decDeg float64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return device.ErrNotConnected
	}
	raNow, decNow := astro.PrecessFromJ2000(raDeg, decDeg, m.now())
	_, err := m.commandLocked("s" + EncodeRADec(raNow, decNow))
	return err
}

// Abort cancels a GoTo. It is the STOP button, so it does the least possible work and never
// pre-checks anything that could fail first.
func (m *Mount) Abort(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return device.ErrNotConnected
	}
	_, err := m.commandLocked("M")
	return err
}

// Jog starts (rate 1–9) or stops (rate 0) a manual slew on one axis.
func (m *Mount) Jog(ctx context.Context, dir device.Direction, rate int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return device.ErrNotConnected
	}
	var axis int
	var positive bool
	switch dir {
	case device.DirNorth:
		axis, positive = axisAltDec, true
	case device.DirSouth:
		axis, positive = axisAltDec, false
	case device.DirEast:
		axis, positive = axisAzmRA, true
	case device.DirWest:
		axis, positive = axisAzmRA, false
	default:
		return fmt.Errorf("unknown direction %q", dir)
	}
	_, err := m.rawLocked(fixedRateCommand(axis, rate, positive))
	return err
}

// guideRateArcsecPerSec is the speed a dither nudge is applied at: fast enough not to waste a minute
// of the night, slow enough that the mount settles quickly afterwards. Roughly half sidereal.
const guideRateArcsecPerSec = 8.0

// Nudge moves by an exact angular amount, by running an axis at guide rate for the time the distance
// takes. Small moves on a GEM are eaten by backlash, which is why the caller measures what actually
// happened from the next frame rather than trusting this.
func (m *Mount) Nudge(ctx context.Context, dRAArcsec, dDecArcsec float64) error {
	if err := m.nudgeAxis(ctx, axisAzmRA, dRAArcsec); err != nil {
		return err
	}
	return m.nudgeAxis(ctx, axisAltDec, dDecArcsec)
}

func (m *Mount) nudgeAxis(ctx context.Context, axis int, arcsec float64) error {
	if arcsec == 0 {
		return nil
	}
	rate := guideRateArcsecPerSec
	if arcsec < 0 {
		rate = -rate
	}
	duration := time.Duration(absFloat(arcsec) / guideRateArcsecPerSec * float64(time.Second))

	m.mu.Lock()
	if m.port == nil {
		m.mu.Unlock()
		return device.ErrNotConnected
	}
	_, err := m.rawLocked(slewRateCommand(axis, rate))
	m.mu.Unlock()
	if err != nil {
		return err
	}

	select {
	case <-ctx.Done():
	case <-time.After(duration):
	}

	// Stopping matters more than starting: a nudge that never stops is a mount that walks away.
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return nil
	}
	_, err = m.rawLocked(slewRateCommand(axis, 0))
	return err
}

// SetTracking switches the drive on or off. Celestron's notes warn that slew commands conflict with
// tracking, so the sequencer turns it off around large moves and back on afterwards.
func (m *Mount) SetTracking(ctx context.Context, on bool, rate string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return device.ErrNotConnected
	}
	mode := byte(TrackingOff)
	if on {
		mode = TrackingEQNorth
		if strings.Contains(strings.ToLower(rate), "south") {
			mode = TrackingEQSouth
		} else if strings.Contains(strings.ToLower(rate), "alt") {
			mode = TrackingAltAz
		}
	}
	_, err := m.rawLocked([]byte{'T', mode})
	return err
}

// commandLocked sends an ASCII command and reads its reply. Caller holds m.mu.
func (m *Mount) commandLocked(cmd string) (string, error) {
	return m.rawLocked([]byte(cmd))
}

// rawBinaryLocked writes a frame and reads a FIXED number of reply bytes, then the '#' terminator.
// Caller holds m.mu.
//
// This exists because rawLocked scans for '#', and a pass-through reply is raw binary: a PEC bin
// holding the value 35 IS '#' (0x23). Scanning would take that value for the terminator, hand back an
// empty body, and leave the real '#' sitting in the port buffer — after which every later command
// reads the previous one's reply, forever. Binary replies must be read by LENGTH, never by delimiter.
func (m *Mount) rawBinaryLocked(frame []byte, want int) ([]byte, error) {
	if m.port == nil {
		return nil, device.ErrNotConnected
	}
	if _, err := m.port.Write(frame); err != nil {
		return nil, fmt.Errorf("write %v: %w", frame, err)
	}
	out := make([]byte, 0, want+1)
	buf := make([]byte, want+1)
	deadline := time.Now().Add(replyTimeout)
	for len(out) < want+1 {
		n, err := m.port.Read(buf[:want+1-len(out)])
		if n > 0 {
			out = append(out, buf[:n]...)
			continue
		}
		if err != nil && err != io.EOF {
			return out, fmt.Errorf("read %d-byte reply to %v: %w", want, frame, err)
		}
		if time.Now().After(deadline) {
			return out, fmt.Errorf("no reply to %v within %s", frame, replyTimeout)
		}
	}
	if out[want] != '#' {
		return nil, fmt.Errorf("reply to %v was not '#'-terminated: %v", frame, out)
	}
	return out[:want], nil
}

// rawLocked writes a frame and reads until the '#' terminator. Caller holds m.mu.
func (m *Mount) rawLocked(frame []byte) (string, error) {
	if m.port == nil {
		return "", device.ErrNotConnected
	}
	if _, err := m.port.Write(frame); err != nil {
		return "", fmt.Errorf("write %q: %w", frame, err)
	}
	var out []byte
	buf := make([]byte, 32)
	deadline := time.Now().Add(replyTimeout)
	for {
		n, err := m.port.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
			if idx := indexByte(out, '#'); idx >= 0 {
				return string(out[:idx+1]), nil
			}
		}
		if err != nil {
			if err == io.EOF && time.Now().Before(deadline) {
				continue
			}
			return string(out), fmt.Errorf("read reply to %q: %w", frame, err)
		}
		if time.Now().After(deadline) {
			return string(out), fmt.Errorf("no reply to %q within %s", frame, replyTimeout)
		}
	}
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
