package inspect

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestVetoBelowHorizon covers the case the batch-relative statistics cannot: a folder where the
// darks have no light to be compared against. The real session's darks were shot at -82 degrees
// with the phone face down, and a 10-second frame aimed at a table is not a light no matter what
// the rest of the batch looks like.
func TestVetoBelowHorizon(t *testing.T) {
	tests := []struct {
		name  string
		frame Frame
		want  FrameType
	}{
		{
			name:  "long exposure aimed at the ground is a dark",
			frame: Frame{Type: Light, ClassSource: SourceHeuristic, AltDeg: -82.3, HasPointing: true, ExposureMs: 10000},
			want:  Dark,
		},
		{
			name:  "very short exposure aimed at the ground is a bias",
			frame: Frame{Type: Light, ClassSource: SourceHeuristic, AltDeg: -82.4, HasPointing: true, ExposureMs: 67},
			want:  Bias,
		},
		{
			name:  "aimed at the sky is left alone",
			frame: Frame{Type: Light, ClassSource: SourceHeuristic, AltDeg: 75.6, HasPointing: true, ExposureMs: 10000},
			want:  Light,
		},
		{
			name:  "just below the horizon is still a light, since a sea cliff is a real framing",
			frame: Frame{Type: Light, ClassSource: SourceHeuristic, AltDeg: -12, HasPointing: true, ExposureMs: 10000},
			want:  Light,
		},
		{
			name:  "no pointing metadata changes nothing",
			frame: Frame{Type: Light, ClassSource: SourceHeuristic, AltDeg: 0, ExposureMs: 10000},
			want:  Light,
		},
		{
			name: "an explicit lights folder outranks the geometry",
			// Folder tokens are a statement of intent; the veto is an inference and must not
			// silently overrule one.
			frame: Frame{Type: Light, ClassSource: SourceFilename, AltDeg: -82.3, HasPointing: true, ExposureMs: 10000},
			want:  Light,
		},
		{
			name:  "a frame already called a flat keeps that finer answer",
			frame: Frame{Type: Flat, ClassSource: SourceHeuristic, AltDeg: -82.3, HasPointing: true, ExposureMs: 200},
			want:  Flat,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := tt.frame
			vetoBelowHorizon([]*Frame{&fr})
			assert.Equal(t, tt.want, fr.Type)
		})
	}
}
