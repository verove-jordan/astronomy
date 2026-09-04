package capture

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Surviving a hardware hiccup mid-night.
//
// The run loop used to end the whole session on ANY error from a single frame. That is the right
// answer for "the output directory is read-only" and the wrong one for "somebody nudged the USB
// cable at 2am": the driver reconnects on its own within seconds, and everything that was going to
// happen for the rest of the night was thrown away because one frame did not.
//
// So the loop now distinguishes the two. A device error pauses the session, waits for the hardware
// to come back, and carries on from the next frame; anything else still fails immediately, because
// retrying a bad path or a full disk forever is worse than stopping.

const (
	// recoveryCeiling is how long a session will wait for hardware before giving up.
	//
	// Ten minutes is long enough for a replugged cable, a hand controller rebooted by hand, or a hub
	// that renegotiated — and short enough that a rig which is genuinely dead is not still "running"
	// at dawn with an hour of clear sky wasted.
	recoveryCeiling = 10 * time.Minute
	// recoveryPoll is how often the hardware is re-tried while recovering. The device server's own
	// reconnect is already backing off underneath, so this only decides how quickly the session
	// notices it succeeded.
	recoveryPoll = 5 * time.Second
)

// errExposureStalled means the camera accepted an exposure and then never reported it finished.
//
// A sentinel rather than another entry in deviceErrorHints because this one is OURS: the hints match
// what the device server wrote, and this is raised here, by the poll loop that gave up waiting. It is
// recoverable for the same reason a vanished filter wheel is — the usual cause is the USB bus
// renegotiating under a camera that comes straight back — but see maxConsecutiveRecoveries.
var errExposureStalled = errors.New("the camera did not finish the exposure")

// maxConsecutiveRecoveries is how many times in a row a run may be rescued before it is called dead.
//
// The ceiling exists BECAUSE the stall above is recoverable. Every other recoverable error is
// self-limiting: the device is either back or it is not, and recover() waits the full ten minutes
// either way. A stall is not — the camera answers "connected" the whole time — so without a count a
// wedged rig would cycle expose-timeout-recover-expose for the rest of the night, spending it on
// nothing and reporting the session as healthy the whole way. Five is two or three real hiccups'
// worth of headroom, and at 60 s subs it costs a quarter of an hour before giving up.
const maxConsecutiveRecoveries = 5

// deviceErrorHints are the fragments a device-side failure carries.
//
// Matching on text is not elegant, and it is what the seam allows: the sequencer talks to the device
// server over HTTP, so a driver's typed sentinel has already been flattened to a status code and a
// message by the time it arrives. The device server answers 409 for "not connected" and 503 for
// "driver unavailable" with these words in the body, and internal/devsrv's error mapping is what
// keeps them stable.
var deviceErrorHints = []string{
	"not_connected",
	"driver_unavailable",
	"the serial link to the hand controller is gone",
	"the serial port is held by another program",
	"connection refused", // the device server itself restarted
	"EOF",
}

// isRecoverable reports whether an error is worth waiting out rather than ending the night for.
func isRecoverable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, errExposureStalled) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, hint := range deviceErrorHints {
		if strings.Contains(msg, strings.ToLower(hint)) {
			return true
		}
	}
	return false
}

// recover waits for the hardware to come back, publishing progress so the UI can say what is
// happening rather than showing a stalled session.
//
// It returns nil once the device server reports a connected camera again — the camera, not the
// mount, because a session can continue without a mount (dithering degrades gracefully already) but
// not without the thing taking the pictures.
func (r *Runner) recoverFromDeviceError(ctx context.Context, cause error) error {
	deadline := time.Now().Add(recoveryCeiling)

	r.mu.Lock()
	previous := r.progress.Status
	r.progress.Status = StatusPaused
	r.progress.Message = fmt.Sprintf("hardware error — waiting up to %s for it to come back: %v",
		recoveryCeiling, cause)
	r.mu.Unlock()
	r.publish()

	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(recoveryPoll):
		}

		st, err := r.client.Camera(ctx)
		if err != nil || !st.Connected {
			continue
		}
		r.mu.Lock()
		r.progress.Status = previous
		r.progress.Message = fmt.Sprintf("hardware recovered after %s",
			time.Until(deadline.Add(-recoveryCeiling)).Round(time.Second)*-1)
		r.mu.Unlock()
		r.publish()
		return nil
	}
	return fmt.Errorf("the hardware did not come back within %s: %w", recoveryCeiling, cause)
}
