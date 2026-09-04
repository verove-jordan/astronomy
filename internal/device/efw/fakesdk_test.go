package efw

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/device"
)

// A pure-Go stand-in for ZWO's EFW library, so the wheel's behaviour can be tested with no hardware
// and no vendor library present.
//
// The behaviour that matters most is the one the real SDK makes easy to get wrong: EFWGetPosition
// does NOT block and returns -1 for the whole rotation. A driver that treats that as a slot number
// starts the next exposure with a filter edge across the sensor, and the frame looks plausible while
// being unusable. The fake reproduces that timing faithfully.

type fakeWheel struct {
	mu sync.Mutex

	wheels     int32
	id         int32
	name       string
	slots      int32
	position   int32 // 0-based, as the SDK reports
	opened     bool
	moving     bool
	moveCalls  []int32
	calibrated bool
	badSlots   int32 // when non-zero, report this implausible slot count
}

func newFakeWheel() *fakeWheel {
	return &fakeWheel{wheels: 1, id: 0, name: "EFW 5x36mm", slots: 5, position: 0}
}

func (f *fakeWheel) sdk() *sdk {
	return &sdk{
		getNum: func() int32 { return f.wheels },
		getID: func(index int32, id *int32) int32 {
			if index != 0 || f.wheels == 0 {
				return 1
			}
			*id = f.id
			return 0
		},
		getProperty: func(_ int32, info *byte) int32 {
			b := unsafe.Slice(info, infoStructSize)
			for i := range b {
				b[i] = 0
			}
			binary.LittleEndian.PutUint32(b[offID:], uint32(f.id))
			copy(b[offName:], f.name)
			slots := f.slots
			if f.badSlots != 0 {
				slots = f.badSlots
			}
			binary.LittleEndian.PutUint32(b[offSlotNum:], uint32(slots))
			return 0
		},
		open:  func(int32) int32 { f.opened = true; return 0 },
		close: func(int32) int32 { f.opened = false; return 0 },
		getPosition: func(_ int32, pos *int32) int32 {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.moving {
				*pos = movingPosition // -1: the trap this driver exists to handle
				return 0
			}
			*pos = f.position
			return 0
		},
		setPosition: func(_ int32, pos int32) int32 {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.moveCalls = append(f.moveCalls, pos)
			f.position = pos
			f.moving = true // the real wheel takes seconds; it stays -1 until settle()
			return 0
		},
		getSDKVersion: func() uintptr { return 0 },
		calibrate: func(int32) int32 {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.calibrated = true
			f.moving = true // calibration turns through every slot
			return 0
		},
	}
}

// didCalibrate reads the flag safely: Calibrate runs on another goroutine in these tests.
func (f *fakeWheel) didCalibrate() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calibrated
}

// settle ends the rotation, as the hardware would.
func (f *fakeWheel) settle() {
	f.mu.Lock()
	f.moving = false
	f.mu.Unlock()
}

func fakeWheelRig(t *testing.T) (*Wheel, *fakeWheel) {
	t.Helper()
	fake := newFakeWheel()
	w := &Wheel{injected: fake.sdk()}
	require.NoError(t, w.Connect(context.Background()))
	t.Cleanup(func() { _ = w.Close() })
	return w, fake
}

func TestWheel_ConnectReadsSlotsFromTheDevice(t *testing.T) {
	w, fake := fakeWheelRig(t)
	assert.True(t, fake.opened)
	assert.Equal(t, 5, w.Slots(), "the slot count comes from the wheel, not a constant")

	st, err := w.State()
	require.NoError(t, err)
	assert.Equal(t, "EFW 5x36mm", st.Name)
	assert.Equal(t, "efw", st.Driver)
	assert.Equal(t, 5, st.Slots)
	assert.Equal(t, 1, st.Position, "the SDK is 0-based; every UI here is 1-based")
	assert.False(t, st.Moving)
}

// A wheel reporting a nonsense slot count means the struct layout does not match this SDK version,
// and continuing would mean addressing slots that do not exist.
func TestWheel_RejectsAnImplausibleSlotCount(t *testing.T) {
	fake := newFakeWheel()
	fake.badSlots = 9999
	w := &Wheel{injected: fake.sdk()}

	err := w.Connect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "EFW_INFO layout")
	assert.False(t, fake.opened, "a wheel we cannot trust must not be left open")
}

// The central contract: while the wheel turns, Position reports 0 (unknown) rather than a slot, and
// WaitSettled blocks until it really has arrived.
func TestWheel_ReportsUnknownWhileMovingAndSettles(t *testing.T) {
	w, fake := fakeWheelRig(t)

	require.NoError(t, w.SetPosition(3))
	assert.Equal(t, []int32{2}, fake.moveCalls, "slot 3 is index 2 to the SDK")

	pos, err := w.Position()
	require.NoError(t, err)
	assert.Zero(t, pos, "a moving wheel must never report a slot number")

	st, err := w.State()
	require.NoError(t, err)
	assert.True(t, st.Moving)

	// WaitSettled must actually wait.
	done := make(chan error, 1)
	go func() { done <- w.WaitSettled(context.Background()) }()
	select {
	case <-done:
		t.Fatal("WaitSettled returned while the wheel was still moving")
	case <-time.After(150 * time.Millisecond):
	}

	fake.settle()
	require.NoError(t, <-done)

	pos, err = w.Position()
	require.NoError(t, err)
	assert.Equal(t, 3, pos)
}

