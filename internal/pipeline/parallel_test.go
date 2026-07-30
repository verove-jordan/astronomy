package pipeline

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// stubStager satisfies InputStager (a set Stager forces the serial loop).
type stubStager struct{}

func (stubStager) Scan(context.Context, []string, inspect.ScanOptions) (*inspect.Inventory, error) {
	return nil, nil
}
func (stubStager) Ensure(context.Context, string, []string) error { return nil }
func (stubStager) Free(context.Context, string, []string)         {}
func (stubStager) Notes() []string                                { return nil }

func TestChannelParallelism(t *testing.T) {
	assert.Equal(t, 1, Options{}.channelParallelism(4), "default is the serial loop")
	assert.Equal(t, 1, Options{ChannelParallel: 0}.channelParallelism(4))
	assert.Equal(t, 2, Options{ChannelParallel: 2}.channelParallelism(4))
	assert.Equal(t, 3, Options{ChannelParallel: 8}.channelParallelism(3), "capped by the channel count")
	assert.Equal(t, 1, Options{ChannelParallel: 2, Stager: stubStager{}}.channelParallelism(4),
		"low-disk staged mode is inherently sequential")
}

func TestDividedLimits(t *testing.T) {
	lim := dividedLimits(siril.Limits{MaxCPUs: 8, MemRatio: 0.6}, 2)
	assert.Equal(t, 4, lim.MaxCPUs)
	assert.InDelta(t, 0.3, lim.MemRatio, 1e-9)

	// Zero (Siril-default) budgets are resolved before dividing, so two "unlimited" instances
	// cannot both take the whole machine.
	lim = dividedLimits(siril.Limits{}, 2)
	assert.GreaterOrEqual(t, lim.MaxCPUs, 1)
	assert.InDelta(t, 0.45, lim.MemRatio, 1e-9)

	same := siril.Limits{MaxCPUs: 8, MemRatio: 0.6}
	assert.Equal(t, same, dividedLimits(same, 1), "n=1 leaves limits untouched")
}

func TestStepperAt_FixedSlotsDoNotFightTheCursor(t *testing.T) {
	var mu sync.Mutex
	var events []Progress
	s := newStepper(func(p Progress) { mu.Lock(); events = append(events, p); mu.Unlock() }, 6)

	s.begin("building masters") // serial step 1
	s.finish()

	// Two channels in fixed slots 2 and 3 (as a parallel wave assigns them).
	fwd2, done2, ref2 := s.at(2, "stacking L")
	fwd3, done3, ref3 := s.at(3, "stacking R")
	assert.Equal(t, stepRef{Name: "stacking L", Index: 2, Total: 6}, ref2, "the slot ref pins the bar position")
	assert.Equal(t, stepRef{Name: "stacking R", Index: 3, Total: 6}, ref3)
	fwd3(siril.Progress{Line: "r-line"})
	fwd2(siril.Progress{Line: "l-line"})
	done3()
	done2()
	s.advanceTo(3)

	next := s.begin("aligning channels")
	_ = next
	s.finish()

	byLine := map[string]int{}
	for _, e := range events {
		if e.Line != "" {
			byLine[e.Line] = e.Index
		}
	}
	assert.Equal(t, 2, byLine["l-line"], "slot events keep their fixed index")
	assert.Equal(t, 3, byLine["r-line"])
	assert.Equal(t, 4, byLine["▶ aligning channels"], "the serial cursor resumes after the wave")
	assert.Contains(t, byLine, "✓ stacking L done in 0s")
	assert.Contains(t, byLine, "✓ stacking R done in 0s")
}

func TestSerializedProgress(t *testing.T) {
	assert.Nil(t, serializedProgress(nil))
	n := 0
	fn := serializedProgress(func(Progress) { n++ })
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); fn(Progress{}) }()
	}
	wg.Wait()
	assert.Equal(t, 50, n)
}
