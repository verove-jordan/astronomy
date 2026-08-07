package nexstar

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"golang.org/x/sys/unix"
)

// The only tests that ever open a real serial device.
//
// Everything else in this package talks to an in-memory fake, which proves the protocol and the
// recovery logic but never touches openSerial, termios, TIOCEXCL or the read-timeout semantics the
// whole driver is built on. A pseudo-terminal is a real tty with a real file descriptor, so the
// production code path runs unmodified — and, usefully, closing the master reproduces a macOS USB
// re-enumeration exactly: reads fail with PortClosed, writes with EIO, and the device node itself
// disappears so reopening gives ENOENT.
//
// Two things it cannot do, stated so nobody assumes otherwise: TIOCEXCL is accepted on a pty but not
// enforced (a second open succeeds), so the "another program holds the port" path is injected at the
// Opener seam instead; and ListPorts cannot see pty slaves, because /dev names them ttysNNN with no
// dot and the darwin filter requires one.

// newPTY allocates a pseudo-terminal pair and returns the master descriptor and the slave's path.
//
// The slave name is derived from the master's device MINOR number rather than read with
// TIOCPTYGNAME, which would need a pointer-taking ioctl that x/sys/unix does not export on darwin —
// leaving only syscall.Syscall plus unsafe.Pointer for a value the kernel already tells us through
// Fstat. macOS caps kern.tty.ptmx_max at 511 and names ptmx-allocated slaves /dev/ttysNNN with
// exactly three digits, so the derivation is total; the Stat below turns any future surprise into a
// failed test rather than a test that quietly drives the wrong device.
func newPTY(t *testing.T) (master int, slavePath string) {
	t.Helper()
	fd, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no /dev/ptmx here: %v", err)
	}
	// Both of these are _IO('t',n) — void-argument ioctls — so the int is ignored and no pointer is
	// laundered through an integer parameter.
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYGRANT, 0); err != nil {
		_ = unix.Close(fd)
		t.Skipf("TIOCPTYGRANT: %v", err)
	}
	if err := unix.IoctlSetInt(fd, unix.TIOCPTYUNLK, 0); err != nil {
		_ = unix.Close(fd)
		t.Skipf("TIOCPTYUNLK: %v", err)
	}
	var st unix.Stat_t
	require.NoError(t, unix.Fstat(fd, &st))
	slavePath = fmt.Sprintf("/dev/ttys%03d", st.Rdev&0xffffff)
	if _, err := os.Stat(slavePath); err != nil {
		_ = unix.Close(fd)
		t.Fatalf("derived slave path %s does not exist: %v", slavePath, err)
	}
	t.Cleanup(func() { _ = unix.Close(fd) })
	return fd, slavePath
}

// commandLen reports how many bytes the frame starting at b needs, or 0 if it is not complete yet.
// The hand controller's grammar is fixed-width per opcode, which is what makes a byte stream
// parseable at all.
func commandLen(b []byte) int {
	if len(b) == 0 {
		return 0
	}
	need := 1
	switch b[0] {
	case 'K', 'T':
		need = 2
	case 'P':
		need = 8
	case 'r', 's':
		need = 18
	case 'H', 'W':
		need = 9
	}
	if len(b) < need {
		return 0
	}
	return need
}

// handController plays a NexStar hand controller on the master side of a pty, reusing the protocol
// fake's own replies so the two cannot answer differently.
type handController struct {
	master   int
	hc       *fakeHC
	byteWise bool
	mu       sync.Mutex
	stopped  bool
}

func (h *handController) run() {
	buf := make([]byte, 64)
	var pending []byte
	for {
		h.mu.Lock()
		if h.stopped {
			h.mu.Unlock()
			return
		}
		h.mu.Unlock()

		n, err := unix.Read(h.master, buf)
		if err != nil {
			if err == unix.EINTR {
				continue
			}
			return
		}
		if n <= 0 {
			return
		}
		pending = append(pending, buf[:n]...)
		for {
			size := commandLen(pending)
			if size == 0 {
				break
			}
			frame := pending[:size]
			pending = pending[size:]

			h.hc.mu.Lock()
			h.hc.sent = append(h.hc.sent, string(frame))
			reply := h.hc.replyTo(frame)
			h.hc.mu.Unlock()

			// Always read before writing: the pty buffers about a kilobyte and the master blocks once
			// that fills. Writing a reply nobody is reading yet is how this deadlocks.
			h.write(reply)
		}
	}
}

func (h *handController) write(b []byte) {
	if !h.byteWise {
		_, _ = unix.Write(h.master, b)
		return
	}
	for i := range b {
		_, _ = unix.Write(h.master, b[i:i+1])
		time.Sleep(time.Millisecond)
	}
}

func (h *handController) stop() {
	h.mu.Lock()
	h.stopped = true
	h.mu.Unlock()
}

// ptyMount wires the real driver, through the real openSerial, to a hand controller on the other end
// of a real pty.
func ptyMount(t *testing.T, byteWise bool) (*Mount, *handController) {
	t.Helper()
	master, slave := newPTY(t)
	hc := &handController{master: master, hc: newFakeHC(), byteWise: byteWise}
	go hc.run()
	t.Cleanup(hc.stop)

	m := New(slave, nil) // nil opener == the production openSerial
	m.now = func() time.Time { return time.Date(2026, 7, 30, 22, 0, 0, 0, time.UTC) }
	m.rnd = rand.New(rand.NewSource(13))
	m.candidates = func() []PortInfo { return nil }
	// The reconnect loop is deliberately endless in production. Here it must not spend real seconds
	// between attempts, or a test that pulls the cable would wait for the backoff before it can close.
	m.sleep = func(time.Duration) { time.Sleep(time.Millisecond) }
	t.Cleanup(func() { _ = m.Close() })
	return m, hc
}

