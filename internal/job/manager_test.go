package job

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestStepPercent pins the progress mapping that fixes the "100% while still working" bar: a running
// step is half-complete, so a step never reads 100% (that is reserved for the job's "done" event), and
// the bar still advances at every step boundary.
func TestStepPercent(t *testing.T) {
	tests := []struct {
		name         string
		index, total int
		want         int
	}{
		{"no step count (planetary)", 0, 0, 0},
		{"first of four starts above zero", 1, 4, 12},
		{"second of four", 2, 4, 37},
		{"final combine of four never hits 100", 4, 4, 87},
		{"final combine of seven never hits 100", 7, 7, 92},
		{"large run final step caps below 100", 50, 50, 99},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, stepPercent(tt.index, tt.total))
		})
	}
}

// TestStepPercent_NeverReports100WhileRunning is the invariant behind the fix: for any plausible step
// count, the last running step stays below 100% so the bar can't look finished mid-run.
func TestStepPercent_NeverReports100WhileRunning(t *testing.T) {
	for total := 1; total <= 200; total++ {
		for index := 1; index <= total; index++ {
			got := stepPercent(index, total)
			assert.Less(t, got, 100, "total=%d index=%d", total, index)
			assert.GreaterOrEqual(t, got, 0, "total=%d index=%d", total, index)
		}
	}
}
