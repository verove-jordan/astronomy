package pipeline

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/mode"
)

func TestApplyClusterProfile(t *testing.T) {
	// A stock deep-sky preset gets the full cluster profile: low saturation, capped headroom, chroma
	// blur, star-core desaturation, the nebula-core roll-off disabled, and the gentle luminance curve.
	stock := mode.For(mode.Deepsky)
	got := applyClusterProfile(stock)
	assert.InDelta(t, clusterSaturation, got.Saturation, 1e-9)
	assert.InDelta(t, clusterStretchHeadroom, got.StretchHeadroom, 1e-9)
	assert.InDelta(t, clusterChromaBlur, got.ChromaBlur, 1e-9)
	assert.InDelta(t, clusterStarDesat, got.StarDesat, 1e-9)
	assert.InDelta(t, clusterLumOpacity, got.LumOpacity, 1e-9)
	assert.Zero(t, got.CoreHighlightKnee)
	assert.Zero(t, got.CoreHighlightCeil)
	assert.Equal(t, clusterLumCurve, got.LumCurve)

	// A user/agent override of a tunable knob wins (patch-preserving): a run that set saturation 0.20 or
	// star_desat 0.9 keeps those, while the knobs still at the stock default get the cluster value.
	user := mode.For(mode.Deepsky)
	user.Saturation = 0.20
	user.StarDesat = 0.9
	gotU := applyClusterProfile(user)
	assert.InDelta(t, 0.20, gotU.Saturation, 1e-9, "explicit saturation override kept")
	assert.InDelta(t, 0.9, gotU.StarDesat, 1e-9, "explicit star_desat override kept")
	assert.InDelta(t, clusterStretchHeadroom, gotU.StretchHeadroom, 1e-9, "untouched knob still gets the cluster value")

	// Idempotent: applying the profile to an already-cluster preset changes nothing (so re-application in
	// finishAligned/rerun over a persisted cluster preset is safe).
	assert.Equal(t, got, applyClusterProfile(got))

	// Nebula is also a cluster-eligible mode (starClusterTarget can route a nebula-mode run).
	assert.InDelta(t, clusterSaturation, applyClusterProfile(mode.For(mode.Nebula)).Saturation, 1e-9)
}
