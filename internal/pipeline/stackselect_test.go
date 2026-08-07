package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/grade"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/stackalg"
)

// TestGradeChannel_M92Shape replays the real M92 Ha failure at the gradeChannel level: 8 of 10
// frames silently fail registration and one of the two survivors is flagged by grading. The
// stack-minimum floor must restore it (Siril cannot stack a single included frame), leaving no
// rejected registered frames and the restoration recorded as provenance.
func TestGradeChannel_M92Shape(t *testing.T) {
	seqDir := t.TempDir()
	var b strings.Builder
	b.WriteString("S 'light_' 1 10 10 0 1 0 0\n")
	for i := 1; i <= 10; i++ {
		b.WriteString(fmt.Sprintf("I %d 1\n", i))
	}
	for i := 0; i < 8; i++ { // unregistered: all-zero R line
		b.WriteString("R0 0 0 0 0 0 0\n")
	}
	b.WriteString("R0 3.0 3.1 0.90 0.9 0.01 40\n") // healthy survivor
	b.WriteString("R0 3.2 3.3 0.40 0.8 0.01 38\n") // flagged: roundness below the 0.55 floor
	require.NoError(t, os.WriteFile(filepath.Join(seqDir, "light_.seq"), []byte(b.String()), 0o644))

	frames := make([]*inspect.Frame, 10)
	for i := range frames {
		frames[i] = &inspect.Frame{Path: fmt.Sprintf("f%02d.fits", i+1)}
	}
	metrics, rejectedReg, regCount, err := gradeChannel(seqDir, "light", frames, grade.DefaultOptions(), false, nil, nil)
	require.NoError(t, err)

	assert.Len(t, metrics, 10)
	assert.Equal(t, 2, regCount)
	assert.Empty(t, rejectedReg, "both registered frames must reach the stack (Siril needs two)")
	restored := metrics[9]
	assert.False(t, restored.Rejected)
	assert.Contains(t, restored.RejectReason, grade.KeptStackMinimumPrefix)
	for _, m := range metrics[:8] {
		assert.True(t, m.Rejected)
		assert.Contains(t, m.RejectReason, "could not register")
	}
}

// TestStackSelectedOrCopy_LoneFrame checks the one-registered-frame degraded path: the lone
// registered frame is promoted to the channel master byte-for-byte, with a note and no Siril run.
func TestStackSelectedOrCopy_LoneFrame(t *testing.T) {
	seqDir, outDir := t.TempDir(), t.TempDir()
	payload := []byte("fits-bytes")
	require.NoError(t, os.WriteFile(filepath.Join(seqDir, "r_light_00001.fits"), payload, 0o644))

	outBase := filepath.Join(outDir, "master_Ha")
	_, note, err := stackSelectedOrCopy(context.Background(), nil, seqDir, "r_light", 1, nil, outBase, stackalg.DefaultLights(), nil)
	require.NoError(t, err)
	assert.Contains(t, note, "only 1 frame registered")
	got, err := os.ReadFile(outBase + ".fits")
	require.NoError(t, err)
	assert.Equal(t, payload, got)
}

// TestStackSelectedOrCopy_FloorViolation checks the defensive error: fewer than two survivors with
// two or more registered frames is a grading-contract violation and must never reach Siril.
func TestStackSelectedOrCopy_FloorViolation(t *testing.T) {
	_, _, err := stackSelectedOrCopy(context.Background(), nil, t.TempDir(), "r_light", 3, []int{1, 2}, "/out/m", stackalg.DefaultLights(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "two-frame stack minimum")
}
