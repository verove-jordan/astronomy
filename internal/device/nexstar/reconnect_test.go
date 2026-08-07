package nexstar

import (
	"context"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/device"
)

// Getting the link back — and refusing to, when what came back is not the same mount.

// reconnectRig wires a driver to an opener that hands out a fresh port each time, so a test can kill
// the current one and watch the driver find its way back.
type reconnectRig struct {
	mu       sync.Mutex
	m        *Mount
	clk      *testClock
	ports    []*faultyPort
	opened   []string
	only     string         // when set, every other path is refused as gone
	newPort  func() *fakeHC // the hand controller behind the next port
	failNext int
	failWith error
}

func newReconnectRig(t *testing.T) *reconnectRig {
	t.Helper()
	r := &reconnectRig{clk: newTestClock(), newPort: newFakeHC}
	r.m = New("/dev/cu.usbserial-1420", r.open)
	r.m.now = func() time.Time { return time.Date(2026, 7, 30, 22, 0, 0, 0, time.UTC) }
	r.m.clock = r.clk.now
	r.m.sleep = func(time.Duration) {}
	r.m.rnd = rand.New(rand.NewSource(5))
	r.m.candidates = func() []PortInfo { return nil }
	require.NoError(t, r.m.Connect(context.Background()))
	t.Cleanup(func() { _ = r.m.Close() })
	return r
}

func (r *reconnectRig) open(path string) (Port, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.opened = append(r.opened, path)
	if r.failNext > 0 {
		r.failNext--
		if r.failWith != nil {
			return nil, r.failWith
		}
		return nil, ErrLinkGone
	}
	if r.only != "" && path != r.only {
		return nil, ErrLinkGone
	}
	fp := newFaultyPort(r.newPort())
	r.ports = append(r.ports, fp)
	return fp, nil
}

func (r *reconnectRig) last() *faultyPort {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ports[len(r.ports)-1]
}

func (r *reconnectRig) attempts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.opened)
}

func TestMount_Command_ReconnectsWhenThePortVanishes(t *testing.T) {
	r := newReconnectRig(t)
	r.last().kill() // the adapter is unplugged mid-session

	// The command that discovered the loss reports it — its own outcome is genuinely unknown.
	_, err := r.m.State(context.Background())
	require.Error(t, err)

	// But the link is already back, without anyone restarting anything.
	st, err := r.m.State(context.Background())
	require.NoError(t, err)
	assert.InDelta(t, 10.0, st.RADeg, 0.5)

	h := r.m.Health()
	assert.True(t, h.Connected)
	assert.EqualValues(t, 1, h.Reconnects, "every reconnect is counted; one that is not is a fault hidden")
}

func TestMount_Reconnect_FollowsTheHandControllerToANewPath(t *testing.T) {
	r := newReconnectRig(t)

	// macOS re-enumerates the bridge under a different name — the suffix is a USB location id, not a
	// device identity, so the remembered path is simply gone.
	r.only = "/dev/cu.usbserial-1430"
	r.m.candidates = func() []PortInfo {
		return []PortInfo{{Path: "/dev/cu.usbserial-1430", Label: "cu.usbserial-1430", Likely: true}}
	}
	r.last().kill()

	_, _ = r.m.State(context.Background())
	st, err := r.m.State(context.Background())
	require.NoError(t, err)
	assert.InDelta(t, 10.0, st.RADeg, 0.5)
	assert.Equal(t, "/dev/cu.usbserial-1430", r.m.Path())
}

// otherMountPort answers the handshake correctly but reports a different model, which is what a
// reconnect landing on the observatory's focuser or a USB GPS looks like.
type otherMountPort struct {
	model   byte
	pending []byte
}

func (p *otherMountPort) Write(b []byte) (int, error) {
	switch {
	case len(b) >= 2 && b[0] == 'K':
		p.pending = append(p.pending, b[1], '#')
	case b[0] == 'V':
		p.pending = append(p.pending, 5, 30, '#')
	case b[0] == 'm':
		p.pending = append(p.pending, p.model, '#')
	default:
		p.pending = append(p.pending, '#')
	}
	return len(b), nil
}

func (p *otherMountPort) Read(b []byte) (int, error) {
	if len(p.pending) == 0 {
		return 0, nil
	}
	n := copy(b, p.pending)
	p.pending = p.pending[n:]
	return n, nil
}

func (p *otherMountPort) Close() error                       { return nil }
func (p *otherMountPort) SetReadTimeout(time.Duration) error { return nil }
func (p *otherMountPort) ResetInputBuffer() error            { p.pending = nil; return nil }

