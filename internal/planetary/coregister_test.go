package planetary

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/comet"
	"github.com/verove-jordan/astronomy/internal/fits"
)

// TestCoRegisterMasters_AlignsChannelsLeavesRefUntouched: each channel master stacks to its own sharpest
// frame, so the channels start misaligned; coRegisterMasters warps every non-reference master onto the
// reference with the AP-field warp (fixing corner residuals a global shift can't), and leaves the
// reference byte-for-byte.
func TestCoRegisterMasters_AlignsChannelsLeavesRefUntouched(t *testing.T) {
	dir := t.TempDir()
	ref := texturedDisk(256, 256) // stands in for the L master
	lBase := filepath.Join(dir, "master_L")
	require.NoError(t, ref.WriteFITS(lBase+".fits"))

	shifted := comet.Translate(ref, 3, -2) // the R master, misaligned vs L
	rBase := filepath.Join(dir, "master_R")
	require.NoError(t, shifted.WriteFITS(rBase+".fits"))
	before := ssd(ref, shifted)

	require.NoError(t, coRegisterMasters(map[string]string{"L": lBase, "R": rBase}, "L"))

	gotR, err := fits.ReadImage(rBase + ".fits")
	require.NoError(t, err)
	after := ssd(ref, gotR)
	assert.Less(t, after, before*0.5,
		"R master aligned onto L (before=%.4g after=%.4g)", before, after)

	// The reference channel is never resampled.
	gotL, err := fits.ReadImage(lBase + ".fits")
	require.NoError(t, err)
	assert.Zero(t, ssd(ref, gotL), "L (reference) master left untouched")
}
