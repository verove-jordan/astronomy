package starnet

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRemoveArgs(t *testing.T) {
	tests := []struct {
		name string
		opts Options
		want []string
	}{
		{"default stride omitted", Options{}, []string{"in.tif", "out.tif"}},
		{"explicit stride appended", Options{Stride: 128}, []string{"in.tif", "out.tif", "128"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, removeArgs("in.tif", "out.tif", tt.opts))
		})
	}
}

func TestParsePercent(t *testing.T) {
	tests := []struct {
		name string
		line string
		want int
	}{
		{"plain", "Done: 75%", 75},
		{"no percent", "Loading model", -1},
		{"over 100", "weird 999%", -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, parsePercent(tt.line))
		})
	}
}

func TestAvailable_EmptyBin(t *testing.T) {
	assert.Error(t, New("").Available(context.Background()))
}
