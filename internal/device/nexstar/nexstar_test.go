package nexstar

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/astro"
	"github.com/verove-jordan/astronomy/internal/device"
)

var _ device.Mount = (*Mount)(nil)

// The wire format, checked against Celestron's own published worked example. Getting the encoding
// wrong does not fail loudly — it slews somewhere plausible but wrong.
func TestEncodeDecodeAngle(t *testing.T) {
	// From the protocol document: 12AB0500 → 313 197 824 / 2^32 → 0.0729 rev → 26.252°.
	got, err := decodeAngle("12AB0500")
	require.NoError(t, err)
	assert.InDelta(t, 26.252, got, 0.001)

	tests := []float64{0, 15, 90, 180, 270, 359.9, 41.2687, 314.75}
	for _, deg := range tests {
		hex := encodeAngle(deg)
		assert.Len(t, hex, 8, "the precise form is 8 hex digits")
		assert.True(t, strings.HasSuffix(hex, "00"),
			"the encoders carry 24 bits, so the low byte is always zero (%s)", hex)
		back, err := decodeAngle(hex)
		require.NoError(t, err)
		// One unit is 360/2^24 ≈ 0.0000215°, so a round trip is good to well under an arcsecond.
		assert.InDelta(t, deg, back, 0.0001, "round trip of %.4f", deg)
	}
}

func TestEncodeAngle_WrapsNegatives(t *testing.T) {
	assert.Equal(t, encodeAngle(350), encodeAngle(-10))
	assert.Equal(t, encodeAngle(0), encodeAngle(360))
}

// Declination comes back as a plain fraction of a revolution: without the quadrant fold a mount
// pointing at −10° reports +350°, and one past the pole reports a mirrored value.
func TestDecodeRADec_FoldsDeclination(t *testing.T) {
	tests := []struct {
		name   string
		raDeg  float64
		decDeg float64
	}{
		{"northern", 10.6847, 41.2687},
		{"southern", 100, -35.5},
		{"equator", 200, 0},
		{"near the pole", 37.95, 89.26},
		{"just below zero", 300, -0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reply := EncodeRADec(tt.raDeg, tt.decDeg) + "#"
			ra, dec, err := DecodeRADec(reply)
			require.NoError(t, err)
			assert.InDelta(t, tt.raDeg, ra, 0.001)
			assert.InDelta(t, tt.decDeg, dec, 0.001)
		})
	}
}

func TestDecodeRADec_RejectsGarbage(t *testing.T) {
	_, _, err := DecodeRADec("nonsense#")
	assert.Error(t, err)
	_, _, err = DecodeRADec("12AB0500#")
	assert.Error(t, err, "a position reply has two comma-separated values")
}

// `J` answers with a BINARY 0/1 byte and `L` with the ASCII characters '0'/'1'. Reading either with
// the other's parser is a silent, permanent wrong answer.
func TestAlignedAndSlewingUseDifferentAlphabets(t *testing.T) {
	assert.True(t, parseAligned(string([]byte{1, '#'})))
	assert.False(t, parseAligned(string([]byte{0, '#'})))
	assert.False(t, parseAligned("1#"), "ASCII '1' is 0x31, not the aligned flag")

	assert.True(t, parseSlewing("1#"))
	assert.False(t, parseSlewing("0#"))
	assert.False(t, parseSlewing(string([]byte{1, '#'})), "a binary 1 is not the slewing reply")
}

func TestParseVersionModelAndPierSide(t *testing.T) {
	assert.Equal(t, "5.30", parseVersion(string([]byte{5, 30, '#'})))
	assert.Equal(t, "Advanced VX", parseModel(string([]byte{20, '#'})))
	assert.Equal(t, "model 99", parseModel(string([]byte{99, '#'})))
	assert.Equal(t, "east", parsePierSide("E#"))
	assert.Equal(t, "west", parsePierSide("W#"))
	assert.Empty(t, parsePierSide("?#"))
}

