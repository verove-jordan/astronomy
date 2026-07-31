package nexstar

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
)

// The command door: one function every frame goes through, and what it does when the answer does
// not arrive.
//
// Celestron's developer notes state the problem exactly: a hand controller may take up to 3.5
// seconds to answer, and "if serial commands are blindly sent without waiting for a response, then
// some commands may be dropped or the software driver could see responses that are for earlier
// commands." The second half is the one that ends a night. Every reply is either a fixed-length
// binary blob or a '#'-terminated string from a small grammar, so a reply that belongs to the
// PREVIOUS command still parses: `L` (slewing?) reading `e`'s coordinates simply answers "not
// slewing", forever, and nothing above notices. Only one command can prove otherwise — an echo of a
// byte the mount has never been asked to echo before. That is why Ping exists, why the handshake
// randomises its byte, and why a bare retry after a timeout is never safe.

const (
	// handshakeAttempts is how many times Connect will ask for an echo before giving up. One attempt
	// can be eaten by a reply left in the buffer by a process that died mid-command; a second by a
	// hand controller still finishing its power-on self-test. A third failure is real. The attempts
	// do not compound, because each is preceded by a full drain and flush.
	handshakeAttempts = 3
	// resyncRounds bounds the recovery loop inside resyncBudget.
	resyncRounds = 3
	// resyncBudget is the whole recovery allowance. Two reply timeouts is long enough for any
	// straggler on a 9600-baud link to have arrived; past that the link is dead rather than confused,
	// and continuing to poke it costs the rest of the night.
	resyncBudget = 2 * replyTimeout
	// drainQuiet is how long a drain waits for another byte before calling the line idle. It is far
	// longer than the 1.04 ms a byte takes at 9600 baud, so a reply already on the wire is collected
	// rather than left behind to be read as the next command's answer.
	drainQuiet = 120 * time.Millisecond
	// drainShort is the second, cheaper pass after a flush, to catch a byte that crossed the USB pipe
	// while the flush was happening.
	drainShort = 40 * time.Millisecond
	// drainCap stops a port that is babbling from filling memory. Nothing the protocol produces comes
	// close to this.
	drainCap = 4096
	// retriesAfterResync / stopAttempts are how many times a read and a stop are asked again.
	retriesAfterResync = 2
	stopAttempts       = 3

	// reopenAttempts / reopenTimeout bound the handshake a RECONNECT performs, which is stricter than
	// the one Connect performs. Celestron's 3.5 s worst case is for commands the hand controller has
	// to relay to another device; an echo it answers itself. A reconnect probes candidate ports while
	// holding the driver's mutex, so being generous there would put Abort — the STOP button — behind
	// ten seconds of asking a dead port to say hello.
	reopenAttempts = 2
	reopenTimeout  = 1200 * time.Millisecond
)

// The three ways a command can fail, kept apart because each has a different remedy: reconnect,
// resynchronise, or tell the caller the outcome is genuinely unknown.
var (
	// ErrDesynchronised means the reply stream could not be brought back into step with the commands.
	ErrDesynchronised = errors.New("the reply stream is out of step with the commands")
	// ErrUnknownOutcome means the mount never acknowledged, so whether it ACTED is unknown. It is
	// deliberately not a plain failure: reporting a timed-out GoTo as "failed" is how a user aborts a
	// slew that is in fact already happening. Callers observe (poll `L`) rather than assume.
	ErrUnknownOutcome = errors.New("the mount did not acknowledge — whether it acted is unknown")

	errReplyTimeout = errors.New("the mount did not answer in time")
	errFraming      = errors.New("the mount's reply did not fit the protocol")
)

// echoAlphabet is the pool the handshake and resynchronisation draw from.
//
// Alphanumerics only. '#' is the terminator, so echoing it would answer "##" — indistinguishable
// from an empty body followed by a straggler. NUL and the XON/XOFF pair are excluded for a different
// reason: the protocol runs with flow control off, but a USB-serial bridge sits in the middle and
// need not agree.
var echoAlphabet = []byte("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz")

