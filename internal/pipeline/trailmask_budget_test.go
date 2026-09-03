package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTrailBasisPlan(t *testing.T) {
	const gib = int64(1) << 30
	// One 26 MP plane is ~104 MiB; a debayered OSC frame is three of them.
	monoFrame := int64(6248) * 4176 * 4
	oscFrame := monoFrame * 3

	tests := []struct {
		name         string
		nFrames      int
		frameBytes   int64
		budget       int64
		wantStreamed bool
		wantBasis    int
	}{
		{
			name: "a small sequence stays in memory", nFrames: 20, frameBytes: monoFrame,
			budget: 40 * gib, wantStreamed: false, wantBasis: 0,
		},
		{
			// The regression: this is the NGC 7000 run. Counting one plane instead of three put
			// the basis at the 48 ceiling (~15 GiB) and killed the engine.
			name: "a 99-sub OSC stack budgets a small basis", nFrames: 99, frameBytes: oscFrame,
			budget: 15 * gib, wantStreamed: true, wantBasis: 19,
		},
		{
			// Same frames counted as mono — what the old code did — asks for 48.
			name: "the same run mis-sized as mono asks for the 48 ceiling", nFrames: 99, frameBytes: monoFrame,
			budget: 15 * gib, wantStreamed: true, wantBasis: 48,
		},
		{
			name: "the basis never drops below the 16-frame floor", nFrames: 99, frameBytes: oscFrame,
			budget: 2 * gib, wantStreamed: true, wantBasis: 16,
		},
		{
			name: "the basis never exceeds the sequence", nFrames: 12, frameBytes: oscFrame,
			budget: 1 * gib, wantStreamed: true, wantBasis: 12,
		},
		{
			name: "an unreadable frame size disables the gate", nFrames: 99, frameBytes: 0,
			budget: 1 * gib, wantStreamed: false, wantBasis: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			streamed, basis := trailBasisPlan(tt.nFrames, tt.frameBytes, tt.budget)
			assert.Equal(t, tt.wantStreamed, streamed, "streamed")
			assert.Equal(t, tt.wantBasis, basis, "basisMax")
		})
	}
}