// The variable-rate frame, against Celestron's worked example: 150″/s → high byte 2, low byte 88.
func TestSlewRateCommand(t *testing.T) {
	cmd := slewRateCommand(axisAzmRA, 150)
	require.Len(t, cmd, 8)
	assert.Equal(t, byte('P'), cmd[0])
	assert.Equal(t, byte(3), cmd[1])
	assert.Equal(t, byte(16), cmd[2], "azimuth/RA motor")
	assert.Equal(t, byte(6), cmd[3], "positive direction")
	assert.Equal(t, byte(2), cmd[4])
	assert.Equal(t, byte(88), cmd[5])

	neg := slewRateCommand(axisAltDec, -150)
	assert.Equal(t, byte(17), neg[2], "altitude/declination motor")
	assert.Equal(t, byte(7), neg[3], "negative direction")
	assert.Equal(t, byte(2), neg[4])
	assert.Equal(t, byte(88), neg[5])
}

func TestFixedRateCommand(t *testing.T) {
	cmd := fixedRateCommand(axisAzmRA, 9, true)
	assert.Equal(t, []byte{'P', 2, 16, 36, 9, 0, 0, 0}, cmd)
	stop := fixedRateCommand(axisAltDec, 0, false)
	assert.Equal(t, []byte{'P', 2, 17, 37, 0, 0, 0, 0}, stop)
	assert.Equal(t, byte(9), fixedRateCommand(axisAzmRA, 42, true)[4], "rate is clamped to 9")
}

// --- driver behaviour, against a fake hand controller -------------------------------------------

// fakeHC is a scriptable stand-in for the hand controller: it records what was sent and answers the
// way a real one does, so the driver's conversation can be asserted without hardware.
type fakeHC struct {
	mu      sync.Mutex
	sent    []string
	pending []byte
	aligned bool
	slewing bool
	// trackMode is what the `t` query answers — the drive mode a nudge has to put back.
	trackMode byte
	raNow     float64
	decNow    float64
	failEcho  bool

	// The PEC table and the worm's position in it. pecBin advances on every read, the way a turning
	// worm would, so a caller polling for phase sees it move.
	pecTable      []int8
	pecBin        int
	pecIndexed    bool
	pecPlaying    bool
	pecRecStopped bool
	// pecCorrupt forces one bin to read back as something other than what was written, to exercise
	// the write verification.
	pecCorrupt map[int]int8

	// guideRate is the motor controller's autoguide-rate byte, in 1/256ths of sidereal. There is one
	// per motor because that is how the hardware stores it, and a pair that disagree is exactly what
	// the audit exists to notice.
	guideRate    byte
	guideRateDec byte

	// site and clock are the hand controller's own stored values, as the eight raw bytes `w` and `h`
	// answer with. Held encoded so the fake exercises the same wire format the driver parses.
	site  []byte
	clock []byte

	// motorVersion is what each motor controller answers to the version command.
	motorVersion [2]byte
}

func newFakeHC() *fakeHC {
	return &fakeHC{
		aligned: true, raNow: 10, decNow: 41,
		pecTable: make([]int8, 88), pecIndexed: true,
		guideRate: 128, guideRateDec: 128, // half sidereal, as mounts tend to ship
		trackMode:    TrackingEQNorth,
		site:         encodeSite(Site{LatDeg: 48.8566, LonDeg: 2.3522})[1:],
		clock:        encodeClock(time.Now())[1:],
		motorVersion: [2]byte{7, 11},
	}
}

func (f *fakeHC) Write(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, string(p))
	f.pending = append(f.pending, f.replyTo(p)...)
	return len(p), nil
}

func (f *fakeHC) replyTo(p []byte) []byte {
	switch {
	case len(p) >= 2 && p[0] == 'K':
		if f.failEcho {
			return []byte("?#")
		}
		return []byte{p[1], '#'}
	case p[0] == 'V':
		return []byte{5, 30, '#'}
	case p[0] == 'm':
		return []byte{20, '#'}
	case p[0] == 'e':
		return []byte(EncodeRADec(f.raNow, f.decNow) + "#")
	case p[0] == 'J':
		if f.aligned {
			return []byte{1, '#'}
		}
		return []byte{0, '#'}
	case p[0] == 'L':
		if f.slewing {
			return []byte("1#")
		}
		return []byte("0#")
	case p[0] == 't':
		return []byte{f.trackMode, '#'}
	case p[0] == 'p':
		return []byte("W#")
	case p[0] == 'w':
		return append(append([]byte{}, f.site...), '#')
	case p[0] == 'W' && len(p) >= 9:
		f.site = append([]byte{}, p[1:9]...)
		return []byte("#")
	case p[0] == 'h':
		return append(append([]byte{}, f.clock...), '#')
	case p[0] == 'H' && len(p) >= 9:
		f.clock = append([]byte{}, p[1:9]...)
		return []byte("#")
	case p[0] == 'r':
		// A real mount starts slewing; the fake jumps to the commanded position.
		if ra, dec, err := DecodeRADec(string(p[1:])); err == nil {
			f.raNow, f.decNow = ra, dec
		}
		return []byte("#")
	case p[0] == 'P' && len(p) >= 8:
		if reply, ok := f.replyPEC(p); ok {
			return reply
		}
		if reply, ok := f.replyGuide(p); ok {
			return reply
		}
		// Jog and Nudge also send pass-through frames; they get the bare acknowledgement they
		// always did.
		return []byte("#")
	default:
		return []byte("#")
	}
}

