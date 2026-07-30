// Package nexstar drives a Celestron NexStar-protocol mount (AVX, CGEM, Evolution…) over the hand
// controller's serial link.
//
// The protocol is small but unforgiving, and three of its details cause most of the trouble:
//
//   - Angles travel as a fraction of a full revolution in hex, not as degrees. The "precise" forms
//     use 32 bits (only the top 24 are meaningful), giving about 0.08 arcseconds per unit.
//   - Two adjacent commands answer in DIFFERENT alphabets: `J` (aligned?) replies with a binary
//     0/1 byte, while `L` (slewing?) replies with the ASCII characters '0'/'1'. Reading one as the
//     other is a classic, silent bug.
//   - The mount works in the equinox of DATE. Everything else in this app is J2000, and by 2026 the
//     two differ by about a third of a degree, so the conversion is mandatory (see nexstar.go).
//
// This file is the pure wire format — no serial port, no timing — so all of it is directly testable.
package nexstar

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/verove-jordan/astronomy/internal/device"
)

// Tracking modes, as the hand controller numbers them.
const (
	TrackingOff     = 0
	TrackingAltAz   = 1
	TrackingEQNorth = 2
	TrackingEQSouth = 3
)

// encodeAngle renders an angle as the protocol's precise 32-bit hex fraction of a revolution. The
// low byte is always zero because the encoders carry 24 bits — Celestron's own examples show the
// same, and sending anything there is silently discarded.
func encodeAngle(deg float64) string {
	frac := math.Mod(deg, 360) / 360
	if frac < 0 {
		frac++
	}
	v := uint32(math.Round(frac*float64(1<<32)/256)) * 256
	return fmt.Sprintf("%08X", v)
}

// decodeAngle turns a precise hex value back into degrees in [0,360).
func decodeAngle(hex string) (float64, error) {
	v, err := strconv.ParseUint(strings.TrimSpace(hex), 16, 64)
	if err != nil {
		return 0, fmt.Errorf("bad angle %q: %w", hex, err)
	}
	return float64(v) / float64(1<<32) * 360, nil
}

// EncodeRADec builds the payload shared by the precise GoTo (`r`) and Sync (`s`) commands. RA is
// given in degrees like everything else in this codebase and converted to the protocol's hour-angle
// fraction here, once.
func EncodeRADec(raDeg, decDeg float64) string {
	return encodeAngle(raDeg) + "," + encodeAngle(decDeg)
}

// DecodeRADec parses the reply to the precise position query (`e`), e.g. "34AB0500,12CE0500".
//
// Declination needs a quadrant fold that RA does not: the mount reports it as a plain fraction of a
// revolution, so a southern declination comes back as (say) 350° rather than −10°, and anything past
// the pole comes back mirrored. Skipping this is how a mount ends up "at" +170° declination.
func DecodeRADec(reply string) (raDeg, decDeg float64, err error) {
	parts := strings.Split(strings.TrimSuffix(strings.TrimSpace(reply), "#"), ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("malformed position reply %q", reply)
	}
	raDeg, err = decodeAngle(parts[0])
	if err != nil {
		return 0, 0, err
	}
	raw, err := decodeAngle(parts[1])
	if err != nil {
		return 0, 0, err
	}
	return raDeg, foldDeclination(raw), nil
}

// foldDeclination maps a full-revolution angle onto the ±90° declination range.
func foldDeclination(deg float64) float64 {
	switch {
	case deg > 90 && deg <= 270:
		return 180 - deg
	case deg > 270:
		return deg - 360
	}
	return deg
}

// Axis identifies which motor a pass-through command addresses.
const (
	axisAzmRA  = 16 // 0x10
	axisAltDec = 17 // 0x11
)

