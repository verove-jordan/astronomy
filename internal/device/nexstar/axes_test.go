package nexstar

import (
	"math"
	"testing"
)

// A shaft angle is a circle, and the encoding is the one place that can silently turn a small slew
// into a long way round. These pin the wrap and the byte order rather than the arithmetic.

func TestAxisPositionRoundTrip(t *testing.T) {
	for _, deg := range []float64{0, 0.077, 45, 90, 179.9, 180, 270, 359.99} {
		got, err := decodeAxisPosition(encodeAxisPosition(deg))
		if err != nil {
			t.Fatalf("decode(encode(%v)): %v", deg, err)
		}
		// One encoder unit is 360/2^24 degrees; a round trip may lose half of one to rounding.
		if math.Abs(got-deg) > 360.0/axisRevolution {
			t.Errorf("round trip %v = %v, want within one encoder unit", deg, got)
		}
	}
}

func TestAxisPositionWraps(t *testing.T) {
	// Negative and past-full angles must land on their in-circle equivalents, not clamp to the ends.
	for _, tc := range []struct{ in, want float64 }{
		{-90, 270},
		{-0.0001, 359.9999}, // just below the seam wraps to just below a full turn, never to zero
		{360, 0},
		{450, 90},
	} {
		got, err := decodeAxisPosition(encodeAxisPosition(tc.in))
		if err != nil {
			t.Fatalf("decode(encode(%v)): %v", tc.in, err)
		}
		// Compared around the circle, not along the number line: 359.9999° is one encoder unit from
		// 0°, and a linear comparison would call that a 360° error.
		if diff := math.Abs(math.Mod(got-tc.want+540, 360) - 180); diff > 360.0/axisRevolution {
			t.Errorf("encode(%v) decoded to %v, want %v (off by %v)", tc.in, got, tc.want, diff)
		}
	}
}

func TestAxisPositionByteOrder(t *testing.T) {
	// Most-significant byte first: a quarter turn is 0x400000, not 0x004000.
	if got := encodeAxisPosition(90); got[0] != 0x40 || got[1] != 0 || got[2] != 0 {
		t.Errorf("encode(90) = % x, want 40 00 00", got)
	}
	got, err := decodeAxisPosition([]byte{0x80, 0x00, 0x00})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if math.Abs(got-180) > 1e-9 {
		t.Errorf("decode(80 00 00) = %v, want 180", got)
	}
}

func TestAxisPositionShortReply(t *testing.T) {
	// A short read means the port is out of step; answering zero degrees would be a lie that points
	// a telescope at the pole.
	if _, err := decodeAxisPosition([]byte{0x40, 0x00}); err == nil {
		t.Fatal("want an error for a two-byte reply, got nil")
	}
}

func TestGetPositionFrameIsARead(t *testing.T) {
	// Classified wrong, a timed-out position read either never retries or retries something that
	// moves the mount.
	if got := classify(passthrough(axisAzmRA, mcGetPosition, nil, 3)); got != retryAfterResync {
		t.Errorf("classify(get position) = %v, want retryAfterResync", got)
	}
	if got := classify(passthrough(axisAzmRA, mcGotoFast, encodeAxisPosition(90), 0)); got != retryNever {
		t.Errorf("classify(goto fast) = %v, want retryNever", got)
	}
	if got := classify(passthrough(axisAzmRA, mcSetPosition, encodeAxisPosition(90), 0)); got != retryNever {
		t.Errorf("classify(set position) = %v, want retryNever", got)
	}
}
