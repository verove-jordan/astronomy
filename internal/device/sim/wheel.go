package sim

import (
	"context"
	"fmt"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
)

// moveTimePerSlot is how long the simulated wheel takes per slot of travel. Real EFW moves take a
// second or two, and the sequencer MUST wait them out — exposing through a half-open filter is a
// classic way to silently ruin a sub, so the simulator makes the wait real.
const moveTimePerSlot = 600 * time.Millisecond

// Wheel is a simulated ZWO EFW. Like the real SDK, SetPosition returns immediately and the position
// reads back as "moving" until it lands.
type Wheel struct {
	world     *World
	connected bool
}

// NewWheel builds a simulated filter wheel bound to a world.
func NewWheel(w *World) *Wheel { return &Wheel{world: w} }

func (f *Wheel) Connect(context.Context) error {
	f.connected = true
	return nil
}

func (f *Wheel) Close() error {
	f.connected = false
	return nil
}

func (f *Wheel) Connected() bool { return f.connected }

func (f *Wheel) State() (device.WheelState, error) {
	if !f.connected {
		return device.WheelState{}, device.ErrNotConnected
	}
	f.world.mu.Lock()
	defer f.world.mu.Unlock()
	moving := f.world.now().Before(f.world.wheelUntil)
	pos := f.world.filterSlot
	if moving {
		pos = 0 // matches the SDK's "-1 while moving" semantics, normalised to 0 = unknown
	}
	slots := f.world.cfg.WheelSlots
	// The slot count comes from the simulated DEVICE, never from how many names happen to be set —
	// deriving it from the names let a 7-filter configuration turn a 5-slot wheel into a 7-slot one,
	// which real hardware would never do.
	names := device.FitFilterNames(f.world.filterNames, slots)
	return device.WheelState{
		Info: device.Info{ID: "sim-wheel",
			// Named from the actual slot count: claiming "5×36mm" while reporting seven slots is the
			// kind of small inconsistency that sends someone debugging the wrong thing.
			Name:   fmt.Sprintf("Simulated EFW %d×36mm", slots),
			Driver: "sim", Kind: device.KindWheel},
		Slots: slots, Position: pos, Moving: moving, Names: names,
	}, nil
}

func (f *Wheel) SetPosition(slot int) error {
	if !f.connected {
		return device.ErrNotConnected
	}
	f.world.mu.Lock()
	defer f.world.mu.Unlock()
	// Bounded by the DEVICE's slot count, not by how many names are set — those are user
	// configuration and can be shorter or (before fitting) longer than the wheel.
	if slots := f.world.cfg.WheelSlots; slot < 1 || slot > slots {
		return fmt.Errorf("slot %d out of range 1..%d", slot, slots)
	}
	now := f.world.now()
	if now.Before(f.world.wheelUntil) {
		return fmt.Errorf("%w: wheel is moving", device.ErrBusy)
	}
	steps := slot - f.world.filterSlot
	if steps < 0 {
		steps = -steps
	}
	f.world.filterSlot = slot
	f.world.wheelUntil = now.Add(time.Duration(steps) * moveTimePerSlot)
	return nil
}

// WaitSettled polls until the wheel stops or ctx ends.
func (f *Wheel) WaitSettled(ctx context.Context) error {
	for {
		st, err := f.State()
		if err != nil {
			return err
		}
		if !st.Moving {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (f *Wheel) Calibrate(ctx context.Context) error {
	if !f.connected {
		return device.ErrNotConnected
	}
	f.world.mu.Lock()
	f.world.wheelUntil = f.world.now().Add(2 * time.Second)
	f.world.filterSlot = 1
	f.world.mu.Unlock()
	return f.WaitSettled(ctx)
}

// SetFilterNames renames the slots (the UI's slot → filter mapping).
func (f *Wheel) SetFilterNames(names []string) {
	f.world.mu.Lock()
	defer f.world.mu.Unlock()
	f.world.filterNames = device.FitFilterNames(names, f.world.cfg.WheelSlots)
}

// filterNameLocked is the filter currently in the beam; the renderer uses it for throughput.
// Caller holds world.mu.
func (w *World) filterNameLocked() string {
	i := w.filterSlot - 1
	if i < 0 || i >= len(w.filterNames) {
		return "L"
	}
	return w.filterNames[i]
}
