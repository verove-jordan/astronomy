package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/graxpert"
	"github.com/verove-jordan/astronomy/internal/mode"
)

// fakeHealthyGraxpert writes an executable script that mimics a working GraXpert: it copies the
// input FITS to the -output path, which satisfies the deep health probe (graxpert.Healthy).
func fakeHealthyGraxpert(t *testing.T) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "graxpert-fake.sh")
	body := `#!/bin/sh
in="$1"; shift
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-output" ]; then out="$2"; shift; fi
  shift
done
[ -n "$out" ] && cp "$in" "$out"
`
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))
	return script
}

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
			"GraXpert healthy → gentle 1, never 0",
			Options{Preset: &mode.Preset{BackgroundAI: true, BackgroundDegree: 3}, Graxpert: graxpert.New(fakeHealthyGraxpert(t), "")},
			1,
		},
		{
			// /bin/echo resolves (Available passes) but produces no output, so the deep probe fails:
			// a present-but-broken GraXpert must NOT capture the gradient path — the preset degree
			// stays in charge so Siril's subsky compensates.
			"GraXpert present but broken → preset degree",
			Options{Preset: &mode.Preset{BackgroundAI: true, BackgroundDegree: 3}, Graxpert: graxpert.New("/bin/echo", "")},
			3,
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
