package siril

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/stackalg"
)

// TestStackClause_DefaultsAreByteIdentical is the regression guard for the whole feature: with the
// recipes every call site was written against, the rendered clause must equal the literal string the
// engine emitted before stacking became configurable. A change here is a change to every master ever
// produced, so it must be deliberate.
func TestStackClause_DefaultsAreByteIdentical(t *testing.T) {
	weighted := stackalg.DefaultLights()
	weighted.Weight = stackalg.WeightWFWHM

	tests := []struct {
		name   string
		opts   stackalg.Options
		frames int
		want   string
	}{
		{"light stack, mid range", stackalg.DefaultLights(), 30, "rej winsorized 3 3 -norm=addscale -output_norm"},
		{"light stack, tiny", stackalg.DefaultLights(), 5, "rej percentile 0.2 0.1 -norm=addscale -output_norm"},
		{"light stack, deep", stackalg.DefaultLights(), 60, "rej generalized 0.3 0.05 -norm=addscale -output_norm"},
		{"light stack, weighted", weighted, 30, "rej winsorized 3 3 -norm=addscale -output_norm -weight=wfwhm"},
		{"master bias/dark", stackalg.DefaultMasters().Dark, 30, "rej winsorized 3 3 -nonorm"},
		{"master flat", stackalg.DefaultMasters().Flat, 30, "rej winsorized 3 3 -norm=mul"},
		{"comet-aligned stack", stackalg.DefaultComet(), 30, "rej winsorized 4 1.8 -norm=addscale -output_norm"},
		{"channel integration", stackalg.DefaultChannelIntegration(), 0, "rej none -norm=addscale -output_norm"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, StackClause(tt.opts, tt.frames))
		})
	}
}

// TestStackClause_UserChoices renders each configurable dimension, pinning the grammar the live
// syntax test then proves Siril actually honours.
func TestStackClause_UserChoices(t *testing.T) {
	base := stackalg.DefaultLights()
	with := func(f func(*stackalg.Options)) stackalg.Options {
		o := base
		f(&o)
		return o
	}

	tests := []struct {
		name string
		opts stackalg.Options
		want string
	}{
		{
			"linear-fit clipping for a moving gradient",
			with(func(o *stackalg.Options) { o.Reject = stackalg.RejectLinearFit }),
			"rej linear 5 3.5 -norm=addscale -output_norm",
		},
		{
			"explicit sigma clipping with custom kappas",
			with(func(o *stackalg.Options) { o.Reject, o.Low, o.High = stackalg.RejectSigma, 2.5, 2 }),
			"rej sigma 2.5 2 -norm=addscale -output_norm",
		},
		{
			"MAD clipping",
			with(func(o *stackalg.Options) { o.Reject = stackalg.RejectMAD }),
			"rej mad 3 3 -norm=addscale -output_norm",
		},
		{
			"median stacking takes no rejection clause",
			with(func(o *stackalg.Options) { o.Combine = stackalg.CombineMedian }),
			"med -norm=addscale -output_norm",
		},
		{
			"sum stacking takes neither normalization nor weighting",
			with(func(o *stackalg.Options) { o.Combine, o.Weight = stackalg.CombineSum, stackalg.WeightNoise }),
			"sum",
		},
		{
			"max stacking for star trails",
			with(func(o *stackalg.Options) { o.Combine = stackalg.CombineMax }),
			"max",
		},
		{
			"fast estimators and rejection maps",
			with(func(o *stackalg.Options) { o.FastNorm, o.RejMaps = true, true }),
			"rej winsorized 3 3 -norm=addscale -fastnorm -output_norm -rejmaps",
		},
		{
			"feathered frame borders",
			with(func(o *stackalg.Options) { o.Feather = 30 }),
			"rej winsorized 3 3 -norm=addscale -output_norm -feather=30",
		},
		{
			"multiplicative normalization",
			with(func(o *stackalg.Options) { o.Norm = stackalg.NormMulScale }),
			"rej winsorized 3 3 -norm=mulscale -output_norm",
		},
		{
			"un-normalized",
			with(func(o *stackalg.Options) { o.Norm = stackalg.NormNone }),
			"rej winsorized 3 3 -nonorm -output_norm",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StackClause(tt.opts, 30)
			assert.Equal(t, tt.want, got)
			// 'additive' is accepted and then SILENTLY IGNORED by Siril — the enum must never emit it.
			assert.NotContains(t, got, "-norm=additive")
		})
	}
}

// TestStackClause_NativeAlgorithmNeverEmitsJunk: an algorithm only the Go combiner implements has no
// Siril token, so if it ever reached this renderer it must degrade to a valid command rather than
// emit a word Siril would reject (the engine routes it to stacknative long before here).
func TestStackClause_NativeAlgorithmNeverEmitsJunk(t *testing.T) {
	o := stackalg.DefaultLights()
	o.Reject = stackalg.RejectEntropyWeighted
	assert.Equal(t, "rej none -norm=addscale -output_norm", StackClause(o, 30))
}
