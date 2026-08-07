package stackalg

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAutoReject_AdaptsToFrameCount(t *testing.T) {
	// The historical rule, now shared by the Siril clause, the native combiner and the UI badge.
	tests := []struct {
		name  string
		count int
		want  Reject
	}{
		{"unknown count keeps winsorized", 0, RejectWinsorized},
		{"tiny stack uses percentile", 7, RejectPercentile},
		{"mid range uses winsorized (low bound)", 8, RejectWinsorized},
		{"mid range uses winsorized (high bound)", 49, RejectWinsorized},
		{"large stack uses GESD", 50, RejectGESD},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, AutoReject(tt.count))
		})
	}
}

func TestResolve_FillsAutoAndDefaults(t *testing.T) {
	tests := []struct {
		name       string
		in         Options
		frames     int
		wantReject Reject
		wantLow    float64
		wantHigh   float64
	}{
		{"auto on a mid stack", DefaultLights(), 30, RejectWinsorized, 3, 3},
		{"auto on a tiny stack", DefaultLights(), 5, RejectPercentile, 0.2, 0.1},
		{"auto on a deep stack", DefaultLights(), 60, RejectGESD, 0.3, 0.05},
		{
			"an explicit algorithm keeps its own defaults",
			Options{Reject: RejectLinearFit}, 60, RejectLinearFit, 5, 3.5,
		},
		{
			"explicit parameters survive",
			Options{Reject: RejectSigma, Low: 2.5, High: 1.5}, 30, RejectSigma, 2.5, 1.5,
		},
		{
			"a parameter beyond the algorithm's own range is pinned",
			Options{Reject: RejectPercentile, Low: 9, High: 8}, 30, RejectPercentile, 0.9, 0.9,
		},
		{
			"rejection-less stacking carries no parameters",
			Options{Reject: RejectNone, Low: 3, High: 3}, 30, RejectNone, 0, 0,
		},
		{
			"an unknown algorithm falls back to the count-adaptive choice",
			Options{Reject: Reject("nonsense")}, 60, RejectGESD, 0.3, 0.05,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(tt.in, tt.frames)
			assert.Equal(t, tt.wantReject, got.Reject)
			assert.InDelta(t, tt.wantLow, got.Low, 1e-9)
			assert.InDelta(t, tt.wantHigh, got.High, 1e-9)
			assert.Equal(t, CombineMean, got.Combine, "an unset combination resolves to the mean")
		})
	}
}

func TestEngineFor(t *testing.T) {
	tests := []struct {
		name string
		in   Options
		want Engine
	}{
		{"the defaults stay on Siril", DefaultLights(), EngineSiril},
		{"an explicit engine is honoured", Options{Engine: EngineNative}, EngineNative},
		{"a Siril algorithm stays on Siril", Options{Reject: RejectGESD}, EngineSiril},
		{"a native-only rejection moves to Go", Options{Reject: RejectEntropyWeighted}, EngineNative},
		{"a native-only combination moves to Go", Options{Combine: CombineTrimmedMean}, EngineNative},
		{"local normalization moves to Go", Options{LocalNorm: true}, EngineNative},
		{
			"an explicit Siril engine is honoured even for a native algorithm (Validate rejects it)",
			Options{Engine: EngineSiril, Reject: RejectRCR}, EngineSiril,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, EngineFor(tt.in))
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		in      Options
		wantErr string
	}{
		{"the defaults are valid", DefaultLights(), ""},
		{"the comet default is valid", DefaultComet(), ""},
		{"a native algorithm on auto is valid", Options{Reject: RejectRCR}, ""},
		{"an unknown engine is rejected", Options{Engine: "gpu"}, "unknown stacking engine"},
		{"an unknown combination is rejected", Options{Combine: "geomean"}, "unknown combination method"},
		{"an unknown rejection is rejected", Options{Reject: "kappa"}, "unknown rejection algorithm"},
		{"an unknown normalization is rejected", Options{Norm: "additive"}, "unknown normalization"},
		{"an unknown weighting is rejected", Options{Weight: "snr"}, "unknown frame weighting"},
		{
			"rejection on a summing stack is rejected",
			Options{Combine: CombineSum, Reject: RejectWinsorized}, "takes no pixel rejection",
		},
		{
			"a native algorithm pinned to Siril is rejected",
			Options{Engine: EngineSiril, Reject: RejectRCR}, "does not implement",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Validate(tt.in)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestClamp_PinsFreeStandingNumbers(t *testing.T) {
	got := Clamp(Options{Low: -1, High: 99, TrimFrac: 0.9, Feather: 9000, LocalNormDegree: 7})
	assert.Equal(t, 0.0, got.Low)
	assert.Equal(t, SigmaMax, got.High)
	assert.InDelta(t, 0.45, got.TrimFrac, 1e-9)
	assert.Equal(t, 512, got.Feather)
	assert.Equal(t, 4, got.LocalNormDegree)
}

// TestCatalogue_IsSelfConsistent guards the table itself: every algorithm the UI can offer must be
// looked up by its own id, Siril-capable rows must carry the token that renders them, and a row with
// parameters must give both of them a usable range.
func TestCatalogue_IsSelfConsistent(t *testing.T) {
	for _, info := range Rejects() {
		t.Run("reject/"+string(info.ID), func(t *testing.T) {
			got, ok := RejectOf(info.ID)
			require.True(t, ok)
			assert.Equal(t, info.ID, got.ID)
			assert.NotEmpty(t, info.Engines, "an algorithm no engine implements cannot be offered")
			if info.SupportedBy(EngineSiril) {
				assert.NotEmpty(t, info.SirilToken, "a Siril-capable algorithm needs its grammar token")
			}
			if info.HasParams {
				for _, p := range []RejectParam{info.Low, info.High} {
					assert.Greater(t, p.Max, p.Min)
					assert.GreaterOrEqual(t, p.Default, p.Min)
					assert.LessOrEqual(t, p.Default, p.Max)
					assert.LessOrEqual(t, p.Max, SigmaMax, "the whitelist clamp must not cut a usable value")
				}
			}
		})
	}
	for _, info := range Combines() {
		t.Run("combine/"+string(info.ID), func(t *testing.T) {
			got, ok := CombineOf(info.ID)
			require.True(t, ok)
			assert.Equal(t, info.ID, got.ID)
			assert.NotEmpty(t, info.Engines)
			if info.SupportedBy(EngineSiril) {
				assert.NotEmpty(t, info.SirilToken)
			}
			if info.Rejects {
				assert.True(t, info.Normalizes, "a rejecting method always accepts normalization")
			}
		})
	}
}
