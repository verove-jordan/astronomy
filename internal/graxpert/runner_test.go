package graxpert

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBackgroundArgs(t *testing.T) {
	tests := []struct {
		name string
		opts BackgroundOptions
		want []string
	}{
		{
			name: "cpu (default)",
			opts: BackgroundOptions{},
			want: []string{"in.fits", "-cmd", "background-extraction", "-output", "out.fits", "-gpu", "false"},
		},
		{
			name: "gpu enabled",
			opts: BackgroundOptions{GPU: true},
			want: []string{"in.fits", "-cmd", "background-extraction", "-output", "out.fits", "-gpu", "true"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, backgroundArgs("in.fits", "out.fits", tt.opts))
		})
	}
}

func TestDenoiseArgs(t *testing.T) {
	assert.Equal(t,
		[]string{"in.fits", "-cmd", "denoising", "-output", "out.fits", "-gpu", "true"},
		denoiseArgs("in.fits", "out.fits", DenoiseOptions{GPU: true}))
	// batch + strength are denoise-only knobs, appended only when set.
	assert.Equal(t,
		[]string{"in.fits", "-cmd", "denoising", "-output", "out.fits", "-gpu", "false", "-batch_size", "16", "-strength", "0.80"},
		denoiseArgs("in.fits", "out.fits", DenoiseOptions{Batch: 16, Strength: 0.8}))
}

func TestParsePercent(t *testing.T) {
	tests := []struct {
		name string
		line string
		want int
	}{
		{"plain percent", "Processing 42%", 42},
		{"spaced percent", "done 100 %", 100},
		{"over 100 rejected", "value 250%", -1},
		{"no percent", "loading model", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parsePercent(tt.line))
		})
	}
}

func TestFirstErrorLine(t *testing.T) {
	// GraXpert can exit 0 after a fatal error, so we scan the log for its "Critical error" marker.
	withErr := "INFO Starting\nERROR    Critical error!  The required ONNX Runtime is misconfigured.\nINFO done"
	assert.Equal(t, "ERROR    Critical error!  The required ONNX Runtime is misconfigured.", firstErrorLine(withErr))
	assert.Empty(t, firstErrorLine("INFO Starting\nINFO download successful\nINFO done"))
}

func TestAvailable_EmptyBin(t *testing.T) {
	assert.Error(t, New("", "").Available(context.Background()))
}