// passthrough builds the `P` frame that carries a command straight to a motor controller, bypassing
// the hand controller's own vocabulary.
//
// Two of its four framing bytes are easy to get backwards, and both failures are silent:
//   - the length byte counts the COMMAND plus its payload, not the frame and not the payload alone;
//   - the last byte is how many bytes the mount should answer WITH, and the mount believes it. Ask
//     for the wrong count and the extra (or missing) bytes stay in the port buffer, so every later
//     command reads the previous one's reply.
//
// The frame is always eight bytes, which caps the payload at three.
func passthrough(dest, cmd byte, payload []byte, respLen byte) []byte {
	frame := []byte{'P', byte(len(payload) + 1), dest, cmd, 0, 0, 0, respLen}
	copy(frame[4:7], payload)
	return frame
}

// slewRateCommand builds a variable-rate slew: the pass-through frame that makes the motor turn at a
// commanded speed rather than driving to a target. This is the primitive behind both dithering and
// periodic-error correction.
//
// The rate is in arcseconds per second, and the protocol wants it multiplied by four then split into
// two bytes — Celestron's own worked example is 150″/s → high 2, low 88.
func slewRateCommand(axis int, arcsecPerSec float64) []byte {
	dir := byte(6) // positive
	if arcsecPerSec < 0 {
		dir = 7 // negative
		arcsecPerSec = -arcsecPerSec
	}
	units := int(math.Round(arcsecPerSec * 4))
	if units > 0xFFFF {
		units = 0xFFFF
	}
	return passthrough(byte(axis), dir, []byte{byte(units >> 8), byte(units & 0xFF)}, 0)
}

// fixedRateCommand builds a hand-controller-style fixed-rate slew (rate 0–9, 0 stops). These are what
// the direction buttons send, and rates 1–2 deliberately do NOT override equatorial tracking.
func fixedRateCommand(axis int, rate int, positive bool) []byte {
	dir := byte(37) // negative
	if positive {
		dir = 36
	}
	if rate < 0 {
		rate = 0
	}
	if rate > 9 {
		rate = 9
	}
	return passthrough(byte(axis), dir, []byte{byte(rate)}, 0)
}

// The motor-controller commands that drive periodic-error correction. Celestron does not document
// these; they are the AUX set, stable across firmware and implemented identically by every
// third-party driver — INDI's being the reference this file's model table already follows.
const (
	mcPECPlayback   = 0x0D // payload 01 starts playback, 00 stops it
	mcPECBin        = 0x0E // → the bin the worm is turning through right now
	mcPECRecordStop = 0x16 // cancel a recording the mount started on its own
	mcAtIndex       = 0x18 // → 0xFF once the index mark has been found
	mcSeekIndex     = 0x19 // begin seeking the index; MOVES RA by up to two degrees
	mcPECReadData   = 0x30 // {0x40+i} → bin i;  {0x3F} → how many bins there are
	mcPECWriteData  = 0x31 // {0x40+i, value} → writes bin i
)

// Celestron overloads one READ command for both the table and its metadata, and tells them apart by
// the selector byte: 0x3F asks how many bins exist, 0x40+i asks for bin i. Forgetting the offset
// makes a read of bin 0 answer with the bin COUNT — a plausible-looking 88 that is not a bin value.
const (
	pecBinOffset     = 0x40
	pecCountSelector = 0x3F
)

// The motor controller's own autoguide-rate setting, from the same undocumented AUX set as the PEC
// commands above. Reading it is what lets a guide loop size its pulses to the rate the mount is
// configured for, instead of assuming the dither constant is also the right guiding speed.
const (
	mcSetAutoguideRate = 0x46 // payload: one byte, rate = 256 × fraction of sidereal
	mcGetAutoguideRate = 0x47 // → one byte, the same scaling
)

// autoguideRateScale is what one unit of the autoguide-rate byte is worth. The rate travels as a
// fraction of sidereal in 1/256ths, so the whole byte spans zero to just under one times sidereal.
const autoguideRateScale = 256.0

// guideRateCommands build the read and write frames for the autoguide rate.
func setGuideRateCommand(axis int, fraction float64) []byte {
	units := int(math.Round(fraction * autoguideRateScale))
	if units < 0 {
		units = 0
	}
	// A full 256 does not fit in the byte, and 255 is indistinguishable from it in practice.
	if units > 0xFF {
		units = 0xFF
	}
	return passthrough(byte(axis), mcSetAutoguideRate, []byte{byte(units)}, 0)
}