// replyPEC answers the periodic-error commands and only those, so the rate/jog frames that share the
// `P` opcode keep their original bare-'#' reply.
func (f *fakeHC) replyPEC(p []byte) ([]byte, bool) {
	switch p[3] {
	case mcPECReadData:
		if p[4] == pecCountSelector {
			return []byte{byte(len(f.pecTable)), '#'}, true
		}
		i := int(p[4] - pecBinOffset)
		if i < 0 || i >= len(f.pecTable) {
			return []byte("#"), true
		}
		v := f.pecTable[i]
		if bad, ok := f.pecCorrupt[i]; ok {
			v = bad
		}
		return []byte{byte(v), '#'}, true
	case mcPECWriteData:
		i := int(p[4] - pecBinOffset)
		if i >= 0 && i < len(f.pecTable) {
			f.pecTable[i] = int8(p[5])
		}
		return []byte("#"), true
	case mcPECBin:
		bin := f.pecBin
		f.pecBin = (f.pecBin + 1) % len(f.pecTable)
		return []byte{byte(bin), '#'}, true
	case mcAtIndex:
		if f.pecIndexed {
			return []byte{0xFF, '#'}, true
		}
		return []byte{0x00, '#'}, true
	case mcSeekIndex:
		return []byte("#"), true
	case mcPECPlayback:
		f.pecPlaying = p[4] == 1
		return []byte("#"), true
	case mcPECRecordStop:
		f.pecRecStopped = true
		return []byte("#"), true
	case mcGetVersion:
		return []byte{f.motorVersion[0], f.motorVersion[1], '#'}, true
	}
	return nil, false
}

func (f *fakeHC) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.pending) == 0 {
		return 0, nil
	}
	n := copy(p, f.pending)
	f.pending = f.pending[n:]
	return n, nil
}

func (f *fakeHC) Close() error                       { return nil }
func (f *fakeHC) SetReadTimeout(time.Duration) error { return nil }
func (f *fakeHC) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
}
func (f *fakeHC) sentPrefixed(prefix byte) (string, bool) {
	for _, c := range f.commands() {
		if len(c) > 0 && c[0] == prefix {
			return c, true
		}
	}
	return "", false
}

func testMount(t *testing.T, hc *fakeHC) *Mount {
	t.Helper()
	// The real inter-write settle guards against dropped bytes on a 9600-baud link; against a fake
	// it only costs 88 sleeps per write.
	old := pecWriteSettle
	pecWriteSettle = 0
	t.Cleanup(func() { pecWriteSettle = old })

	m := New("/dev/fake", func(string) (Port, error) { return hc, nil })
	m.now = func() time.Time { return time.Date(2026, 7, 27, 22, 0, 0, 0, time.UTC) }
	require.NoError(t, m.Connect(context.Background()))
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestConnect_HandshakesAndReadsIdentity(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)

	assert.True(t, m.Connected())
	cmds := hc.commands()
	require.NotEmpty(t, cmds)
	// The handshake used to be the literal "Kx". It is now "K" plus a RANDOM byte, and the change is
	// the point rather than an incidental refactor: an echo of a fixed byte always asks for the same
	// answer, so a stale "x#" left in the buffer by a process that died mid-command passes the
	// handshake and leaves the stream one reply behind for the rest of the session. This assertion
	// therefore pins the shape and the safety of the byte, not its value. See link.go.
	require.Len(t, cmds[0], 2)
	assert.Equal(t, byte('K'), cmds[0][0], "the echo handshake proves a mount is really there")
	assert.NotContains(t, []byte{'#', 0x00, 0x11, 0x13}, cmds[0][1],
		"the echo byte must never be the terminator or a flow-control character")

	st, err := m.State(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "Advanced VX", st.Model)
	assert.Equal(t, "5.30", st.Firmware)
	assert.Equal(t, "west", st.PierSide)
	assert.True(t, st.Aligned)
	assert.True(t, st.Tracking)
}

