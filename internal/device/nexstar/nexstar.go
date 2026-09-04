package nexstar

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
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

// ErrAlignmentLost is the same refusal, but for the case worth naming separately: the mount WAS
// aligned, the link dropped, and it came back saying it is not. That is a power cycle rather than a
// cable, so the answer is not "try again" but "re-align" — and telling the two apart at 3am is the
// difference between a fixable night and a confusing one.
var ErrAlignmentLost = errors.New("the mount appears to have been power-cycled since it was aligned — re-align before slewing")

// Port is the serial link, narrowed to what the driver needs so tests can supply a fake.
type Port interface {
	io.ReadWriteCloser
	SetReadTimeout(d time.Duration) error
	// ResetInputBuffer discards bytes that arrived before we were listening.
	//
	// It is on the interface rather than type-asserted for the reason device.go:220 records about
	// SetFilterNames: a driver that quietly lacked the method would degrade every session instead of
	// failing, and a missing flush degrades in the worst possible way. A port that merely opens is
	// not empty — a run killed mid-command, or CPWI disconnecting, leaves the hand controller's
	// answer sitting in the kernel's queue, and the first command we send reads THAT as its reply.
	// From then on every answer belongs to the previous question, forever, silently.
	ResetInputBuffer() error
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

	// trackMode is the last drive mode this driver actually SAW or commanded, and trackSeen says
	// whether it has ever seen one. Guiding needs the mode after every pulse (a variable-rate slew
	// leaves the drive stopped — see Nudge), and guiding fires constantly: asking the mount each time
	// would put two extra round trips on a 9600-baud link on every correction of the night. So it is
	// remembered instead, from the `t` reads State already makes and from what SetTracking wrote.
	//
	// Cleared on connect. A mount that has been power-cycled behind our back has a mode we have not
	// seen, and re-asserting a remembered one could start a drive somebody deliberately stopped.
	trackMode byte
	trackSeen bool

	// The mount cannot be asked whether PEC is playing or seeking, so the driver remembers what it
	// last commanded. A mount power-cycled behind our back reads as "not playing", which is the safe
	// direction to be wrong in.
	pecPlaying bool
	pecSeeking bool

	// now is the clock used for precession; injectable so tests are not time-dependent.
	now func() time.Time
	// clock is the wall clock used for timeouts, backoff and health. It is deliberately SEPARATE
	// from now: tests freeze now so precession is deterministic, and a frozen clock inside a read
	// loop never reaches its deadline. Unifying the two does not tidy anything — it hangs.
	clock func() time.Time
	// sleep is how the reconnect backoff waits; injectable so tests do not spend real seconds.
	sleep func(time.Duration)
	// rnd draws the handshake's echo bytes. Injectable so a test can assert the bytes differ without
	// being flaky about it.
	rnd *rand.Rand

	stats linkStats

	// closing distinguishes "we closed the port" from "the adapter vanished". The two are
	// indistinguishable in the error the serial library returns, and without the flag the reconnect
	// loop fights every deliberate disconnect.
	closing bool
	// connecting suppresses recovery while Connect is itself running, which would otherwise recurse.
	connecting   bool
	reconnecting bool
	// stopped closes when the driver is shut down, so the background goroutines end with it.
	stopped chan struct{}

	// alignmentStale records that the mount reported itself aligned before a reconnect and unaligned
	// after. That is a power cycle, and a GoTo against the alignment it no longer has would point
	// somewhere arbitrary.
	alignmentStale bool

	// candidates lists the ports a reconnect may try. Injectable because ListPorts deliberately only
	// sees /dev/cu.* and /dev/tty.* devices, which the pseudo-terminal loopback tests are not.
	candidates func() []PortInfo

	// stops holds, per axis, the frame that would halt it and the moment it must be sent by. See
	// watchdog.go — this is what keeps a mount from running away when the link drops mid-move.
	stops map[int]*pendingStop
	// deadman overrides how long an unrenewed jog may run; zero uses the default.
	deadman time.Duration
	// timeout overrides how long one reply may take; zero uses the protocol's documented worst case.
	timeout         time.Duration
	watchdogRunning bool
	// shutdown records that Close has run, so the background goroutines are not restarted and a
	// later Connect knows to hand them a fresh stop channel.
	shutdown bool
}

// New builds a driver for a serial device path. Nothing is opened until Connect.
func New(path string, opener Opener) *Mount {
	if opener == nil {
		opener = openSerial
	}
	return &Mount{
		path:       path,
		opener:     opener,
		now:        time.Now,
		clock:      time.Now,
		sleep:      time.Sleep,
		rnd:        rand.New(rand.NewSource(time.Now().UnixNano())),
		candidates: ListPorts,
		stops:      map[int]*pendingStop{},
		stopped:    make(chan struct{}),
	}
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
	return m.connectLocked()
}

