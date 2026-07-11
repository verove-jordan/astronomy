package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/mode"
)

// runOptionsFrom snapshots the resolved preset toggles into run.json's provenance block; a nil preset
// yields nil so the field stays absent.
func TestRunOptionsFrom(t *testing.T) {
	assert.Nil(t, runOptionsFrom(nil))

	p := mode.For(mode.Deepsky)
	o := runOptionsFrom(&p)
	require.NotNil(t, o)
	assert.Equal(t, "deepsky", o.Mode)
	assert.True(t, o.ColorCalibration, "deepsky calibrates colour")
	assert.True(t, o.Denoise, "deepsky denoises chroma/lum")
	assert.Equal(t, "wfwhm", o.StackWeight)
}
