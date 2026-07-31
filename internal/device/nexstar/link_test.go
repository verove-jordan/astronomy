package nexstar

import (
	"context"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// What happens to the link when the wire misbehaves.
//
// These are the failures that end an unattended night: a reply that never comes, one that comes too
// late to belong to the command that asked for it, and line noise. Each test drives one of them
// through the real driver and asserts the recovery — and, where recovery is impossible, says so
// explicitly rather than pretending.

// testClock is the wall clock the driver uses for timeouts, deadlines and backoff.
//
// It advances on every read, which is what makes the 3.5-second reply timeout resolve in
// microseconds instead of really taking 3.5 seconds, and it can also be jumped forward so a test can
// reach a four-second deadman without waiting for one. It is deliberately NOT the same clock as the
// driver's astronomical `now` — freezing that one keeps precession deterministic, and freezing this
// one would hang every read loop.
type testClock struct {
	mu   sync.Mutex
	t    time.Time
	step time.Duration
}

func newTestClock() *testClock {
	return &testClock{t: time.Date(2026, 7, 30, 22, 0, 0, 0, time.UTC), step: 50 * time.Millisecond}
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(c.step)
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// faultyRig builds a connected driver whose transport can be broken on demand. The handshake has
// already happened by the time it returns, so faults injected afterwards land on the test's own
// commands rather than on Connect.
func faultyRig(t *testing.T, hc *fakeHC) (*Mount, *faultyPort, *testClock) {
	t.Helper()
	fp := newFaultyPort(hc)
	clk := newTestClock()
	m := New("/dev/fake", func(string) (Port, error) { return fp, nil })
	m.now = func() time.Time { return time.Date(2026, 7, 30, 22, 0, 0, 0, time.UTC) }
	m.clock = clk.now
	m.sleep = func(time.Duration) {}
	m.rnd = rand.New(rand.NewSource(7))
	require.NoError(t, m.Connect(context.Background()))
	t.Cleanup(func() { _ = m.Close() })
	return m, fp, clk
}

func TestMount_Connect_FlushesBytesLeftByAPreviousProcess(t *testing.T) {
	hc := newFakeHC()
	// Exactly what a run killed mid-command leaves behind: the answer to a question nobody is waiting
	// for any more. With the old fixed "Kx" handshake this stale "x#" would have been ACCEPTED as the
	// echo reply, and every command afterwards would have read the previous one's answer.
	hc.seed([]byte("x#"))

	m, _, _ := faultyRig(t, hc)

	// The proof is not that Connect succeeded — it is that the stream is in step afterwards.
	st, err := m.State(context.Background())
	require.NoError(t, err)
	assert.InDelta(t, 10.0, st.RADeg, 0.5)
	assert.InDelta(t, 41.0, st.DecDeg, 0.5)
}

func TestMount_Connect_DrawsAFreshEchoByteForEveryAttempt(t *testing.T) {
	hc := newFakeHC()
	hc.failEcho = true // never answers correctly, so all three attempts are used

	fp := newFaultyPort(hc)
	clk := newTestClock()
	m := New("/dev/fake", func(string) (Port, error) { return fp, nil })
	m.clock, m.sleep, m.rnd = clk.now, func(time.Duration) {}, rand.New(rand.NewSource(3))
	require.Error(t, m.Connect(context.Background()))

	var echoBytes []byte
	for _, w := range fp.sent() {
		if len(w) == 2 && w[0] == 'K' {
			echoBytes = append(echoBytes, w[1])
		}
	}
	require.Len(t, echoBytes, handshakeAttempts)

	seen := map[byte]bool{}
	for _, b := range echoBytes {
		assert.False(t, seen[b], "a repeated echo byte cannot distinguish a stale reply from a fresh one")
		seen[b] = true
		assert.NotContains(t, []byte{'#', 0x00, 0x11, 0x13}, b,
			"the terminator and the flow-control bytes are never safe to echo")
	}
}

// constantEchoPort answers every echo with the same fixed reply, whatever was asked. It is what a
// desynchronised stream looks like from the outside, and the reason the handshake demands an EXACT
// match rather than merely a well-formed answer.
type constantEchoPort struct {
	reply   []byte
	pending []byte
}

func (p *constantEchoPort) Write(b []byte) (int, error) {
	p.pending = append(p.pending, p.reply...)
	return len(b), nil
}

func (p *constantEchoPort) Read(b []byte) (int, error) {
	if len(p.pending) == 0 {
		return 0, nil
	}
	n := copy(b, p.pending)
	p.pending = p.pending[n:]
	return n, nil
}

func (p *constantEchoPort) Close() error                       { return nil }
func (p *constantEchoPort) SetReadTimeout(time.Duration) error { return nil }
func (p *constantEchoPort) ResetInputBuffer() error            { p.pending = nil; return nil }

func TestMount_Connect_RejectsAnEchoOfSomebodyElsesByte(t *testing.T) {
	clk := newTestClock()
	m := New("/dev/fake", func(string) (Port, error) { return &constantEchoPort{reply: []byte("x#")}, nil })
	m.clock, m.sleep, m.rnd = clk.now, func(time.Duration) {}, rand.New(rand.NewSource(1))

	err := m.Connect(context.Background())
	require.Error(t, err, "a well-formed reply to a DIFFERENT question must not pass the handshake")
}

func TestMount_Command_ResynchronisesBeforeAskingAgain(t *testing.T) {
	hc := newFakeHC()
	m, fp, _ := faultyRig(t, hc)

	fp.inject(fault{drop: true}) // the mount never answers the position query
	flushesBefore := fp.flushCount()

	st, err := m.State(context.Background())
	require.NoError(t, err, "a read is safe to ask again once the stream is back in step")
	assert.InDelta(t, 10.0, st.RADeg, 0.5)

	assert.Greater(t, fp.flushCount(), flushesBefore, "the port must be flushed before the retry")

	// The retry must be SEPARATED from the first attempt by the resynchronisation echo. A bare retry
	// would be read by the late reply, which is how a slow mount becomes a permanently lagged stream.
	var order []byte
	for _, w := range fp.sent() {
		if len(w) > 0 && (w[0] == 'e' || w[0] == 'K') {
			order = append(order, w[0])
		}
	}
	require.GreaterOrEqual(t, len(order), 3)
	assert.Equal(t, byte('e'), order[len(order)-3])
	assert.Equal(t, byte('K'), order[len(order)-2], "an echo must sit between the attempt and the retry")
	assert.Equal(t, byte('e'), order[len(order)-1])
}

// laggedPort is a link that is permanently one reply behind: every command is answered with the
// PREVIOUS command's reply. It is the failure Celestron's developer notes describe, reproduced
// exactly, and it is undetectable by parsing — every answer is still well formed.
type laggedPort struct {
	inner   Port
	lagging bool
	prev    []byte
	buf     []byte
}

func (p *laggedPort) startLagging(seed []byte) { p.lagging, p.prev = true, seed }

func (p *laggedPort) Write(b []byte) (int, error) {
	n, err := p.inner.Write(b)
	if err != nil {
		return n, err
	}
	reply := drainPort(p.inner)
	if !p.lagging {
		p.buf = append(p.buf, reply...)
		return n, nil
	}
	p.buf = append(p.buf, p.prev...)
	p.prev = reply
	return n, nil
}

func (p *laggedPort) Read(b []byte) (int, error) {
	if len(p.buf) == 0 {
		return 0, nil
	}
	n := copy(b, p.buf)
	p.buf = p.buf[n:]
	return n, nil
}

func (p *laggedPort) Close() error                       { return p.inner.Close() }
func (p *laggedPort) SetReadTimeout(time.Duration) error { return nil }
func (p *laggedPort) ResetInputBuffer() error            { p.buf = nil; p.lagging = false; return nil }

func TestMount_Ping_IsTheOnlyThingThatCanSeeAMisattributedReply(t *testing.T) {
	hc := newFakeHC()
	lp := &laggedPort{inner: hc}
	clk := newTestClock()
	m := New("/dev/fake", func(string) (Port, error) { return lp, nil })
	m.now = func() time.Time { return time.Date(2026, 7, 30, 22, 0, 0, 0, time.UTC) }
	m.clock, m.sleep, m.rnd = clk.now, func(time.Duration) {}, rand.New(rand.NewSource(11))
	require.NoError(t, m.Connect(context.Background()))
	t.Cleanup(func() { _ = m.Close() })

	// From here the link answers every question with the previous question's answer, seeded with a
	// perfectly valid position so nothing downstream has anything to complain about.
	lp.startLagging([]byte(EncodeRADec(200, -30) + "#"))

	st, err := m.State(context.Background())
	require.NoError(t, err, "this is the point: a lagged stream produces no error at all")
	assert.InDelta(t, 200.0, st.RADeg, 1.0,
		"the driver reports a position the mount is not at, and cannot know it")

	// Only an echo of a byte the mount has never been asked to echo can prove the stream is out of
	// step — which is why the supervisor pings, and why the handshake randomises its byte.
	lp.startLagging([]byte("0#"))
	err = m.Ping(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrDesynchronised)
	assert.Positive(t, m.Health().Desyncs, "a proven mis-attribution must be counted, not swallowed")
}

func TestMount_Command_RecoversFromAStalledReply(t *testing.T) {
	hc := newFakeHC()
	m, fp, _ := faultyRig(t, hc)

	// A stalled reply is the nastiest thing this link does: the command times out AND its answer is
	// then sitting there ready to be read as the next command's. Because a timeout always forces a
	// resynchronisation, the driver clears it inside the same call.
	fp.inject(fault{only: 'e', stall: true})
	st, err := m.State(context.Background())
	require.NoError(t, err)
	assert.InDelta(t, 10.0, st.RADeg, 0.5)

	require.NoError(t, m.Ping(context.Background()), "the stream must be back in step afterwards")
	assert.Positive(t, m.Health().Resyncs)
}

func TestMount_State_ReportsAMalformedReplyRatherThanGuessing(t *testing.T) {
	hc := newFakeHC()
	m, fp, _ := faultyRig(t, hc)

	fp.inject(fault{garbage: []byte{0x00, 0xFF}})
	_, err := m.State(context.Background())
	require.Error(t, err, "line noise in front of a position must never be decoded as a position")

	// The stream itself is still in step — the noise arrived inside one reply, not between two.
	require.NoError(t, m.Ping(context.Background()))
}

func TestMount_Read_AssemblesAReplyDeliveredOneByteAtATime(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T, m *Mount, hc *fakeHC)
	}{
		{
			name: "delimited reply",
			run: func(t *testing.T, m *Mount, _ *fakeHC) {
				st, err := m.State(context.Background())
				require.NoError(t, err)
				assert.InDelta(t, 10.0, st.RADeg, 0.5)
			},
		},
		{
			// The rate byte 35 IS '#'. A reply read by scanning for the terminator would stop on the
			// VALUE, hand back an empty body and leave the real terminator in the buffer.
			name: "binary reply whose value is the terminator",
			run: func(t *testing.T, m *Mount, hc *fakeHC) {
				hc.guideRate = 35
				got, err := m.GuideRate(context.Background())
				require.NoError(t, err)
				assert.InDelta(t, 35.0/autoguideRateScale, got, 1e-9)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hc := newFakeHC()
			m, fp, _ := faultyRig(t, hc)
			fp.setByteWise(true)
			tt.run(t, m, hc)
		})
	}
}

