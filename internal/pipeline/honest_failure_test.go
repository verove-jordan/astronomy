package pipeline

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/postprocess"
)

// TestCombineFailure pins the honest-status contract: a multi-channel run that produced no final
// image is a FAILURE (task #312 finished "Réussi" with no combined/colorcal/final at all), while a
// lone-channel run, a cancelled run and a successful combine keep their semantics.
func TestCombineFailure(t *testing.T) {
	stacked := func(n int) []ChannelResult {
		out := make([]ChannelResult, n)
		for i := range out {
			out[i] = ChannelResult{Filter: "L", OutputPath: "/out/master.fits"}
		}
		return out
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	cases := []struct {
		name    string
		ctx     context.Context
		res     *Result
		wantErr string
	}{
		{
			name: "multi-channel with no final fails with the combine cause",
			ctx:  context.Background(),
			res: &Result{Channels: stacked(4), Warnings: []string{
				"channel combination failed: rgbcomp: images must have the same dimensions",
			}},
			wantErr: "channel combination failed",
		},
		{
			name:    "multi-channel with no final and no recorded cause still fails",
			ctx:     context.Background(),
			res:     &Result{Channels: stacked(2)},
			wantErr: "no final image was produced",
		},
		{
			name: "a successful combine is not a failure",
			ctx:  context.Background(),
			res:  &Result{Channels: stacked(4), Final: &postprocess.Result{}},
		},
		{
			name: "a lone channel's master is the deliverable",
			ctx:  context.Background(),
			res:  &Result{Channels: stacked(1)},
		},
		{
			name: "channels that themselves failed don't double-report",
			ctx:  context.Background(),
			res:  &Result{Channels: []ChannelResult{{Err: "grading: boom"}, {OutputPath: "/out/m.fits"}}},
		},
		{
			name:    "zero stacked masters is a failure (disk-full run said succeeded)",
			ctx:     context.Background(),
			res:     &Result{Channels: []ChannelResult{{Filter: "L"}, {Filter: "R"}}},
			wantErr: "no channel produced a stacked master",
		},
		{
			name:    "a lone failed channel surfaces its own error",
			ctx:     context.Background(),
			res:     &Result{Channels: []ChannelResult{{Filter: "L", Err: "grading: boom"}}},
			wantErr: "L: grading: boom",
		},
		{
			name: "a cancelled run stays a cancellation",
			ctx:  cancelled,
			res:  &Result{Channels: stacked(4)},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := combineFailure(tc.ctx, tc.res)
			if tc.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
		})
	}
}

// TestMasterDimsMismatch pins the co-registration pre-flight: mixed master dimensions are named
// once, honestly, instead of failing three Siril steps in a row.
func TestMasterDimsMismatch(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, w, h int) string {
		p := filepath.Join(dir, name)
		require.NoError(t, fits.NewImage(w, h, 1).WriteFITS(p))
		return p
	}
	t.Run("equal canvases pass silently", func(t *testing.T) {
		masters := map[string]string{"L": write("l.fits", 64, 48), "R": write("r.fits", 64, 48)}
		assert.Empty(t, masterDimsMismatch(masters, []string{"L", "R"}))
	})
	t.Run("the task #312 shape is named channel by channel", func(t *testing.T) {
		masters := map[string]string{
			"L": write("l2.fits", 32, 16), // the min-framing sliver
			"G": write("g2.fits", 64, 48),
		}
		warn := masterDimsMismatch(masters, []string{"L", "G"})
		assert.Contains(t, warn, "mixed dimensions")
		assert.Contains(t, warn, "L 32×16")
		assert.Contains(t, warn, "G 64×48")
	})
}