func getGuideRateCommand(axis int) []byte {
	return passthrough(byte(axis), mcGetAutoguideRate, nil, 1)
}

// siderealArcsecPerSec is the rate the sky turns, and the unit the mount's PEC rates are scaled
// against. Shared with the simulator so the two cannot drift apart.
const siderealArcsecPerSec = device.SiderealArcsecPerSec

// pecRateScale is the divisor that turns a bin's signed byte into a fraction of the sidereal rate.
// Celestron changed it between generations and the model byte is the only way to tell: the early
// mounts (NexStar GPS and the i-Series) use 512, everything since uses 1024. Guessing wrong scales
// the whole correction by two — which looks like a mount that got half-fixed, not like a bug.
func pecRateScale(model byte) float64 {
	if model <= 2 {
		return 512
	}
	return 1024
}

// pecWormArcsec is how much RA one worm revolution covers. All current models turn 2° per worm; only
// model 8 differs at 1°.
func pecWormArcsec(model byte) float64 {
	if model == 8 {
		return 3600
	}
	return 7200
}

// parseSlewing reads the reply to `L`. It answers in ASCII '0'/'1' — unlike `J` next door, which
// answers with a binary byte. Mixing the two up reads "slewing" as "not slewing" forever.
func parseSlewing(reply string) bool {
	return strings.HasPrefix(strings.TrimSpace(reply), "1")
}

// parseAligned reads the reply to `J`, whose payload is a BINARY 0 or 1 byte.
func parseAligned(reply string) bool {
	body := strings.TrimSuffix(reply, "#")
	return len(body) > 0 && body[0] == 1
}

// parseVersion reads the reply to `V`: two binary bytes, major and minor.
func parseVersion(reply string) string {
	body := strings.TrimSuffix(reply, "#")
	if len(body) < 2 {
		return ""
	}
	return fmt.Sprintf("%d.%02d", body[0], body[1])
}

// modelNames maps the `m` reply onto a human name. Celestron's published table stops before the AVX;
// the later entries come from the INDI driver's table, which is the de-facto reference.
var modelNames = map[byte]string{
	1: "NexStar GPS", 3: "NexStar i-Series", 4: "NexStar i-Series SE", 5: "CGE",
	6: "Advanced GT", 7: "NexStar SLT", 9: "CPC", 10: "GT", 11: "NexStar 4/5 SE",
	12: "NexStar 6/8 SE", 13: "CGE Pro", 14: "CGEM DX", 15: "LCM", 16: "Sky Prodigy",
	17: "CPC Deluxe", 18: "GT 16", 19: "StarSeeker", 20: "Advanced VX", 21: "Cosmos",
	22: "Evolution", 23: "CGX", 24: "CGX-L", 25: "Astro Fi", 26: "SkyWatcher",
}

func parseModel(reply string) string {
	body := strings.TrimSuffix(reply, "#")
	if len(body) < 1 {
		return ""
	}
	if name, ok := modelNames[body[0]]; ok {
		return name
	}
	return fmt.Sprintf("model %d", body[0])
}

// parseModelCode returns the raw model byte. The PEC rate scale and worm length branch on it, and the
// display name is lossy — an unrecognised mount renders as "model 42", which cannot be mapped back.
func parseModelCode(reply string) byte {
	body := strings.TrimSuffix(reply, "#")
	if len(body) < 1 {
		return 0
	}
	return body[0]
}

// parsePierSide reads the reply to `p` ("E#"/"W#"). The command is undocumented by Celestron but
// implemented by every driver; an unrecognised reply yields "" rather than a guess.
func parsePierSide(reply string) string {
	switch strings.TrimSuffix(strings.TrimSpace(reply), "#") {
	case "E":
		return "east"
	case "W":
		return "west"
	}
	return ""
}