// LinkHealth is what the link knows about itself. It is the overnight evidence: a night that ends
// badly is diagnosed from these counters, not from a log line that says "it stopped".
type LinkHealth struct {
	Connected    bool   `json:"connected"`
	Reconnecting bool   `json:"reconnecting"`
	Path         string `json:"path"`
	Model        string `json:"model,omitempty"`
	Firmware     string `json:"firmware,omitempty"`

	UptimeMs       int64 `json:"uptime_ms"`
	LastReplyAgoMs int64 `json:"last_reply_ago_ms"`

	Commands uint64 `json:"commands"`
	Errors   uint64 `json:"errors"`
	Retries  uint64 `json:"retries"`
	Resyncs  uint64 `json:"resyncs"`
	// Desyncs counts PROVEN mis-attributions: an echo that came back with someone else's answer.
	// Unlike the others this one has no acceptable rate — it means a reply was read as the answer to
	// the wrong question, and the only reason it is a number rather than a crash is that the link
	// recovers from it.
	Desyncs    uint64 `json:"desyncs"`
	Reconnects uint64 `json:"reconnects"`
	// Unrecovered counts commands that survived neither a resynchronisation nor a reconnect.
	Unrecovered uint64 `json:"unrecovered"`

	LatencyP50Ms int64  `json:"latency_p50_ms"`
	LatencyP99Ms int64  `json:"latency_p99_ms"`
	LatencyMaxMs int64  `json:"latency_max_ms"`
	LastError    string `json:"last_error,omitempty"`
}

// linkStats is the mutable half, guarded by Mount.mu like everything else in the driver.
type linkStats struct {
	commands, errors, retries uint64
	resyncs, desyncs          uint64
	reconnects, unrecovered   uint64
	connectedAt, lastOK       time.Time
	lastErr                   error
	latency                   latencyHist
}

// Health reports the link's own state.
func (m *Mount) Health() LinkHealth {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.healthLocked()
}

func (m *Mount) healthLocked() LinkHealth {
	h := LinkHealth{
		Connected:    m.port != nil,
		Reconnecting: m.reconnecting,
		Path:         m.path,
		Model:        m.model,
		Firmware:     m.firmware,
		Commands:     m.stats.commands,
		Errors:       m.stats.errors,
		Retries:      m.stats.retries,
		Resyncs:      m.stats.resyncs,
		Desyncs:      m.stats.desyncs,
		Reconnects:   m.stats.reconnects,
		Unrecovered:  m.stats.unrecovered,
		LatencyP50Ms: m.stats.latency.percentileMs(0.50),
		LatencyP99Ms: m.stats.latency.percentileMs(0.99),
		LatencyMaxMs: m.stats.latency.maxMs,
	}
	if !m.stats.connectedAt.IsZero() {
		h.UptimeMs = m.clock().Sub(m.stats.connectedAt).Milliseconds()
	}
	if !m.stats.lastOK.IsZero() {
		h.LastReplyAgoMs = m.clock().Sub(m.stats.lastOK).Milliseconds()
	}
	if m.stats.lastErr != nil {
		h.LastError = m.stats.lastErr.Error()
	}
	return h
}

// IdleFor reports how long it has been since a command last succeeded. The supervisor uses it to
// decide whether a heartbeat is needed at all: while a PEC run or a guide loop is working, its own
// traffic is the liveness proof and an extra ping would only steal time on a 9600-baud link.
func (m *Mount) IdleFor() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stats.lastOK.IsZero() {
		return 0
	}
	return m.clock().Sub(m.stats.lastOK)
}

// Ping proves the link is not merely open but SYNCHRONISED, by asking the mount to echo a byte it
// has never been asked to echo before.
//
// Every other command's reply is indistinguishable from the previous command's reply of the same
// shape, so this is the only question whose wrong answer can be detected at all.
func (m *Mount) Ping(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return device.ErrNotConnected
	}
	b := m.echoByte(nil)
	reply, err := m.sendLocked([]byte{'K', b}, -1)
	if err != nil {
		return err
	}
	if len(reply) != 2 || reply[0] != b {
		m.stats.desyncs++
		got := string(reply)
		if err := m.resyncLocked(); err != nil {
			return err
		}
		return fmt.Errorf("%w: an echo of %q was answered with %q", ErrDesynchronised, string(b), got)
	}
	return nil
}

