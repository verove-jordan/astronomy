package inspect

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNightKeyIn pins the local-noon bucketing on a FIXED zone — never the machine's TZ.
func TestNightKeyIn(t *testing.T) {
	cet := time.FixedZone("CET", 3600)
	at := func(s string) int64 {
		tt, err := time.ParseInLocation("2006-01-02T15:04:05", s, cet)
		require.NoError(t, err)
		return tt.UnixMilli()
	}
	cases := []struct {
		name string
		ms   int64
		want string
	}{
		{"evening belongs to its own date", at("2023-02-27T22:55:00"), "2023-02-27"},
		{"after midnight joins the evening's night", at("2023-02-28T03:40:00"), "2023-02-27"},
		{"just before noon still the previous night", at("2023-02-28T11:59:59"), "2023-02-27"},
		{"noon starts the next night", at("2023-02-28T12:00:00"), "2023-02-28"},
		{"no DATE-OBS", 0, ""},
		{"negative guard", -5, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, NightKeyIn(cet, tc.ms))
		})
	}
}

// nightFrame builds an in-memory frame for the grouping tests (buildSets is pure — no files needed).
func nightFrame(typ FrameType, filter, night string, expMs, gain int64) *Frame {
	fr := &Frame{Type: typ, Filter: filter, Session: night, ExposureMs: expMs, Gain: gain, Offset: 10, BinX: 1, BinY: 1}
	if night != "" {
		fr.DateObsMs = 1 // any positive stamp: Session is authoritative for grouping
	}
	return fr
}

func setKeys(sets []Set) []SetKey {
	keys := make([]SetKey, len(sets))
	for i, s := range sets {
		keys[i] = s.Key
	}
	return keys
}

// TestBuildSets_SingleNight_KeysUnchanged: one night (however dated) must produce EXACTLY the
// pre-sessionization keys — every Session zero, one set per config. The byte-identity pin.
func TestBuildSets_SingleNight_KeysUnchanged(t *testing.T) {
	frames := []*Frame{
		nightFrame(Light, "L", "2023-02-27", 30000, 250),
		nightFrame(Light, "L", "2023-02-27", 30000, 250),
		nightFrame(Flat, "L", "2023-02-27", 5, 0),
		nightFrame(Dark, "", "2023-02-27", 30000, 250),
		nightFrame(Bias, "", "2023-02-27", 0, 250),
	}
	sets := buildSets(frames)
	require.Len(t, sets, 4)
	for _, s := range sets {
		assert.Empty(t, s.Key.Session, "%s set must carry no night on a single-night scan", s.Key.Type)
	}
}

// TestBuildSets_MultiNight_SplitsLightsAndFlatsOnly: two nights of the SAME config split the light
// and flat sets per night; darks/bias stay pooled (night-agnostic thermal signatures).
func TestBuildSets_MultiNight_SplitsLightsAndFlatsOnly(t *testing.T) {
	frames := []*Frame{
		nightFrame(Light, "L", "2023-02-27", 30000, 250),
		nightFrame(Light, "L", "2023-03-15", 30000, 250),
		nightFrame(Flat, "L", "2023-02-27", 5, 0),
		nightFrame(Flat, "L", "2023-03-15", 5, 0),
		nightFrame(Dark, "", "2023-02-27", 30000, 250),
		nightFrame(Dark, "", "2023-03-15", 30000, 250),
		nightFrame(Bias, "", "2023-02-27", 0, 250),
		nightFrame(Bias, "", "2023-03-15", 0, 250),
	}
	sets := buildSets(frames)
	byType := map[FrameType][]SetKey{}
	for _, k := range setKeys(sets) {
		byType[k.Type] = append(byType[k.Type], k)
	}
	require.Len(t, byType[Light], 2, "same-config lights split per night")
	assert.Equal(t, "2023-02-27", byType[Light][0].Session)
	assert.Equal(t, "2023-03-15", byType[Light][1].Session)
	require.Len(t, byType[Flat], 2, "flats split per night (dust moves between nights)")
	require.Len(t, byType[Dark], 1, "darks pool across nights")
	assert.Empty(t, byType[Dark][0].Session)
	require.Len(t, byType[Bias], 1, "bias pools across nights")
	assert.Empty(t, byType[Bias][0].Session)
}

