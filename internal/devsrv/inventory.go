package devsrv

import (
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
)

// The device inventory: which drivers this build can use, and what hardware they can see.
//
// Both answers cost real work. The AVFoundation probe spawns an ffmpeg subprocess, the ZWO probes
// call into the vendor SDK over USB, the mount probe walks the USB tree — and the capture page asks
// for both every three seconds.
//
// Computing them per request is what made a perfectly healthy sidecar look dead. /health held the
// device mutex while it shelled out, so every request that arrived during a connect waited behind
// it — and a connect can be twenty seconds, because that is an iPhone's auto-exposure warm-up. The
// engine's health probe gave up at two, the UI concluded the device server was not running, blanked
// all three panels and told the user to start a process that was already running.
//
// So the inventory is computed off the request path and cached, under two rules:
//
//   - A driver whose device is IN USE is not re-probed. Asking libASICamera2 to enumerate cameras
//     from an HTTP goroutine while another goroutine is mid-exposure is exactly the kind of
//     concurrent vendor-SDK call that takes the process down, and there is nothing to learn from it:
//     we know that camera is there, because we are holding it. Its previous entry is reused.
//   - Only the FIRST answer may block, and only briefly. Every one after it is a memory read.
const (
	// inventoryTTL bounds staleness. Hot-plugging is a human action measured in the time it takes to
	// walk to the telescope, so ten seconds is invisible — and it is what turns /health from a
	// subprocess spawn into a map read.
	inventoryTTL = 10 * time.Second
	// firstProbeBudget is how long a request waits for the very first inventory. Paid once, in the
	// opening seconds of the process; after that the cache always has an answer to hand back.
	firstProbeBudget = 3 * time.Second
)

// inventory caches the driver report and the discovered device list, and refreshes them in the
// background.
type inventory struct {
	// busy reports which device KINDS this server currently holds, so their drivers are left alone
	// while they are working.
	busy func() map[string]bool

	mu         sync.Mutex
	drivers    []DriverStatus
	devices    []device.Info
	at         time.Time
	refreshing bool
	closed     bool

	ready     chan struct{} // closed once the first refresh has landed
	readyOnce sync.Once
}

func newInventory(busy func() map[string]bool) *inventory {
	inv := &inventory{busy: busy, ready: make(chan struct{})}
	inv.refresh()
	return inv
}

// get returns the cached inventory, kicking off a background refresh when it has gone stale. It
// blocks only while there is no answer at all, and only up to budget.
func (i *inventory) get(budget time.Duration) ([]DriverStatus, []device.Info) {
	i.mu.Lock()
	first, stale := i.at.IsZero(), time.Since(i.at) > inventoryTTL
	i.mu.Unlock()

	switch {
	case first:
		select {
		case <-i.ready:
		case <-time.After(budget):
		}
	case stale:
		i.refresh()
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	return i.drivers, i.devices
}

// invalidate drops the cached answer's freshness so the next reader triggers a refresh. Connecting
// or disconnecting a device changes what the probes would say, and waiting out the TTL for that
// would leave the panel describing the previous state.
func (i *inventory) invalidate() {
	i.mu.Lock()
	i.at = time.Now().Add(-inventoryTTL)
	i.mu.Unlock()
}

func (i *inventory) close() {
	i.mu.Lock()
	i.closed = true
	i.mu.Unlock()
}

// refresh runs one probe pass in the background. Concurrent callers collapse onto the one already
// running, so a burst of polling can never turn into a burst of USB traffic.
func (i *inventory) refresh() {
	i.mu.Lock()
	if i.refreshing || i.closed {
		i.mu.Unlock()
		return
	}
	i.refreshing = true
	prevDrivers, prevDevices := i.drivers, i.devices
	i.mu.Unlock()

	go func() {
		drivers, devices := probeAll(i.busy(), prevDrivers, prevDevices)
		i.mu.Lock()
		i.drivers, i.devices = drivers, devices
		i.at, i.refreshing = time.Now(), false
		i.mu.Unlock()
		i.readyOnce.Do(func() { close(i.ready) })
	}()
}

// probeAll asks every driver whether it is usable and what it can see, reusing the previous answer
// for any kind whose device this server is currently holding.
func probeAll(busy map[string]bool, prevDrivers []DriverStatus, prevDevices []device.Info) ([]DriverStatus, []device.Info) {
	drivers := []DriverStatus{
		{Name: DriverSim, Kind: "all", Available: true, Detail: "built-in simulator"},
	}
	for _, p := range probes {
		if p.disturbs && busy[p.kind] {
			if prev, ok := findDriver(prevDrivers, p.name); ok {
				drivers = append(drivers, prev)
				continue
			}
		}
		st := DriverStatus{Name: p.name, Kind: p.kind}
		if detail, err := p.probe(); err != nil {
			st.Detail = err.Error()
		} else {
			st.Available, st.Detail = true, detail
		}
		drivers = append(drivers, st)
	}

	devices := simDevices()
	for _, kind := range []string{device.KindCamera, device.KindWheel, device.KindMount} {
		// Same rule for the listing: enumerating a ZWO camera mid-exposure is the call that takes the
		// process down, while listing serial ports never opens one.
		if busy[kind] && kindDisturbs(kind) {
			devices = append(devices, hardwareOfKind(prevDevices, kind)...)
			continue
		}
		devices = append(devices, enumerate(kind)...)
	}
	return drivers, devices
}

// kindDisturbs reports whether ANY driver of this kind must be left alone while its device works.
func kindDisturbs(kind string) bool {
	for _, p := range probes {
		if p.kind == kind && p.disturbs {
			return true
		}
	}
	return false
}

func findDriver(list []DriverStatus, name string) (DriverStatus, bool) {
	for _, d := range list {
		if d.Name == name {
			return d, true
		}
	}
	return DriverStatus{}, false
}

// hardwareOfKind picks the real (non-simulated) entries of one kind out of a previous enumeration.
func hardwareOfKind(list []device.Info, kind string) []device.Info {
	var out []device.Info
	for _, d := range list {
		if d.Kind == kind && d.Driver != DriverSim {
			out = append(out, d)
		}
	}
	return out
}
