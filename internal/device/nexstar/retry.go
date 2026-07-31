package nexstar

// When it is safe to ask the mount the same thing twice.
//
// A command that times out has an unknown outcome: the hand controller may never have heard it, or
// may have acted on it and lost only the acknowledgement. Retrying is therefore not a transport
// detail — it is a decision about what a SECOND copy of this frame would do to a telescope. That is
// why the classification is by consequence, not by whether the protocol calls something a read.
//
// The table is a whitelist and the zero value is retryNever, so a command added next year is unsafe
// until somebody has thought about it. Getting that default the other way round is how a driver
// ends up restarting a slew the user just aborted.

type retrySafety int

const (
	// retryNever: a duplicate moves the mount, or changes state that outlives the session.
	retryNever retrySafety = iota
	// retryAfterResync: a pure read. A duplicate costs twenty milliseconds of wire time and nothing
	// else — but only once the stream has been resynchronised, because the first reply may still be
	// on its way and would otherwise be read as the answer to the retry.
	retryAfterResync
	// retryAlways: a STOP. A duplicate is a no-op; NOT retrying is the hazard, because the frame that
	// went missing is the one that halts a moving axis.
	retryAlways
)

// readCommands are the hand-controller commands that only report state.
//
// 'K' (echo) is here as well: it changes nothing, and the resynchronisation loop is built out of it.
var readCommands = map[byte]bool{
	'K': true,            // echo
	'V': true,            // firmware version
	'm': true,            // model
	'e': true, 'E': true, // right ascension / declination
	'z': true, 'Z': true, // azimuth / altitude
	'L': true, // goto in progress?
	'J': true, // aligned?
	't': true, // tracking mode
	'p': true, // pier side
	'w': true, // site
	'h': true, // clock
}

// classify decides what a second copy of this frame would do.
func classify(frame []byte) retrySafety {
	if len(frame) == 0 {
		return retryNever
	}
	if frame[0] == 'M' {
		// Cancel GoTo. This is the STOP button: a duplicate does nothing, and a dropped one leaves the
		// mount slewing towards wherever it was last sent.
		return retryAlways
	}
	if frame[0] == 'P' {
		return classifyPassthrough(frame)
	}
	if readCommands[frame[0]] {
		return retryAfterResync
	}
	// Everything else moves the mount or writes state that survives the session:
	//   r/R  GoTo      — a retry can send a settled tube back across the sky mid-exposure.
	//   s/S  Sync      — sync applies to where the mount is NOW, and it has moved since. Two syncs at
	//                    two positions corrupt the pointing model, invisibly, until the next GoTo misses.
	//   T    tracking  — idempotent on paper, but a retry after an unknown outcome can switch tracking
	//                    back on during a slew, which Celestron explicitly warns conflicts.
	//   H/W  clock/site, x/y hibernate — durable state; the caller re-reads and re-issues instead.
	return retryNever
}

// classifyPassthrough handles the `P` frames, where the opcode is the same for a stop and for the
// slew that needs stopping — the payload is what separates them.
func classifyPassthrough(frame []byte) retrySafety {
	if len(frame) < 8 {
		return retryNever
	}
	switch frame[3] {
	case 6, 7: // variable-rate slew: units are the two payload bytes, and zero units means stop
		if frame[4] == 0 && frame[5] == 0 {
			return retryAlways
		}
		return retryNever
	case 36, 37: // fixed-rate slew, hand-controller style: rate 0 means stop
		if frame[4] == 0 {
			return retryAlways
		}
		return retryNever
	case mcPECReadData, mcPECBin, mcAtIndex, mcGetAutoguideRate:
		// Reads of motor-controller state.
		return retryAfterResync
	}
	// mcPECWriteData writes a table that survives a power cycle, and pec.go already retries it PAIRED
	// with a read-back — a transport retry underneath would break that pairing and could report a
	// verified write that was verified against a different bin. mcSeekIndex moves RA by up to two
	// degrees. mcPECPlayback, mcPECRecordStop and mcSetAutoguideRate change durable state.
	return retryNever
}
