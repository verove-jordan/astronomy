package nexstar

import (
	"sync"
	"time"
)

// Breaking the link on purpose, one named way at a time.
//
// fakeHC (nexstar_test.go) models the PROTOCOL: given a command it produces the bytes a hand
// controller would. Everything that goes wrong on a cold tripod at 3am is a TRANSPORT problem
// instead — a reply that never comes, one that comes too late, line noise, an unplugged cable — so
// it lives here, in a decorator that wraps any Port. Keeping the two apart means the protocol fake
// stays readable and every existing test keeps working untouched.
//
// The faults are not arbitrary. Each one is something the 9600-baud USB link between a MacBook and
// a NexStar+ hand controller actually does, and each has a matching recovery in link.go.

// fault is applied to exactly one command, in order.
type fault struct {
	// only, when set, holds the fault back until a frame with this opcode is written. Without it a
	// fault queued for the second attempt of a command would instead be eaten by the echo the
	// resynchronisation sends in between — which is exactly the kind of accident that makes a fault
	// harness quietly stop testing what it claims to.
	only byte
	// drop makes the reply vanish entirely: the mount never answered. The command times out and the
	// stream is still in step afterwards.
	drop bool
	// stall holds the reply back until the NEXT command is sent, which is the far nastier case: the
	// command times out AND the late answer is then read as the next command's reply. This is the
	// one-behind desynchronisation Celestron's own developer notes warn about, and the reason a bare
	// retry after a timeout is never safe.
	stall bool
	// truncate delivers only the first n bytes of the reply — a reply cut short by a brownout or a
	// hub renegotiating.
	truncate int
	// garbage is line noise delivered ahead of the reply. Tests deliberately include a case whose
	// noise contains '#', because a scanner looking for the terminator will stop on it.
	garbage []byte
}

// faultyPort wraps a Port and injects transport failures. It takes the replies the inner port
// produced and decides what — if anything — actually arrives.
type faultyPort struct {
	inner Port

	mu       sync.Mutex
	faults   []fault  // consumed one per Write, in order; an empty list means a healthy link
	buf      []byte   // bytes available to Read
	held     []byte   // a stalled reply, still "in flight" in the hardware
	writes   [][]byte // every frame the driver sent, for assertions
	flushes  int
	dead     bool // the adapter has gone; every operation fails from here
	byteWise bool // deliver one byte per Read, the way 9600 baud really arrives
	// afterWrite runs once the frame has been sent, so a test can pull the cable at an exact moment
	// in a command's life rather than racing a timer.
	afterWrite func(frame []byte)
}

func newFaultyPort(inner Port, faults ...fault) *faultyPort {
	return &faultyPort{inner: inner, faults: faults}
}

// inject queues faults for the commands still to come. Connect's handshake happens before any test
// body runs, so faults meant for the test must be added afterwards or the handshake eats them.
func (f *faultyPort) inject(faults ...fault) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faults = append(f.faults, faults...)
}

func (f *faultyPort) setByteWise(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.byteWise = on
}

func (f *faultyPort) onWrite(fn func([]byte)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.afterWrite = fn
}

// kill makes the port behave like an unplugged adapter from the next operation onwards.
func (f *faultyPort) kill() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dead = true
}

func (f *faultyPort) sent() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]byte, len(f.writes))
	for i, w := range f.writes {
		out[i] = append([]byte(nil), w...)
	}
	return out
}

// sentCount reports how many frames starting with the given opcode were written. The retry tests
// live or die on this number: "exactly one GoTo reached the mount" is the safety property.
func (f *faultyPort) sentCount(opcode byte) int {
	n := 0
	for _, w := range f.sent() {
		if len(w) > 0 && w[0] == opcode {
			n++
		}
	}
	return n
}

func (f *faultyPort) flushCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.flushes
}

func (f *faultyPort) Write(p []byte) (int, error) {
	f.mu.Lock()
	if f.dead {
		f.mu.Unlock()
		return 0, ErrLinkGone
	}
	f.writes = append(f.writes, append([]byte(nil), p...))
	var fl fault
	if len(f.faults) > 0 && (f.faults[0].only == 0 || (len(p) > 0 && f.faults[0].only == p[0])) {
		fl, f.faults = f.faults[0], f.faults[1:]
	}
	f.mu.Unlock()

	// The mount hears the command whatever happens to its answer — that asymmetry is the whole
	// hazard behind the retry rules, so the harness must reproduce it rather than skipping the write.
	n, err := f.inner.Write(p)
	if err != nil {
		return n, err
	}
	reply := drainPort(f.inner)

	f.mu.Lock()
	if hook := f.afterWrite; hook != nil {
		f.mu.Unlock()
		hook(p)
		f.mu.Lock()
	}
	defer f.mu.Unlock()
	// A reply held back from an earlier command lands now, ahead of this one's own answer.
	f.buf = append(f.buf, f.held...)
	f.held = nil

	switch {
	case fl.drop:
		return n, nil
	case fl.stall:
		f.held = reply
		return n, nil
	}
	if fl.truncate > 0 && fl.truncate < len(reply) {
		reply = reply[:fl.truncate]
	}
	f.buf = append(f.buf, fl.garbage...)
	f.buf = append(f.buf, reply...)
	return n, nil
}

func (f *faultyPort) Read(p []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dead {
		return 0, ErrLinkGone
	}
	if len(f.buf) == 0 || len(p) == 0 {
		// A read timeout on a real port returns no bytes and no error; a test fake that returned EOF
		// here would exercise a path the hardware never takes.
		return 0, nil
	}
	n := copy(p, f.buf)
	if f.byteWise && n > 1 {
		n = 1
	}
	f.buf = f.buf[n:]
	return n, nil
}

// ResetInputBuffer discards what has already arrived — and deliberately NOT what is still in
// flight. A stalled reply survives the flush, exactly as bytes still crossing the USB pipe survive
// a TCIFLUSH, which is why flushing alone cannot fix a desynchronised stream.
func (f *faultyPort) ResetInputBuffer() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dead {
		return ErrLinkGone
	}
	f.flushes++
	f.buf = nil
	return nil
}

func (f *faultyPort) Close() error {
	f.mu.Lock()
	f.dead = true
	f.mu.Unlock()
	return f.inner.Close()
}

func (f *faultyPort) SetReadTimeout(d time.Duration) error { return f.inner.SetReadTimeout(d) }

// drainPort empties whatever the inner port has ready. fakeHC answers synchronously inside Write,
// so one pass is enough; the loop exists so a slower fake cannot silently lose bytes.
func drainPort(p Port) []byte {
	var out []byte
	buf := make([]byte, 64)
	for {
		n, err := p.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
			continue
		}
		if err != nil || n == 0 {
			return out
		}
	}
}

// ResetInputBuffer lets fakeHC satisfy Port. Clearing the queued reply is what the real ioctl does.
func (f *fakeHC) ResetInputBuffer() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = nil
	return nil
}

// seed puts bytes in the fake's queue that no command asked for — what a process killed mid-command
// leaves behind for the next one to trip over.
func (f *fakeHC) seed(b []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending = append(f.pending, b...)
}