// TestBuildSets_OneNightPlusUndated_NoSplit: a real night plus headerless strays must NOT activate
// the split — the common "a few files lack DATE-OBS" folder behaves exactly like today.
func TestBuildSets_OneNightPlusUndated_NoSplit(t *testing.T) {
	frames := []*Frame{
		nightFrame(Light, "L", "2023-02-27", 30000, 250),
		nightFrame(Light, "L", "", 30000, 250), // undated stray
	}
	sets := buildSets(frames)
	require.Len(t, sets, 1, "one config, one set — undated strays join it")
	assert.Empty(t, sets[0].Key.Session)
}

// TestBuildSets_TwoNightsPlusUndated_OwnBucket: with a genuine multi-night split, undated frames
// form their own bucket instead of contaminating a night.
func TestBuildSets_TwoNightsPlusUndated_OwnBucket(t *testing.T) {
	frames := []*Frame{
		nightFrame(Light, "L", "2023-02-27", 30000, 250),
		nightFrame(Light, "L", "2023-03-15", 30000, 250),
		nightFrame(Light, "L", "", 30000, 250),
	}
	sets := buildSets(frames)
	require.Len(t, sets, 3)
	nights := []string{sets[0].Key.Session, sets[1].Key.Session, sets[2].Key.Session}
	assert.Equal(t, []string{"", "2023-02-27", "2023-03-15"}, nights, "undated bucket + one set per night")

	inv := &Inventory{Frames: frames}
	warnUndatedSplit(inv)
	require.Len(t, inv.Warnings, 1)
	assert.Contains(t, inv.Warnings[0], "no DATE-OBS")
}

func TestSessionSummary(t *testing.T) {
	t.Run("all undated → nil (payload unchanged)", func(t *testing.T) {
		frames := []*Frame{nightFrame(Light, "L", "", 30000, 250)}
		assert.Nil(t, sessionSummary(frames))
	})
	t.Run("two nights + undated, sorted with undated last", func(t *testing.T) {
		n1a := nightFrame(Light, "L", "2023-02-27", 30000, 250)
		n1a.DateObsMs = 1000
		n1b := nightFrame(Light, "R", "2023-02-27", 30000, 250)
		n1b.DateObsMs = 5000
		n1f := nightFrame(Flat, "L", "2023-02-27", 5, 0)
		n1f.DateObsMs = 3000 // the night's flats sit inside its time window too
		n2 := nightFrame(Light, "L", "2023-03-15", 60000, 400)
		n2.DateObsMs = 9000
		stray := nightFrame(Light, "L", "", 30000, 250)

		got := sessionSummary([]*Frame{n1a, n1b, n1f, n2, stray})
		require.Len(t, got, 3)
		assert.Equal(t, "2023-02-27", got[0].Key)
		assert.Equal(t, int64(1000), got[0].StartMs)
		assert.Equal(t, int64(5000), got[0].EndMs)
		assert.Equal(t, 2, got[0].Counts[Light])
		assert.Equal(t, 1, got[0].Counts[Flat])
		require.Len(t, got[0].Configs, 2, "one config per (filter,exposure,gain)")
		assert.Equal(t, "L", got[0].Configs[0].Filter)
		assert.Equal(t, 1, got[0].Configs[0].Count)

		assert.Equal(t, "2023-03-15", got[1].Key)
		assert.Equal(t, int64(60000), got[1].Configs[0].ExposureMs)

		assert.Equal(t, "", got[2].Key, "undated bucket sorts last")
		assert.Equal(t, 1, got[2].Counts[Light])
	})
}
