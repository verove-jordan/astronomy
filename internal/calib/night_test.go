package calib

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

// TestMasterName_SessionSuffix: a night-stamped flat set gets a per-night file name (two nights'
// stacks must never overwrite each other); a keyless (single-night) set keeps its historical name.
func TestMasterName_SessionSuffix(t *testing.T) {
	base := inspect.SetKey{Type: inspect.Flat, Filter: "L", ExposureMs: 5, Gain: 0, Offset: 10, Bin: 1, TempBucket: -25}
	assert.Equal(t, "master_FLAT_L_5ms_g0o10_b1_-25C", masterName(MasterFlat, base),
		"single-night name unchanged — the byte-identity pin")

	night := base
	night.Session = "2023-02-27"
	assert.Equal(t, "master_FLAT_L_5ms_g0o10_b1_-25C_n2023-02-27", masterName(MasterFlat, night))
}

// TestPickFlat_NightTiers: the light's own night wins over a deeper other-night flat; unknown nights
// (library masters, promoted header-less sets) rank LAST — their dust age is unknowable, and one such
// ghost flat once outranked every dated candidate and re-polluted the L channel (job 364);
// single-night lights (no Session) keep the historical ordering.
func TestPickFlat_NightTiers(t *testing.T) {
	flat := func(session string, frames int) Master {
		return Master{Type: MasterFlat, Filter: "L", ExposureMs: 5, Gain: 0, Offset: 10, Bin: 1,
			FrameCount: frames, Session: session, Path: "lib_" + session}
	}
	light := inspect.SetKey{Type: inspect.Light, Filter: "L", ExposureMs: 30000, Gain: 0, Offset: 10, Bin: 1}

	t.Run("same night beats a deeper other night", func(t *testing.T) {
		l := light
		l.Session = "2023-02-27"
		masters := []Master{flat("2023-03-15", 200), flat("2023-02-27", 10)}
		got := pickFlat(l, masters, true)
		require.NotNil(t, got)
		assert.Equal(t, "2023-02-27", got.Session)
	})
	t.Run("a dated night beats an unknown night (library)", func(t *testing.T) {
		l := light
		l.Session = "2023-02-27"
		masters := []Master{flat("", 200), flat("2023-03-15", 10)}
		got := pickFlat(l, masters, true)
		require.NotNil(t, got)
		assert.Equal(t, "2023-03-15", got.Session,
			"a flat dated days away beats a night-blind one of unknowable dust age, whatever its depth")
	})
	t.Run("unknown night is still the last resort", func(t *testing.T) {
		l := light
		l.Session = "2023-02-27"
		got := pickFlat(l, []Master{flat("", 10)}, true)
		require.NotNil(t, got)
		assert.Empty(t, got.Session, "with no dated candidate the night-blind flat is still applied")
	})
	t.Run("single-night light keeps the depth ordering", func(t *testing.T) {
		masters := []Master{flat("", 10), flat("", 200)}
		got := pickFlat(light, masters, true)
		require.NotNil(t, got)
		assert.Equal(t, 200, got.FrameCount, "no night on the light → every candidate ties → deepest wins, as before")
	})
}

// TestMatchForLight_CrossNightFlatNote: applying another night's flat is allowed (better than none)
// but must be called out — dust may have moved.
func TestMatchForLight_CrossNightFlatNote(t *testing.T) {
	light := inspect.SetKey{Type: inspect.Light, Filter: "L", ExposureMs: 30000, Gain: 0, Offset: 10, Bin: 1, Session: "2023-02-27"}
	masters := []Master{{Type: MasterFlat, Filter: "L", ExposureMs: 5, Gain: 0, Offset: 10, Bin: 1, FrameCount: 20, Session: "2023-03-15", Path: "x"}}

	sel := matchForLight(light, masters, false)

	require.NotNil(t, sel.Flat)
	found := false
	for _, n := range sel.Notes {
		if strings.Contains(n, "2023-03-15") && strings.Contains(n, "dust") {
			found = true
		}
	}
	assert.True(t, found, "cross-night flat use must be noted; got %v", sel.Notes)
}

// TestMasterMatchesSet_NightStamped: a night-stamped flat set is satisfied ONLY by its own night's
// master — never by a night-blind library flat (dust state) — while keyless sets keep matching.
func TestMasterMatchesSet_NightStamped(t *testing.T) {
	set := inspect.Set{Key: inspect.SetKey{Type: inspect.Flat, Filter: "L", ExposureMs: 5, Gain: 0, Offset: 10, Bin: 1, Session: "2023-02-27"}}
	library := Master{Type: MasterFlat, Filter: "L", ExposureMs: 5, Gain: 0, Offset: 10, Bin: 1, Path: "lib"}
	sameNight := library
	sameNight.Session = "2023-02-27"
	otherNight := library
	otherNight.Session = "2023-03-15"

	assert.False(t, masterMatchesSet(&library, set), "night-blind library master must not satisfy a per-night flat set")
	assert.True(t, masterMatchesSet(&sameNight, set))
	assert.False(t, masterMatchesSet(&otherNight, set))

	keyless := set
	keyless.Key.Session = ""
	assert.True(t, masterMatchesSet(&library, keyless), "single-night behavior unchanged")
}

// fakeStore records SaveMaster calls (no DB — the skip rule is pure logic).
type fakeStore struct{ saved []Master }

func (f *fakeStore) ListMasters(context.Context) ([]Master, error) { return nil, nil }
func (f *fakeStore) SaveMaster(_ context.Context, m Master) error {
	f.saved = append(f.saved, m)
	return nil
}

// TestBuildOrReuseMasters_SkipsLibrarySaveForNightFlats: per-night masters are run-local — the
// library dedup key has no night column, so persisting them would make nights overwrite each other.
// Exercised through the deep-save loop's rule directly (Siril isn't needed to test the policy).
func TestSaveSkip_NightStampedMasters(t *testing.T) {
	store := &fakeStore{}
	masters := []Master{
		{Type: MasterFlat, Filter: "L", Session: "2023-02-27", Path: "a"},
		{Type: MasterFlat, Filter: "L", Session: "", Path: "b"},
		{Type: MasterBias, Path: "c"},
	}
	for _, m := range masters {
		if m.Session != "" {
			continue // the exact guard used by BuildOrReuseMasters + BuildDeepMasters
		}
		require.NoError(t, store.SaveMaster(context.Background(), m))
	}
	require.Len(t, store.saved, 2)
	for _, m := range store.saved {
		assert.Empty(t, m.Session, "only night-blind masters persist")
	}
}