// sendLocked is the single door every command goes through: write, read, and — when the answer does
// not arrive or does not fit — decide whether resynchronising and asking again is safe.
//
// want < 0 selects delimiter mode (read to '#'); want >= 0 selects fixed-LENGTH mode. The two are
// not interchangeable: a PEC bin holding the value 35 IS '#', so scanning for the terminator would
// hand back an empty body and leave the real one in the buffer.
//
// Caller holds m.mu.
func (m *Mount) sendLocked(frame []byte, want int) ([]byte, error) {
	if m.port == nil {
		// Both sentinels, so existing callers testing for ErrNotConnected keep working while the
		// recovery code can still ask "is the link gone?".
		return nil, fmt.Errorf("%w: %w", device.ErrNotConnected, ErrLinkGone)
	}

	safety := classify(frame)
	attempts := 1
	switch safety {
	case retryAfterResync:
		attempts = retriesAfterResync
	case retryAlways:
		attempts = stopAttempts
	}

	var lastErr error
	for i := 0; i < attempts; i++ {
		if i > 0 {
			// Never a bare retry: the first reply may still be on its way, and would then be read as
			// the answer to the retry. Resynchronising is what makes asking again safe.
			if err := m.resyncLocked(); err != nil {
				return nil, err
			}
			m.stats.retries++
		}
		start := m.clock()
		body, err := m.attemptLocked(frame, want)
		if err == nil {
			m.noteOKLocked(m.clock().Sub(start))
			return body, nil
		}
		lastErr = err
		m.noteErrLocked(err)
		if errors.Is(err, ErrLinkGone) {
			return nil, m.linkLostLocked(err)
		}
	}

	// Out of attempts. Resynchronise anyway, so the NEXT command starts from a clean stream — this is
	// what stops a single lost reply from poisoning every command after it.
	if err := m.resyncLocked(); err != nil {
		m.stats.unrecovered++
		return nil, err
	}
	if safety == retryNever {
		m.stats.unrecovered++
		return nil, fmt.Errorf("%w: %v", ErrUnknownOutcome, lastErr)
	}
	m.stats.unrecovered++
	return nil, lastErr
}

// attemptLocked is one write and one read, with no recovery of any kind.
func (m *Mount) attemptLocked(frame []byte, want int) ([]byte, error) {
	if _, err := m.port.Write(frame); err != nil {
		return nil, fmt.Errorf("write %v: %w", frame, err)
	}
	if want >= 0 {
		return m.readFixedLocked(frame, want)
	}
	return m.readDelimitedLocked(frame)
}

// readDelimitedLocked reads until the '#' terminator.
func (m *Mount) readDelimitedLocked(frame []byte) ([]byte, error) {
	var out []byte
	buf := make([]byte, 32)
	deadline := m.clock().Add(m.replyDeadline())
	for {
		n, err := m.port.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
			if idx := indexByte(out, '#'); idx >= 0 {
				return out[:idx+1], nil
			}
			if len(out) > drainCap {
				return nil, fmt.Errorf("%w: reply to %v never terminated", errFraming, frame)
			}
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read reply to %v: %w", frame, err)
		}
		if !m.clock().Before(deadline) {
			return nil, fmt.Errorf("%w: no reply to %v within %s", errReplyTimeout, frame, m.replyDeadline())
		}
	}
}

// readFixedLocked reads exactly want bytes and then the terminator.
func (m *Mount) readFixedLocked(frame []byte, want int) ([]byte, error) {
	out := make([]byte, 0, want+1)
	buf := make([]byte, want+1)
	deadline := m.clock().Add(m.replyDeadline())
	for len(out) < want+1 {
		n, err := m.port.Read(buf[:want+1-len(out)])
		if n > 0 {
			out = append(out, buf[:n]...)
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("read %d-byte reply to %v: %w", want, frame, err)
		}
		if !m.clock().Before(deadline) {
			return nil, fmt.Errorf("%w: no reply to %v within %s", errReplyTimeout, frame, m.replyDeadline())
		}
	}
	if out[want] != '#' {
		return nil, fmt.Errorf("%w: reply to %v was not '#'-terminated: %v", errFraming, frame, out)
	}
	return out[:want], nil
}