// connectLocked is the body of Connect, split out because a reconnect performs exactly the same
// sequence and the two must not be allowed to drift apart.
func (m *Mount) connectLocked() error {
	if m.path == "" {
		return fmt.Errorf("%w: no serial port selected", device.ErrDriverUnavailable)
	}
	port, err := m.opener(m.path)
	if err != nil {
		// BOTH wrapped, not %v for the second: the open error is the only thing carrying
		// ErrPortBusy / ErrPortUnconfigurable / ErrLinkGone, and formatting it with %v prints the
		// sentinel's text while breaking the chain — so every errors.Is on a Connect failure
		// silently answered false, and the sentinels serial.go went to the trouble of defining
		// could only ever be matched by code that opened a port itself.
		return fmt.Errorf("%w: %w", device.ErrDriverUnavailable, err)
	}
	m.port = port
	m.closing = false
	// A Mount that was closed and is being connected again needs a fresh shutdown channel, or the
	// watchdog and the reconnect loop would see the old, already-closed one and decline to start.
	if m.shutdown || m.stopped == nil {
		m.shutdown = false
		m.stopped = make(chan struct{})
		m.watchdogRunning = false
	}
	_ = port.SetReadTimeout(replyTimeout)

	// The echo is the handshake: a serial port that opens proves nothing (a USB adapter with nothing
	// attached opens happily), but a mount echoes back exactly what it was sent. Recovery is
	// suppressed for the duration — a failed handshake must report itself, not start a reconnect
	// loop that would call straight back into here.
	m.connecting = true
	hsErr := m.handshakeLocked(handshakeAttempts)
	m.connecting = false
	if hsErr != nil {
		_ = port.Close()
		m.port = nil
		return fmt.Errorf("%w: no NexStar mount answered on %s (%w)", device.ErrDriverUnavailable, m.path, hsErr)
	}

	if v, err := m.commandLocked("V"); err == nil {
		m.firmware = parseVersion(v)
	}
	if mm, err := m.commandLocked("m"); err == nil {
		m.model, m.modelCode = parseModel(mm), parseModelCode(mm)
	}
	// Whatever drive mode was remembered belonged to the session that just ended.
	m.trackSeen = false
	m.stats.connectedAt = m.clock()
	m.stats.lastOK = m.clock()
	return nil
}

func (m *Mount) Close() error {
	m.mu.Lock()
	// Recorded before the port is touched: a Read already blocked in another goroutine will fail the
	// moment the descriptor goes, and without this flag that failure is indistinguishable from an
	// unplugged cable — so the reconnect loop would fight every deliberate disconnect.
	m.closing = true
	m.stops = map[int]*pendingStop{}
	if m.stopped != nil && !m.shutdown {
		close(m.stopped)
	}
	m.shutdown = true
	m.watchdogRunning = false
	port := m.port
	m.port = nil
	m.mu.Unlock()

	if port == nil {
		return nil
	}
	return port.Close()
}

func (m *Mount) Connected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.port != nil
}

// Model and Firmware report what the mount said about itself at Connect. They are cached rather
// than re-read because they cannot change while the port is open, and because a reconnect compares
// them against these values to prove it found the SAME mount rather than a different device that
// happened to be handed the old device path.
func (m *Mount) Model() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.model
}

func (m *Mount) Firmware() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.firmware
}

// Path reports the serial device the driver is using.
func (m *Mount) Path() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.path
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
	// Altitude and azimuth come from the mount rather than being computed here, because the mount
	// derives them from ITS site and clock — which is exactly what a user needs to see when those are
	// wrong. MountState has always declared these fields and the simulator has always filled them; on
	// real hardware they read a constant zero until now.
	if az, err := m.commandLocked("z"); err == nil {
		if azDeg, altDeg, derr := DecodeAzAlt(az); derr == nil {
			st.AzDeg, st.AltDeg = azDeg, altDeg
		}
	}
	if s, err := m.commandLocked("L"); err == nil {
		st.Slewing = parseSlewing(s)
	}
	if a, err := m.commandLocked("J"); err == nil {
		st.Aligned = parseAligned(a)
	}
	if t, err := m.commandLocked("t"); err == nil {
		st.TrackingRate, st.Tracking = trackingName(t)
		m.rememberTrackingLocked(t)
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
	if aligned, err := m.commandLocked("J"); err == nil {
		switch {
		case !parseAligned(aligned) && m.alignmentStale:
			return ErrAlignmentLost
		case !parseAligned(aligned):
			return ErrNotAligned
		default:
			// It is aligned again, so whatever happened has been dealt with by a human.
			m.alignmentStale = false
		}
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
	if _, err := m.rawLocked(fixedRateCommand(axis, rate, positive)); err != nil {
		// The frame may still have landed, so an axis started by a command that then failed is armed
		// anyway. Being wrong in this direction costs one redundant stop frame; being wrong the other
		// way costs a mount that keeps slewing.
		if rate > 0 {
			m.armStopLocked(axis, fixedRateCommand(axis, 0, positive), m.deadmanLocked())
		}
		return err
	}
	if rate > 0 {
		m.armStopLocked(axis, fixedRateCommand(axis, 0, positive), m.deadmanLocked())
	} else {
		m.disarmStopLocked(axis)
	}
	return nil
}

// guideRateArcsecPerSec is the speed a dither nudge is applied at: fast enough not to waste a minute
// of the night, slow enough that the mount settles quickly afterwards. Roughly half sidereal.
const guideRateArcsecPerSec = 8.0

// Nudge moves by an exact angular amount, by running an axis at guide rate for the time the distance
// takes. Small moves on a GEM are eaten by backlash, which is why the caller measures what actually
// happened from the next frame rather than trusting this.
func (m *Mount) Nudge(ctx context.Context, dRAArcsec, dDecArcsec float64) error {
	// Read the drive mode BEFORE moving, and put it back afterwards.
	//
	// nudgeAxis moves the axes with variable-rate slew commands, and Celestron's notes warn that
	// those conflict with tracking — see SetTracking. Measured on an AVX running firmware 5.31: a
	// nudge leaves the drive STOPPED, while the mount happily goes on answering the `t` query with
	// its old mode, so State() still reports tracking:true and nothing downstream notices.
	//
	// Dithering is the caller that makes this serious: it nudges between subs, so an unrestored
	// drive silently trails every remaining frame of the night at the full sidereal rate — 15"/s,
	// which is 75" of trail in a 5 s sub. The failure looks like a mount fault, not like dither.
	mode := m.trackingModeForRestore()
	defer m.restoreTracking(mode)

	if err := m.nudgeAxis(ctx, axisAzmRA, dRAArcsec); err != nil {
		return err
	}
	return m.nudgeAxis(ctx, axisAltDec, dDecArcsec)
}

// trackingModeForRestore reads the drive's current mode. It answers TrackingOff whenever the mode
// cannot be read, because re-asserting a mode we never actually saw would be worse than leaving the
// drive alone: it could start a mount the caller had deliberately stopped.
func (m *Mount) trackingModeForRestore() byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return TrackingOff
	}
	reply, err := m.rawLocked([]byte("t"))
	if err != nil || len(reply) == 0 {
		return TrackingOff
	}
	m.rememberTrackingLocked(reply)
	return reply[0]
}