// An open serial port proves nothing — a USB adapter with nothing plugged into it opens happily.
func TestConnect_RefusesWhenNothingAnswers(t *testing.T) {
	hc := newFakeHC()
	hc.failEcho = true
	m := New("/dev/fake", func(string) (Port, error) { return hc, nil })
	err := m.Connect(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, device.ErrDriverUnavailable)
	assert.False(t, m.Connected())
}

// The mount speaks the equinox of date; this app speaks J2000. The conversion has to happen on BOTH
// legs, or a GoTo lands a third of a degree off in 2026 — twenty times the field of a pixel.
func TestGotoAndState_ConvertBetweenEpochs(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)
	when := m.now()

	const targetRA, targetDec = 10.6847, 41.2687
	require.NoError(t, m.GotoRADec(context.Background(), targetRA, targetDec))

	sent, ok := hc.sentPrefixed('r')
	require.True(t, ok, "a GoTo must have been sent")
	sentRA, sentDec, err := DecodeRADec(sent[1:])
	require.NoError(t, err)

	wantRA, wantDec := astro.PrecessFromJ2000(targetRA, targetDec, when)
	assert.InDelta(t, wantRA, sentRA, 0.001, "the mount is commanded in ITS epoch")
	assert.InDelta(t, wantDec, sentDec, 0.001)
	assert.Greater(t, astro.AngularSeparation(targetRA, targetDec, sentRA, sentDec), 0.2,
		"the conversion must actually move the coordinates, not be a no-op")

	// …and reading back converts the other way, landing on the J2000 target again.
	st, err := m.State(context.Background())
	require.NoError(t, err)
	assert.InDelta(t, targetRA, st.RADeg, 0.001)
	assert.InDelta(t, targetDec, st.DecDeg, 0.001)
}

func TestGoto_RefusedWhenNotAligned(t *testing.T) {
	hc := newFakeHC()
	hc.aligned = false
	m := testMount(t, hc)

	err := m.GotoRADec(context.Background(), 100, 20)
	require.ErrorIs(t, err, ErrNotAligned)
	_, sentGoto := hc.sentPrefixed('r')
	assert.False(t, sentGoto, "nothing may be commanded to an unaligned mount")
}

func TestSync_SendsPreciseSyncInMountEpoch(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)

	require.NoError(t, m.Sync(context.Background(), 314.75, 44.31))
	sent, ok := hc.sentPrefixed('s')
	require.True(t, ok)
	ra, dec, err := DecodeRADec(sent[1:])
	require.NoError(t, err)
	wantRA, wantDec := astro.PrecessFromJ2000(314.75, 44.31, m.now())
	assert.InDelta(t, wantRA, ra, 0.001)
	assert.InDelta(t, wantDec, dec, 0.001)
}

func TestAbort_SendsCancel(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)
	require.NoError(t, m.Abort(context.Background()))
	_, ok := hc.sentPrefixed('M')
	assert.True(t, ok)
}

func TestJog_UsesFixedRateFrames(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)

	require.NoError(t, m.Jog(context.Background(), device.DirNorth, 5))
	found := false
	for _, c := range hc.commands() {
		if len(c) == 8 && c[0] == 'P' && c[1] == 2 && c[2] == 17 && c[3] == 36 && c[4] == 5 {
			found = true
		}
	}
	assert.True(t, found, "north at rate 5 is a fixed-rate frame on the declination motor")

	assert.Error(t, m.Jog(context.Background(), device.Direction("sideways"), 1))
}

