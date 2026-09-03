package nexstar_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/device/nexstar"
	"github.com/verove-jordan/astronomy/internal/device/sim"
)

// The audit against the SIMULATOR, which is the reason it reaches a mount through capabilities
// rather than through one concrete driver.
//
// A simulated mount has a periodic-error table and an autoguide rate — those are hardware behaviour
// worth simulating — but it has no hand controller, so it stores no site and no clock, and it cannot
// be asked about its declination motor separately. Every one of those gaps must come back as a gap.
// Getting this right is what makes the panel developable indoors instead of only on a clear night.

func simMount(t *testing.T) *sim.Mount {
	t.Helper()
	m := sim.NewMount(sim.NewWorld(sim.Config{}))
	require.NoError(t, m.Connect(context.Background()))
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestAudit_ReportsCapabilityGapsRatherThanFailing(t *testing.T) {
	m := simMount(t)

	r, err := nexstar.Audit(context.Background(), m)
	require.NoError(t, err)

	// What the simulator does model.
	assert.True(t, r.Drive.Read)
	require.True(t, r.PEC.Supported, "the simulator has a worm table")
	require.True(t, r.PEC.Read)
	assert.Positive(t, r.PEC.Bins)
	require.True(t, r.Guide.Read)

	// What it does not, reported as absence rather than as an error or a silent zero.
	assert.False(t, r.Site.Read)
	assert.Contains(t, r.Site.Err, "no stored site")
	assert.False(t, r.Clock.Read)
	assert.Contains(t, r.Clock.Err, "no stored clock")
	assert.False(t, r.Guide.BothAxes, "the simulator exposes one autoguide rate, not one per motor")

	notes := strings.Join(r.Notes, "\n")
	assert.Contains(t, notes, "declination one")

	// And the rendering must survive every one of those holes.
	assert.NotEmpty(t, r.String())
}

func TestRestore_OnADriverThatCannotDoEverything(t *testing.T) {
	m := simMount(t)
	require.NoError(t, m.PECWriteCurve(context.Background(), fill(88, 9)))

	res, err := nexstar.Restore(context.Background(), m, nexstar.RestoreOptions{
		PEC: true, GuideRate: true, Site: true, Clock: true,
		BackupDir: t.TempDir(),
	})
	require.NoError(t, err)

	got := map[string]nexstar.RestoreAction{}
	for _, a := range res.Actions {
		got[a.Item] = a
	}
	assert.True(t, got["pec"].Applied, "%s", got["pec"].Err)
	assert.True(t, got["guide_rate"].Applied, "%s", got["guide_rate"].Err)
	assert.False(t, got["site"].Applied)
	assert.Contains(t, got["site"].Err, "no stored site")
	assert.False(t, got["clock"].Applied)

	// The one that could be done, was done — a capability gap elsewhere must not abandon the rest.
	assert.True(t, res.After.PEC.AllZero)
}

func fill(n int, v int8) []int8 {
	out := make([]int8, n)
	for i := range out {
		out[i] = v
	}
	return out
}