// resyncLocked brings the reply stream back into step with the commands.
//
// The order — drain to quiet, flush, drain again, then echo — is load bearing. Flushing alone
// discards only what the tty layer holds right now and misses the bytes still crossing the USB pipe;
// draining alone cannot tell "the line is empty" from "the mount is slow" and so eats the very reply
// it was waiting for. Doing both, then proving synchronisation with a byte the mount has not been
// asked for before, is what makes the loop converge instead of turning a one-command lag into a
// two-command lag.
//
// Caller holds m.mu.
func (m *Mount) resyncLocked() error {
	if m.port == nil {
		return fmt.Errorf("%w: %w", device.ErrNotConnected, ErrLinkGone)
	}
	deadline := m.clock().Add(resyncBudget)
	seen := map[byte]bool{}

	for round := 0; round < resyncRounds; round++ {
		m.drainLocked(drainQuiet)
		_ = m.flushLocked()
		m.drainLocked(drainShort)

		b := m.echoByte(seen)
		seen[b] = true
		reply, err := m.attemptLocked([]byte{'K', b}, -1)
		switch {
		case err == nil && len(reply) == 2 && reply[0] == b:
			m.stats.resyncs++
			m.stats.lastOK = m.clock()
			return nil
		case errors.Is(err, ErrLinkGone):
			return err
		case err == nil && len(reply) == 2 && seen[reply[0]]:
			// An earlier attempt's answer: we now know exactly how far behind the stream is, and one
			// more round consumes it. Without remembering the bytes we have used, this case is
			// indistinguishable from noise and the loop thrashes.
			continue
		}
		if !m.clock().Before(deadline) {
			break
		}
	}
	m.stats.desyncs++
	return ErrDesynchronised
}

// drainLocked reads whatever is already on the line and throws it away, returning it only so tests
// can assert what was there. It stops at two consecutive silent reads or at the byte cap.
func (m *Mount) drainLocked(window time.Duration) []byte {
	if m.port == nil {
		return nil
	}
	_ = m.port.SetReadTimeout(window)
	defer func() { _ = m.port.SetReadTimeout(m.replyDeadline()) }()

	var out []byte
	buf := make([]byte, 64)
	silent := 0
	for len(out) < drainCap {
		n, err := m.port.Read(buf)
		if n > 0 {
			out = append(out, buf[:n]...)
			silent = 0
			continue
		}
		if err != nil {
			return out
		}
		if silent++; silent >= 2 {
			return out
		}
	}
	return out
}

func (m *Mount) flushLocked() error {
	if m.port == nil {
		return nil
	}
	return m.port.ResetInputBuffer()
}

// handshakeLocked proves a hand controller is on the other end, and that we are reading ITS answer
// rather than one left over from a process that died mid-command.
//
// Each attempt draws a FRESH random echo byte, and that is the whole point. The old fixed `Kx`
// always asks for "x#", so a stale "x#" sitting in the buffer passes the handshake and leaves the
// stream exactly one reply behind — permanently, silently, and in the direction that makes "is the
// mount slewing?" answer with a pair of coordinates.
//
// Caller holds m.mu.
func (m *Mount) handshakeLocked(attempts int) error {
	var last error
	seen := map[byte]bool{}
	for i := 0; i < attempts; i++ {
		m.drainLocked(drainQuiet)
		_ = m.flushLocked()
		m.drainLocked(drainShort)

		b := m.echoByte(seen)
		seen[b] = true
		reply, err := m.attemptLocked([]byte{'K', b}, -1)
		if err == nil && len(reply) == 2 && reply[0] == b {
			return nil
		}
		if errors.Is(err, ErrLinkGone) {
			return err
		}
		if err != nil {
			last = err
		} else {
			last = fmt.Errorf("an echo of %q was answered with %q", string(b), string(reply))
		}
	}
	if last == nil {
		last = errors.New("no answer")
	}
	return last
}

