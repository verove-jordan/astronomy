package grade

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The R lines are verbatim Siril 1.4.3 output (from the live syntax test's planted-drift register):
// "R0 fwhm wfwhm roundness quality background nbstars H h00 h01 h02 h10 h11 h12 h20 h21 h22".
const seqFixture = `S 'light_' 1 3 3 2 4 6 0 0 0
I 1 1
I 2 1
I 3 0
R0 7.28766 7.28766 0.971388 0 0.0131527 25 H 1.00001 -1.13159e-05 7.99992 2.32884e-05 0.999998 -4.00171 8.49273e-08 -4.87644e-08 1
R0 7.28553 7.28553 0.971923 0 0.0131605 25 H 1 0 0 0 1 0 0 0 1
R0 0 0 0 0 0 0
`

func TestParseSeq_ShiftsFromHomography(t *testing.T) {
	path := filepath.Join(t.TempDir(), "light_.seq")
	require.NoError(t, os.WriteFile(path, []byte(seqFixture), 0o644))

	seq, err := ParseSeq(path)
	require.NoError(t, err)
	require.Len(t, seq.Metrics, 3)
	require.Len(t, seq.Included, 3)
	assert.False(t, seq.Included[2])

	// Registered frame: translation read from the homography's h02/h12.
	assert.InDelta(t, 7.99992, seq.Metrics[0].ShiftX, 1e-9)
	assert.InDelta(t, -4.00171, seq.Metrics[0].ShiftY, 1e-9)
	assert.InDelta(t, 7.28766, seq.Metrics[0].FWHM, 1e-9)

	// Reference frame: identity homography → zero shift.
	assert.Zero(t, seq.Metrics[1].ShiftX)
	assert.Zero(t, seq.Metrics[1].ShiftY)

	// Unregistered frame (all-zero R line, no H matrix): parses with zero shift, no error.
	assert.Zero(t, seq.Metrics[2].FWHM)
	assert.Zero(t, seq.Metrics[2].ShiftX)
}
