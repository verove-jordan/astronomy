// Package sysmon samples the live resource usage (resident memory + CPU) of a process subtree, so
// the engine can show how much each pipeline step (siril-cli, GraXpert, StarNet++ …) is costing.
//
// It is dependency-free by design (honoring the host's GOTOOLCHAIN=local, hand-roll-deps
// constraint): each tick shells out to `ps` once, sums the root PID and its descendants, and
// derives an instantaneous CPU% from the change in cumulative CPU-time between ticks.
package sysmon

import (
	"context"
	"time"
)

// DefaultInterval is the sampling period for a Monitor when callers don't pick one.
const DefaultInterval = time.Second

// Sample is one resource reading of a process subtree.
type Sample struct {
	RSSBytes   int64   // aggregate resident set size of the subtree, in bytes
	CPUPercent float64 // instantaneous CPU usage; 100 == one core fully busy (can exceed 100)
}

// Monitor periodically samples a process subtree and reports each reading to a callback. It is
// started with Start and must be released with Stop.
type Monitor struct {
	stop chan struct{}
	done chan struct{}
}

// Start begins sampling the subtree rooted at pid every interval (<=0 → DefaultInterval), invoking
// onSample with each reading until Stop is called or ctx is cancelled. A baseline is primed
// immediately so the first reported Sample already carries a real CPU%.
func Start(ctx context.Context, pid int, interval time.Duration, onSample func(Sample)) *Monitor {
	if interval <= 0 {
		interval = DefaultInterval
	}
	m := &Monitor{stop: make(chan struct{}), done: make(chan struct{})}
	go m.run(ctx, pid, interval, onSample)
	return m
}

// Stop halts sampling and waits for the sampling goroutine to exit. It is safe to call more than
// once.
func (m *Monitor) Stop() {
	select {
	case <-m.stop: // already stopped
	default:
		close(m.stop)
	}
	<-m.done
}

func (m *Monitor) run(ctx context.Context, pid int, interval time.Duration, onSample func(Sample)) {
	defer close(m.done)

	// Prime a CPU-time baseline without emitting, so the first tick reports a real CPU%.
	prevCPU, _, err := sampleTree(ctx, pid)
	prevWall := time.Now()
	havePrev := err == nil

	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stop:
			return
		case <-tick.C:
			cpu, rss, err := sampleTree(ctx, pid)
			if err != nil {
				continue // process gone or ps hiccup; skip this tick
			}
			now := time.Now()
			pct := 0.0
			if havePrev {
				if dw := now.Sub(prevWall).Seconds(); dw > 0 {
					pct = (cpu - prevCPU).Seconds() / dw * 100
					if pct < 0 {
						pct = 0 // CPU-time only grows; a negative delta means the tree changed
					}
				}
			}
			prevCPU, prevWall, havePrev = cpu, now, true
			if onSample != nil {
				onSample(Sample{RSSBytes: rss, CPUPercent: pct})
			}
		}
	}
}
