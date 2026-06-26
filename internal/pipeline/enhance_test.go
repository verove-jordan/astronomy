package pipeline

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/graxpert"
	"github.com/verove-jordan/astronomy/internal/mode"
)

// backgroundDegree must always yield a degree in Siril's valid [1,4] range — Siril rejects
// `subsky 0` ("Polynomial degree order must be within the [1, 4] range"), which crashed the finish
// stage when GraXpert was active.
func TestBackgroundDegree_AlwaysInSirilRange(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		opts Options
		want int
	}{
		{"no preset → 1", Options{}, 1},
		{"preset degree 3 → 3", Options{Preset: &mode.Preset{BackgroundDegree: 3}}, 3},
		{"preset degree 0 → 1", Options{Preset: &mode.Preset{BackgroundDegree: 0}}, 1},
		{"preset degree above range → 4", Options{Preset: &mode.Preset{BackgroundDegree: 9}}, 4},
		{
			"GraXpert active → gentle 1, never 0",
			// /bin/echo makes graxpert.Available() succeed, so aiBackground is true.
			Options{Preset: &mode.Preset{BackgroundAI: true, BackgroundDegree: 3}, Graxpert: graxpert.New("/bin/echo")},
			1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := backgroundDegree(ctx, tt.opts)
			assert.Equal(t, tt.want, got)
			assert.GreaterOrEqual(t, got, 1)
			assert.LessOrEqual(t, got, 4)
		})
	}
}