// A nudge must always be followed by a stop: a rate command that is never cancelled is a mount that
// walks away across the sky.
func TestNudge_AlwaysStopsTheAxis(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)

	require.NoError(t, m.Nudge(context.Background(), 8, 0))

	var rateFrames [][]byte
	for _, c := range hc.commands() {
		if len(c) == 8 && c[0] == 'P' && c[1] == 3 {
			rateFrames = append(rateFrames, []byte(c))
		}
	}
	require.GreaterOrEqual(t, len(rateFrames), 2, "a move and a stop")
	last := rateFrames[len(rateFrames)-1]
	assert.Zero(t, last[4], "the final frame must command rate zero")
	assert.Zero(t, last[5])
}

// A nudge moves the axes with slew commands, and those stop the drive. Measured on fw 5.31: the
// mount goes on reporting tracking:true afterwards, so nothing downstream can notice. Dither nudges
// between subs, so failing to restore the drive trails every remaining frame of the night.
func TestNudge_RestoresTrackingAfterTheMove(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)

	require.NoError(t, m.Nudge(context.Background(), 8, 0))

	sent, ok := hc.sentPrefixed('T')
	require.True(t, ok, "the drive must be put back after a nudge")
	assert.Equal(t, byte(TrackingEQNorth), sent[1], "restored to the mode it was in, not a guess")

	// The restore must come after the last rate command, or it re-asserts tracking and then stops it.
	lastRate, lastTrack := -1, -1
	for i, c := range hc.commands() {
		if len(c) == 8 && c[0] == 'P' && c[1] == 3 {
			lastRate = i
		}
		if len(c) >= 2 && c[0] == 'T' {
			lastTrack = i
		}
	}
	assert.Greater(t, lastTrack, lastRate, "tracking is restored after the axis has stopped")
}

// A drive that was deliberately off must stay off: PEC training stops it on purpose, and so does the
// sequencer around a large slew. Restoring a mode we never saw could start a mount under someone.
func TestNudge_LeavesAStoppedDriveStopped(t *testing.T) {
	hc := newFakeHC()
	hc.trackMode = TrackingOff
	m := testMount(t, hc)

	require.NoError(t, m.Nudge(context.Background(), 8, 0))

	_, ok := hc.sentPrefixed('T')
	assert.False(t, ok, "no drive command may be sent when tracking was already off")
}

func TestSetTracking(t *testing.T) {
	hc := newFakeHC()
	m := testMount(t, hc)

	require.NoError(t, m.SetTracking(context.Background(), false, ""))
	sent, ok := hc.sentPrefixed('T')
	require.True(t, ok)
	assert.Equal(t, byte(TrackingOff), sent[1])

	hc.mu.Lock()
	hc.sent = nil
	hc.mu.Unlock()
	require.NoError(t, m.SetTracking(context.Background(), true, "sidereal"))
	sent, ok = hc.sentPrefixed('T')
	require.True(t, ok)
	assert.Equal(t, byte(TrackingEQNorth), sent[1])
}

func TestOperationsRequireAConnection(t *testing.T) {
	m := New("/dev/fake", func(string) (Port, error) { return newFakeHC(), nil })
	ctx := context.Background()
	assert.ErrorIs(t, m.Abort(ctx), device.ErrNotConnected)
	assert.ErrorIs(t, m.GotoRADec(ctx, 1, 1), device.ErrNotConnected)
	_, err := m.State(ctx)
	assert.ErrorIs(t, err, device.ErrNotConnected)
}

// Connect must not eat the sentinel that says WHY the port would not open.
//
// It used to: the open error was formatted with %v, which prints the sentinel's words while breaking
// the chain. Every errors.Is on a Connect failure therefore answered false, and a caller could not
// tell "another program holds the port" — the normal state while the device server runs — from a
// mount that is switched off. The words looked right, which is what made it survive.
func TestMount_Connect_KeepsTheReasonThePortWouldNotOpen(t *testing.T) {
	tests := []struct {
		name string
		open error
		want error
	}{
		{"busy", ErrPortBusy, ErrPortBusy},
		{"unconfigurable", ErrPortUnconfigurable, ErrPortUnconfigurable},
		{"gone", ErrLinkGone, ErrLinkGone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New("/dev/cu.test", func(string) (Port, error) {
				return nil, fmt.Errorf("%w: underlying detail", tt.open)
			})
			err := m.Connect(context.Background())
			require.Error(t, err)
			assert.ErrorIs(t, err, tt.want, "the sentinel must survive Connect's wrapping")
			assert.ErrorIs(t, err, device.ErrDriverUnavailable, "and so must the driver-level one")
		})
	}
}
