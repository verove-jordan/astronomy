package pipeline

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFinishQualityWarnings(t *testing.T) {
	tests := []struct {
		name string
		m    finishMetrics
		want []string // substrings that must appear, in any order; empty = no warnings
	}{
		{"clean", finishMetrics{Background: 0.05}, nil},
		{"warm sky", finishMetrics{WarmCast: 0.03}, []string{"warm sky cast"}},
		{"magenta signal", finishMetrics{SignalCast: -0.05}, []string{"magenta/pink"}},
		{"green signal", finishMetrics{SignalCast: 0.05}, []string{"green cast in the bright signal"}},
		{"green background", finishMetrics{GreenCast: 0.03}, []string{"green background cast"}},
		{"blown stars", finishMetrics{WhiteClip: [3]float64{0.02, 0, 0}}, []string{"blown highlights"}},
		{
			"warm at threshold is fine",
			finishMetrics{WarmCast: warmCastMax},
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := finishQualityWarnings(tt.m)
			if len(tt.want) == 0 {
				assert.Empty(t, got)
				return
			}
			joined := strings.Join(got, " | ")
			for _, w := range tt.want {
				assert.Contains(t, joined, w)
			}
		})
	}
}