func TestMount_Command_TruncatedBinaryReplyIsAnErrorNotAValue(t *testing.T) {
	hc := newFakeHC()
	hc.guideRate = 42
	m, fp, _ := faultyRig(t, hc)

	// Both attempts truncated: the value byte arrives, the terminator never does. The faults are
	// pinned to the pass-through opcode so the echo the resynchronisation sends in between does not
	// consume the one meant for the retry.
	fp.inject(fault{only: 'P', truncate: 1}, fault{only: 'P', truncate: 1})
	_, err := m.GuideRate(context.Background())
	require.Error(t, err, "half a reply is not a rate")
}

func TestMount_Health_CountsWhatTheNightWillBeJudgedOn(t *testing.T) {
	hc := newFakeHC()
	m, fp, _ := faultyRig(t, hc)

	require.NoError(t, m.Ping(context.Background()))
	before := m.Health()
	assert.True(t, before.Connected)
	assert.Positive(t, before.Commands)
	assert.Zero(t, before.Unrecovered)

	fp.inject(fault{drop: true})
	_, err := m.State(context.Background())
	require.NoError(t, err)

	after := m.Health()
	assert.Greater(t, after.Errors, before.Errors)
	assert.Greater(t, after.Retries, before.Retries)
	assert.Greater(t, after.Resyncs, before.Resyncs)
	assert.NotEmpty(t, after.LastError)
	assert.Equal(t, "Advanced VX", after.Model)
}

func TestLatencyHist_PercentilesUseBucketEdges(t *testing.T) {
	var h latencyHist
	for i := 0; i < 90; i++ {
		h.add(15 * time.Millisecond)
	}
	for i := 0; i < 10; i++ {
		h.add(900 * time.Millisecond)
	}
	assert.Equal(t, int64(20), h.percentileMs(0.50), "the median falls in the 10–20 ms bucket")
	assert.Equal(t, int64(1000), h.percentileMs(0.99))
	assert.Equal(t, int64(900), h.maxMs)
	assert.Equal(t, int64(0), (&latencyHist{}).percentileMs(0.5), "an empty histogram reports nothing, not a guess")
}
