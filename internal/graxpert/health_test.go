package graxpert

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeScript writes an executable shell script and returns its path. Each call uses a fresh
// t.TempDir, so the on-disk health cache (keyed by path+mtime) never collides across tests.
func writeScript(t *testing.T, body string) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "graxpert.sh")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\n"+body), 0o755))
	return script
}

// copyingScript mimics a working GraXpert: copies the positional input to the -output path, and
// appends one line to countFile per invocation so tests can assert how many probes ran.
func copyingScript(t *testing.T, countFile string) string {
	return writeScript(t, fmt.Sprintf(`echo run >> %q
in="$1"; shift
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-output" ]; then out="$2"; shift; fi
  shift
done
[ -n "$out" ] && cp "$in" "$out"
`, countFile))
}

func countRuns(t *testing.T, countFile string) int {
	t.Helper()
	raw, err := os.ReadFile(countFile)
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	n := 0
	for _, b := range raw {
		if b == '\n' {
			n++
		}
	}
	return n
}

func TestHealthy_WorkingTool(t *testing.T) {
	countFile := filepath.Join(t.TempDir(), "count")
	r := New(copyingScript(t, countFile))
	assert.NoError(t, r.Healthy(context.Background()))
	assert.Equal(t, 1, countRuns(t, countFile))
}

func TestHealthy_MemoizesInProcessAndOnDisk(t *testing.T) {
	countFile := filepath.Join(t.TempDir(), "count")
	bin := copyingScript(t, countFile)

	r := New(bin)
	require.NoError(t, r.Healthy(context.Background()))
	require.NoError(t, r.Healthy(context.Background())) // in-process memo
	assert.Equal(t, 1, countRuns(t, countFile))

	// A brand-new Runner (fresh process simulation) must hit the disk cache, not re-probe.
	r2 := New(bin)
	require.NoError(t, r2.Healthy(context.Background()))
	assert.Equal(t, 1, countRuns(t, countFile))
}

func TestHealthy_CriticalErrorExitZero(t *testing.T) {
	// GraXpert's signature failure mode: logs a critical ONNX error but exits 0.
	bin := writeScript(t, `echo "Critical error! The required ONNX Runtime (AI library) package is misconfigured"`)
	err := New(bin).Healthy(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ONNX")
}

func TestHealthy_NoOutputProduced(t *testing.T) {
	err := New("/bin/echo").Healthy(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no output")
}

func TestHealthy_MissingBinary(t *testing.T) {
	assert.Error(t, New("").Healthy(context.Background()))
	assert.Error(t, New("/nonexistent/graxpert").Healthy(context.Background()))
}

func TestHealthCached_UnknownThenKnown(t *testing.T) {
	countFile := filepath.Join(t.TempDir(), "count")
	r := New(copyingScript(t, countFile))

	_, known := r.HealthCached()
	assert.False(t, known, "no verdict before any probe")
	assert.Equal(t, 0, countRuns(t, countFile), "HealthCached must never probe")

	require.NoError(t, r.Healthy(context.Background()))
	verdict, known := r.HealthCached()
	assert.True(t, known)
	assert.NoError(t, verdict)
}
