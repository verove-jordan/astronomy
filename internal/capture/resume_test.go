package capture

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// What a half-finished night still owes. Every case here is one that actually happens at the
// telescope, which is why the rule is a count per channel rather than a position in a list.
func TestRemaining(t *testing.T) {
	lrgb := Sequence{Steps: []Step{
		{Filter: "L", Count: 20, ExposureUs: 60_000_000, Gain: 100, Offset: 50, Bin: 1, DitherN: 5},
		{Filter: "R", Count: 10, ExposureUs: 60_000_000, Gain: 100, Offset: 50, Bin: 1, DitherN: 5},
		{Filter: "Ha", Count: 30, ExposureUs: 300_000_000, Gain: 200, Offset: 50, Bin: 1, DitherN: 5},
	}}

	tests := []struct {
		name string
		seq  Sequence
		done []FrameTally
		want []Step // filter and the count still owed
	}{
		{
			name: "nothing captured leaves the whole plan",
			seq:  lrgb,
			want: []Step{{Filter: "L", Count: 20}, {Filter: "R", Count: 10}, {Filter: "Ha", Count: 30}},
		},
		{
			name: "a partly shot channel keeps the balance",
			seq:  lrgb,
			done: []FrameTally{{Filter: "L", Type: "light", Count: 12}},
			want: []Step{{Filter: "L", Count: 8}, {Filter: "R", Count: 10}, {Filter: "Ha", Count: 30}},
		},
		{
			name: "a finished channel is dropped entirely",
			seq:  lrgb,
			done: []FrameTally{{Filter: "L", Type: "light", Count: 20}},
			want: []Step{{Filter: "R", Count: 10}, {Filter: "Ha", Count: 30}},
		},
		{
			name: "an interleaved night leaves each channel its own balance",
			seq:  lrgb,
			done: []FrameTally{
				{Filter: "L", Type: "light", Count: 8},
				{Filter: "R", Type: "light", Count: 7},
				{Filter: "Ha", Type: "light", Count: 5},
			},
			want: []Step{{Filter: "L", Count: 12}, {Filter: "R", Count: 3}, {Filter: "Ha", Count: 25}},
		},
		{
			name: "an alias in the record still matches the step",
			seq:  Sequence{Steps: []Step{{Filter: "SII", Count: 10, ExposureUs: 1, Bin: 1}}},
			done: []FrameTally{{Filter: "S2", Type: "light", Count: 4}},
			want: []Step{{Filter: "SII", Count: 6}},
		},
		{
			name: "an empty type is a light frame, as it is everywhere else",
			seq:  Sequence{Steps: []Step{{Filter: "L", Count: 10, ExposureUs: 1, Bin: 1}}},
			done: []FrameTally{{Filter: "L", Type: "", Count: 4}},
			want: []Step{{Filter: "L", Count: 6}},
		},
		{
			name: "darks do not pay off lights",
			seq:  Sequence{Steps: []Step{{Filter: "L", Count: 10, ExposureUs: 1, Bin: 1}}},
			done: []FrameTally{{Filter: "L", Type: "dark", Count: 10}},
			want: []Step{{Filter: "L", Count: 10}},
		},
		{
			name: "two steps of one channel are filled in order",
			seq: Sequence{Steps: []Step{
				{Filter: "L", Count: 20, ExposureUs: 60_000_000, Bin: 1},
				{Filter: "L", Count: 20, ExposureUs: 5_000_000, Bin: 1},
			}},
			done: []FrameTally{{Filter: "L", Type: "light", Count: 30}},
			want: []Step{{Filter: "L", Count: 10}},
		},
		{
			name: "more frames than planned leaves nothing",
			seq:  Sequence{Steps: []Step{{Filter: "L", Count: 10, ExposureUs: 1, Bin: 1}}},
			done: []FrameTally{{Filter: "L", Type: "light", Count: 40}},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Remaining(tt.seq, tt.done)

			require.Len(t, got.Steps, len(tt.want))
			for i, want := range tt.want {
				assert.Equal(t, want.Filter, got.Steps[i].Filter, "step %d filter", i)
				assert.Equal(t, want.Count, got.Steps[i].Count, "step %d count", i)
			}
		})
	}
}

// Resuming finishes THIS plan: every setting but the count is the one the night was started with.
// A resume that quietly reverted the gain would produce frames that cannot be stacked with the ones
// already on disk, which is the whole thing it exists to avoid.
func TestRemaining_KeepsEverySettingButTheCount(t *testing.T) {
	step := Step{
		Filter: "Ha", Slot: 4, Count: 30, ExposureUs: 300_000_000,
		Gain: 200, Offset: 50, Bin: 2, Type: "light", DitherN: 3, DitherPx: 12,
	}
	seq := Sequence{Name: "narrowband", Steps: []Step{step}, Interleave: true, RepeatBlock: 5}

	got := Remaining(seq, []FrameTally{{Filter: "Ha", Type: "light", Count: 11}})

	require.Len(t, got.Steps, 1)
	want := step
	want.Count = 19
	assert.Equal(t, want, got.Steps[0])
	assert.Equal(t, "narrowband", got.Name)
	assert.True(t, got.Interleave, "the interleave policy is part of the plan being finished")
	assert.Equal(t, 5, got.RepeatBlock)
}

// A finished night must be recognisable as finished, so the UI can refuse to resume it rather than
// starting a session that takes no frames.
func TestRemaining_AFinishedPlanHasNothingLeft(t *testing.T) {
	seq := Sequence{Steps: []Step{
		{Filter: "L", Count: 5, ExposureUs: 1, Bin: 1},
		{Filter: "R", Count: 5, ExposureUs: 1, Bin: 1},
	}}

	got := Remaining(seq, []FrameTally{
		{Filter: "L", Type: "light", Count: 5},
		{Filter: "R", Type: "light", Count: 5},
	})

	assert.Empty(t, got.Steps)
	assert.Zero(t, got.TotalFrames())
	assert.Error(t, got.Validate(), "an empty sequence must not be startable")
}