// rememberTrackingLocked records a drive mode the mount just told us. Caller holds m.mu.
func (m *Mount) rememberTrackingLocked(reply string) {
	body := strings.TrimSuffix(reply, "#")
	if len(body) == 0 {
		return
	}
	m.trackMode, m.trackSeen = body[0], true
}

// trackingModeForGuide answers with the remembered drive mode, asking the mount only if it has never
// been seen.
//
// This is the guiding counterpart of trackingModeForRestore, and the difference is the whole point.
// A nudge happens between subs and can afford to ask; a guide pulse happens DURING one, dozens of
// times, and two extra round trips per correction is a real cost on a 9600-baud link that the same
// exposure's polling is already sharing. State() reads `t` about once a second while the mount panel
// is open, and SetTracking records what it wrote, so the cache is fresh without anyone paying for it.
func (m *Mount) trackingModeForGuide() byte {
	m.mu.Lock()
	if m.trackSeen {
		mode := m.trackMode
		m.mu.Unlock()
		return mode
	}
	m.mu.Unlock()
	// Never seen one: this is the first pulse of the session, so pay for it once.
	return m.trackingModeForRestore()
}

// restoreTracking puts the drive back into the mode it held before a nudge. A drive that was already
// off is left off — PEC training stops it on purpose, and so does the sequencer around a big slew.
func (m *Mount) restoreTracking(mode byte) {
	if mode == TrackingOff {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return // the link went; afterReopenLocked has a queued stop to flush before anything else
	}
	_, _ = m.rawLocked([]byte{'T', mode})
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
	// Armed BEFORE the axis is asked to move, and armed even if that command reports failure: the
	// frame may have landed anyway. From here the deadman owns the stop, so no path out of this
	// function can leave the motor running.
	m.armStopLocked(axis, slewRateCommand(axis, 0), duration+stopGrace)
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
		// The link went while the axis was turning. This used to return nil — reporting success
		// having never stopped the motor, which does not stop because a USB cable was pulled. The
		// stop stays armed instead, and reopenLocked sends it before the port is handed to anyone
		// else. The caller is told the truth in the meantime.
		return fmt.Errorf("%w: the link went while the axis was moving; the stop is queued for the reconnect", device.ErrNotConnected)
	}
	if _, err = m.rawLocked(slewRateCommand(axis, 0)); err != nil {
		return err
	}
	m.disarmStopLocked(axis)
	return nil
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
	if _, err := m.rawLocked([]byte{'T', mode}); err != nil {
		return err
	}
	m.trackMode, m.trackSeen = mode, true
	return nil
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
	return m.sendLocked(frame, want)
}

// rawLocked writes a frame and reads until the '#' terminator. Caller holds m.mu.
//
// Both framing helpers are thin wrappers over sendLocked so that the flush, resynchronisation and
// retry rules cover every command in the package — including the PEC and guiding frames, which are
// the ones sent most often over a whole night — without a single call site having to change.
//
// On any error the body comes back EMPTY. The previous version returned whatever had arrived so far
// alongside the error, and a caller that ignored the error would parse half a reply.
func (m *Mount) rawLocked(frame []byte) (string, error) {
	body, err := m.sendLocked(frame, -1)
	if err != nil {
		return "", err
	}
	return string(body), nil
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
