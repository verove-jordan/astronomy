package platesolve

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeMinimalFITS emits the smallest file fits.Open will accept: one 2880-byte header block.
func writeMinimalFITS(t *testing.T, path string) {
	t.Helper()
	var b strings.Builder
	for _, c := range []string{
		"SIMPLE  =                    T",
		"BITPIX  =                   16",
		"NAXIS   =                    0",
		"END",
	} {
		fmt.Fprintf(&b, "%-80s", c)
	}
	block := b.String()
	if pad := 2880 - len(block)%2880; pad != 2880 {
		block += strings.Repeat(" ", pad)
	}
	require.NoError(t, os.WriteFile(path, []byte(block), 0o644))
}

// The solver has to read back the file Siril actually wrote, and which extension that is comes from
// `setext` in the script header — it says `fits`. Looking only for `.fit` meant every solve failed
// with "solved file unreadable" AFTER Siril had logged "Siril solve succeeded", which sent the reader
// hunting for a bad image instead of a missing suffix.
func TestOpenSolved_FindsWhatSirilActuallyWrote(t *testing.T) {
	tests := []struct {
		name string
		ext  string
	}{
		{"the .fits that setext fits produces", ".fits"},
		{"a .fit output is still accepted", ".fit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeMinimalFITS(t, filepath.Join(dir, "solve_out"+tt.ext))

			got, err := openSolved(dir, "solve_out")

			require.NoError(t, err)
			require.NotNil(t, got)
		})
	}

	t.Run("neither spelling present is still an error", func(t *testing.T) {
		_, err := openSolved(t.TempDir(), "solve_out")
		require.Error(t, err)
	})
}