// echoByte picks a byte the mount has not just been asked to echo. Randomising per attempt is what
// makes a stale reply provably distinguishable from a fresh one.
func (m *Mount) echoByte(seen map[byte]bool) byte {
	for i := 0; i < 8; i++ {
		b := echoAlphabet[m.rnd.Intn(len(echoAlphabet))]
		if !seen[b] {
			return b
		}
	}
	// Sixty-two candidates and at most three used: reaching here means the random source is
	// degenerate, so fall back to the first unused byte rather than looping.
	for _, b := range echoAlphabet {
		if !seen[b] {
			return b
		}
	}
	return echoAlphabet[0]
}

// replyDeadline is how long a single reply may take. It is normally Celestron's documented worst
// case, and briefly shorter while a reconnect probes candidate ports.
func (m *Mount) replyDeadline() time.Duration {
	if m.timeout > 0 {
		return m.timeout
	}
	return replyTimeout
}

// withTimeout runs fn against a tighter reply deadline, restoring the previous one afterwards —
// including on the port itself, or a real serial read would still block for the old value.
func (m *Mount) withTimeout(d time.Duration, fn func() error) error {
	prev := m.timeout
	m.timeout = d
	if m.port != nil {
		_ = m.port.SetReadTimeout(d)
	}
	defer func() {
		m.timeout = prev
		if m.port != nil {
			_ = m.port.SetReadTimeout(m.replyDeadline())
		}
	}()
	return fn()
}

func (m *Mount) noteOKLocked(d time.Duration) {
	m.stats.commands++
	m.stats.lastOK = m.clock()
	m.stats.latency.add(d)
}

func (m *Mount) noteErrLocked(err error) {
	m.stats.errors++
	m.stats.lastErr = err
}

// --- latency ------------------------------------------------------------------------------------

// latencyBucketsMs are the upper edges of the reply-time histogram, log-spaced around what the wire
// actually costs: at 9600 8N1 one byte takes 1.04 ms, so the 19 bytes of a precise position query
// are about 20 ms before the hand controller has done any thinking. The top edge is Celestron's
// documented 3.5 s worst case, which is also our timeout — anything at that edge IS a timeout.
var latencyBucketsMs = []int64{5, 10, 20, 30, 50, 75, 100, 150, 200, 300, 500, 750, 1000, 1500, 2000, 3000, 3500}

// latencyHist is a fixed-bucket histogram. Buckets rather than a sample list because a whole night
// at 2 Hz is nearly sixty thousand samples, and no new dependency is worth a percentile.
type latencyHist struct {
	// counts has one slot per bucket plus a final overflow slot, allocated on first use so the zero
	// value of a Mount stays cheap.
	counts       []uint64
	n            uint64
	sumMs        int64
	minMs, maxMs int64
}

func (h *latencyHist) add(d time.Duration) {
	if h.counts == nil {
		h.counts = make([]uint64, len(latencyBucketsMs)+1)
	}
	ms := d.Milliseconds()
	h.n++
	h.sumMs += ms
	if h.n == 1 || ms < h.minMs {
		h.minMs = ms
	}
	if ms > h.maxMs {
		h.maxMs = ms
	}
	i := sort.Search(len(latencyBucketsMs), func(i int) bool { return ms <= latencyBucketsMs[i] })
	h.counts[i]++
}

// percentileMs reports a bucket-resolution percentile: the upper edge of the bucket the requested
// fraction falls into. It is deliberately not interpolated — pretending to millisecond precision
// from 20 ms buckets would invite comparisons the data cannot support.
func (h *latencyHist) percentileMs(p float64) int64 {
	if h.n == 0 {
		return 0
	}
	target := uint64(float64(h.n) * p)
	var seen uint64
	for i, c := range h.counts {
		seen += c
		if seen > target {
			if i >= len(latencyBucketsMs) {
				return h.maxMs
			}
			return latencyBucketsMs[i]
		}
	}
	return h.maxMs
}

func (h *latencyHist) meanMs() int64 {
	if h.n == 0 {
		return 0
	}
	return h.sumMs / int64(h.n)
}