func TestMount_Reconnect_RefusesAMountThatIsNotTheOneWeHad(t *testing.T) {
	clk := newTestClock()
	first := newFaultyPort(newFakeHC())
	n := 0
	m := New("/dev/cu.usbserial-1420", func(string) (Port, error) {
		n++
		if n == 1 {
			return first, nil
		}
		return &otherMountPort{model: 12}, nil // a NexStar 6/8 SE, not the Advanced VX we had
	})
	m.now = func() time.Time { return time.Date(2026, 7, 30, 22, 0, 0, 0, time.UTC) }
	m.clock, m.sleep, m.rnd = clk.now, func(time.Duration) {}, rand.New(rand.NewSource(2))
	m.candidates = func() []PortInfo { return nil }
	require.NoError(t, m.Connect(context.Background()))
	t.Cleanup(func() { _ = m.Close() })

	first.kill()
	_, err := m.State(context.Background())
	require.Error(t, err)

	assert.False(t, m.Connected(),
		"answering the handshake is not enough; a different device must not inherit the session")
	assert.Contains(t, m.Health().LastError, "Advanced VX")
}

func TestMount_Close_DoesNotTriggerAReconnect(t *testing.T) {
	r := newReconnectRig(t)
	before := r.attempts()

	require.NoError(t, r.m.Close())
	// Give any goroutine that wrongly started a chance to try.
	time.Sleep(20 * time.Millisecond)

	assert.Equal(t, before, r.attempts(),
		"our own disconnect is indistinguishable from an unplug in the error alone — hence the closing flag")
	assert.False(t, r.m.Connected())
}

func TestMount_Reconnect_FlushesAPendingStopBeforeAnythingElse(t *testing.T) {
	r := newReconnectRig(t)

	// An axis is turning when the cable goes.
	require.NoError(t, r.m.Jog(context.Background(), device.DirNorth, 7))
	require.Equal(t, 1, r.m.stopsPending())
	r.last().kill()

	_, _ = r.m.State(context.Background()) // discovers the loss and reconnects

	require.True(t, r.m.Connected())
	assert.Equal(t, 0, r.m.stopsPending(), "the axis must be stopped by the reconnect, not left turning")

	// And the stop must be the FIRST thing on the new port, before any status read borrows it.
	sent := r.last().sent()
	require.NotEmpty(t, sent)
	var firstNonHandshake []byte
	for _, w := range sent {
		if len(w) > 0 && w[0] != 'K' && w[0] != 'V' && w[0] != 'm' {
			firstNonHandshake = w
			break
		}
	}
	require.NotNil(t, firstNonHandshake)
	assert.Equal(t, retryAlways, classify(firstNonHandshake),
		"everything else can wait; a slewing mount cannot")
}

func TestMount_Reconnect_MarksTheAlignmentStaleAfterAPowerCycle(t *testing.T) {
	r := newReconnectRig(t)

	// The mount comes back unaligned: that is a power cycle, not a cable.
	r.newPort = func() *fakeHC {
		hc := newFakeHC()
		hc.aligned = false
		return hc
	}
	r.last().kill()
	_, _ = r.m.State(context.Background())
	require.True(t, r.m.Connected())

	err := r.m.GotoRADec(context.Background(), 83.8, -5.4)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAlignmentLost,
		"'re-align' and 'not aligned yet' are different instructions at three in the morning")
}

func TestMount_Reconnect_DoesNotSilentlyResumePECPlayback(t *testing.T) {
	r := newReconnectRig(t)
	require.NoError(t, r.m.PECPlayback(context.Background(), true))

	r.last().kill()
	_, _ = r.m.State(context.Background())
	require.True(t, r.m.Connected())

	st, err := r.m.PECStatus(context.Background())
	require.NoError(t, err)
	assert.False(t, st.Playing,
		"a curve replayed against an unknown index phase tracks WORSE than no curve at all")
}

func TestMount_Reconnect_BacksOffWithoutBusyLooping(t *testing.T) {
	r := newReconnectRig(t)

	var waits []time.Duration
	r.m.sleep = func(d time.Duration) { waits = append(waits, d) }
	r.failNext = 6 // the immediate attempt plus five from the loop
	r.last().kill()

	_, _ = r.m.State(context.Background())

	// The loop runs in its own goroutine, outside the mutex — a backoff held under the lock would put
	// Abort, the STOP button, behind a thirty-second sleep.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !r.m.Connected() {
		time.Sleep(5 * time.Millisecond)
	}
	require.True(t, r.m.Connected(), "the reconnect must keep trying rather than give up")

	require.NotEmpty(t, waits)
	for i, w := range waits {
		assert.LessOrEqual(t, w, time.Duration(float64(reconnectBackoffMax)*(1+reconnectJitter)))
		if i > 0 {
			assert.GreaterOrEqual(t, w, waits[i-1]/2, "the pause must grow, not oscillate")
		}
	}
}

func TestMount_Reconnect_NamesAPortHeldByAnotherProgram(t *testing.T) {
	r := newReconnectRig(t)
	r.failNext, r.failWith = 1, ErrPortBusy
	r.last().kill()

	_, err := r.m.State(context.Background())
	require.Error(t, err)

	// The message the user sees must send them to the right place: another program, not a broken
	// cable. Swallowing the open error would leave the panel saying "no mount found" while CPWI sits
	// on the port two windows away.
	assert.Contains(t, r.m.Health().LastError, ErrPortBusy.Error())
}