func TestWheel_RejectsSlotsOutsideTheWheel(t *testing.T) {
	w, fake := fakeWheelRig(t)
	assert.ErrorContains(t, w.SetPosition(0), "outside")
	assert.ErrorContains(t, w.SetPosition(6), "outside")
	assert.Empty(t, fake.moveCalls, "an impossible slot must move nothing")
}

// A cancelled context must abandon the wait rather than hang a sequence for 30 seconds.
func TestWheel_WaitSettledHonoursCancellation(t *testing.T) {
	w, _ := fakeWheelRig(t)
	require.NoError(t, w.SetPosition(2))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err := w.WaitSettled(ctx)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

// Calibration turns the wheel through every slot, so it must settle before returning — otherwise the
// caller would move on with the wheel still spinning.
func TestWheel_CalibrateWaitsForTheWheelToStop(t *testing.T) {
	w, fake := fakeWheelRig(t)

	done := make(chan error, 1)
	go func() { done <- w.Calibrate(context.Background()) }()

	select {
	case <-done:
		t.Fatal("Calibrate returned while the wheel was still turning")
	case <-time.After(100 * time.Millisecond):
	}
	assert.True(t, fake.didCalibrate())

	fake.settle()
	require.NoError(t, <-done)
}

// Slot names are what turn a slot number into a FILTER header, and the header drives per-filter
// flats and channel detection through the whole pipeline.
func TestWheel_Names(t *testing.T) {
	w, _ := fakeWheelRig(t)
	assert.Empty(t, w.Names(), "the wheel itself knows nothing about what is in it")

	w.SetFilterNames([]string{"L", "R", "G", "B", "Ha"})
	assert.Equal(t, []string{"L", "R", "G", "B", "Ha"}, w.Names())

	st, err := w.State()
	require.NoError(t, err)
	assert.Equal(t, []string{"L", "R", "G", "B", "Ha"}, st.Names)

	// The returned slice must be a copy: a caller mutating it must not rewrite the wheel's idea of
	// which filter is in which slot.
	got := w.Names()
	got[0] = "corrupted"
	assert.Equal(t, "L", w.Names()[0])
}

func TestWheel_OperationsRequireAConnection(t *testing.T) {
	w := New()
	assert.ErrorIs(t, w.SetPosition(1), device.ErrNotConnected)
	_, err := w.Position()
	assert.ErrorIs(t, err, device.ErrNotConnected)
	assert.ErrorIs(t, w.Calibrate(context.Background()), device.ErrNotConnected)
	assert.NoError(t, w.Close(), "closing an unconnected wheel is harmless")
}

func TestWheel_ConnectFailsWithNoWheel(t *testing.T) {
	fake := newFakeWheel()
	fake.wheels = 0
	w := &Wheel{injected: fake.sdk()}
	err := w.Connect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no ZWO filter wheel")
}

func TestWheel_CloseReleasesTheDevice(t *testing.T) {
	w, fake := fakeWheelRig(t)
	require.NoError(t, w.Close())
	assert.False(t, fake.opened)
	assert.False(t, w.Connected())
}

// A saved configuration from a bigger wheel must not make this one report slots it does not have:
// State().Names drives the filter picker, and offering slot 6 on a 5-slot wheel is a move that can
// only fail.
func TestWheel_NamesAreFittedToTheWheelsRealSlotCount(t *testing.T) {
	w, _ := fakeWheelRig(t) // the fake reports 5 slots
	require.Equal(t, 5, w.Slots())

	w.SetFilterNames([]string{"L", "R", "G", "B", "Ha", "OIII", "SII"})
	assert.Equal(t, []string{"L", "R", "G", "B", "Ha"}, w.Names(),
		"the two narrowband slots this wheel does not have must be dropped")

	st, err := w.State()
	require.NoError(t, err)
	assert.Len(t, st.Names, st.Slots, "the picker gets exactly one name per physical slot")

	// A short list is padded, so slot 5 stays addressable even when unnamed.
	w.SetFilterNames([]string{"L", "Ha"})
	assert.Equal(t, []string{"L", "Ha", "", "", ""}, w.Names())
}

// A wheel that has gone away must be reported as NOT CONNECTED, not as a generic failure.
//
// It is the type, not the words, that decides whether a night survives: devsrv maps this sentinel to
// code "not_connected", and the sequencer waits out anything carrying that code and carries on from
// the next frame. Unwrapped it fell through to a bare 500, and one USB hiccup on the wheel ended a
// session at frame 5 of 80.
func TestEFWCheck_AVanishedWheelIsNotConnected(t *testing.T) {
	tests := []struct {
		name         string
		code         int32
		notConnected bool
	}{
		{"removed, or open in another program", 4, true},
		{"closed", 9, true},
		{"moving is a wheel that answered", 5, false},
		{"an error state is a wheel that answered", 6, false},
		{"success is no error", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := efwCheck("EFWGetPosition", tt.code)
			if tt.code == 0 {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Equal(t, tt.notConnected, errors.Is(err, device.ErrNotConnected))
			assert.Contains(t, err.Error(), "EFWGetPosition", "the failing call stays in the message")
		})
	}
}