func TestOpenSerial_OpensARealTTYAtTheProtocolsSettings(t *testing.T) {
	_, slave := newPTY(t)
	port, err := openSerial(slave)
	require.NoError(t, err, "9600 8N1 must be settable on a real terminal")
	require.NoError(t, port.Close())
}

func TestOpenSerial_ReadTimeoutReturnsNothingRatherThanBlocking(t *testing.T) {
	_, slave := newPTY(t)
	port, err := openSerial(slave)
	require.NoError(t, err)
	defer func() { _ = port.Close() }()

	require.NoError(t, port.SetReadTimeout(150*time.Millisecond))
	buf := make([]byte, 8)
	start := time.Now()
	n, err := port.Read(buf)

	// The whole read loop is built on this contract: a quiet line yields no bytes and NO error, so
	// the deadline check — not an error — is what ends the wait.
	assert.Equal(t, 0, n)
	assert.NoError(t, err)
	assert.GreaterOrEqual(t, time.Since(start), 100*time.Millisecond)
}

func TestOpenSerial_ResetInputBufferDiscardsWhatAlreadyArrived(t *testing.T) {
	master, slave := newPTY(t)
	port, err := openSerial(slave)
	require.NoError(t, err)
	defer func() { _ = port.Close() }()

	_, err = unix.Write(master, []byte(strings.Repeat("x#", 250)))
	require.NoError(t, err)
	time.Sleep(50 * time.Millisecond)

	require.NoError(t, port.ResetInputBuffer())
	require.NoError(t, port.SetReadTimeout(100*time.Millisecond))
	buf := make([]byte, 64)
	n, err := port.Read(buf)
	assert.Equal(t, 0, n, "stale bytes must be gone, or the first command reads them as its reply")
	assert.NoError(t, err)
}

func TestMount_Connect_OverARealSerialLink(t *testing.T) {
	m, _ := ptyMount(t, false)

	require.NoError(t, m.Connect(context.Background()))
	assert.Equal(t, "Advanced VX", m.Model())
	assert.Equal(t, "5.30", m.Firmware())

	st, err := m.State(context.Background())
	require.NoError(t, err)
	assert.InDelta(t, 10.0, st.RADeg, 0.5)
	assert.InDelta(t, 41.0, st.DecDeg, 0.5)

	require.NoError(t, m.Ping(context.Background()), "the stream must be in step over a real fd too")
}

func TestMount_State_ReassemblesAReplyDeliveredByteByByte(t *testing.T) {
	m, _ := ptyMount(t, true)

	require.NoError(t, m.Connect(context.Background()))
	st, err := m.State(context.Background())
	require.NoError(t, err, "at 9600 baud a reply arrives a byte at a time; the reader must wait for '#'")
	assert.InDelta(t, 10.0, st.RADeg, 0.5)
}

func TestOpenSerial_ReportsTheLinkGoneWhenTheAdapterVanishes(t *testing.T) {
	master, slave := newPTY(t)
	port, err := openSerial(slave)
	require.NoError(t, err)
	defer func() { _ = port.Close() }()

	// Closing the master is what an unplugged USB adapter looks like to the process holding the
	// slave. The whole reconnect path hangs off this being an ERROR rather than a quiet timeout: a
	// timeout would be retried as though the mount were merely slow, and the night would be spent
	// asking a descriptor that is never going to answer.
	require.NoError(t, unix.Close(master))

	_, werr := port.Write([]byte("Kx"))
	_, rerr := port.Read(make([]byte, 8))
	err = werr
	if err == nil {
		err = rerr
	}
	require.Error(t, err, "a vanished adapter must not read as a slow one")
	assert.ErrorIs(t, err, ErrLinkGone,
		"the library reports this as *serial.PortError or a bare EIO; both must translate to one sentinel")
}

func TestOpenSerial_LeaksNoFileDescriptors(t *testing.T) {
	_, slave := newPTY(t)
	before := openFDs(t)
	for i := 0; i < 100; i++ {
		port, err := openSerial(slave)
		require.NoError(t, err)
		require.NoError(t, port.Close())
	}
	// A descriptor leaked per open would end a night at the process limit rather than with an error
	// anybody could read.
	assert.LessOrEqual(t, openFDs(t), before+2)
}

func TestListPorts_DoesNotOfferPtySlaves(t *testing.T) {
	_, slave := newPTY(t)
	for _, p := range ListPorts() {
		assert.NotEqual(t, slave, p.Path,
			"the darwin filter wants a dot in the name; a library bump that loosened it would fill the picker with terminals")
	}
}

// openFDs probes how many descriptors are in use, by asking for a new one and reading back the
// number the kernel picked: descriptors are allocated lowest-free-first, so a number that grows is a
// leak. Counting /dev/fd would be the obvious way and does not work — reading the directory consumes
// a descriptor that is gone by the time the entries are stat'ed, and macOS answers EBADF.
func openFDs(t *testing.T) int {
	t.Helper()
	fd, err := unix.Open("/dev/null", unix.O_RDONLY, 0)
	require.NoError(t, err)
	require.NoError(t, unix.Close(fd))
	return fd
}
