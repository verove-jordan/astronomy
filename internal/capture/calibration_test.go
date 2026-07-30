package capture

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lrgbLights() LightSettings {
	return LightSettings{
		ExposureUs: 300_000_000, Gain: 139, Offset: 21, Bin: 1,
		Filters: []string{"L", "R", "G", "B"}, TempMilliC: -10_000, HasTemp: true,
	}
}

// The whole point of the wizard: darks must match the lights exactly. A dark at the wrong exposure
// or gain removes a thermal pattern the lights do not have, which is worse than no calibration.
func TestBuildCalibrationPlan_DarksMatchTheLights(t *testing.T) {
	l := lrgbLights()
	plan, err := BuildCalibrationPlan(CalibrationRequest{Lights: l, Kinds: []CalibrationKind{CalibDark}})
	require.NoError(t, err)
	require.Len(t, plan.Sequence.Steps, 1)

	s := plan.Sequence.Steps[0]
	assert.Equal(t, "dark", s.Type)
	assert.Equal(t, l.ExposureUs, s.ExposureUs, "a dark at a different exposure is useless")
	assert.Equal(t, l.Gain, s.Gain)
	assert.Equal(t, l.Offset, s.Offset)
	assert.Equal(t, l.Bin, s.Bin)
	assert.Equal(t, recommendedDarks, s.Count)
	assert.Empty(t, s.Filter, "a dark is taken with the shutter closed; the filter is irrelevant")
}

// Flats live in the optical train, so every filter needs its own — the mistake that leaves dust
// motes in three channels out of four.
func TestBuildCalibrationPlan_OneFlatSetPerFilter(t *testing.T) {
	plan, err := BuildCalibrationPlan(CalibrationRequest{
		Lights: lrgbLights(), Kinds: []CalibrationKind{CalibFlat}, FlatExposureUs: 2_500_000,
	})
	require.NoError(t, err)
	require.Len(t, plan.Sequence.Steps, 4)

	got := map[string]bool{}
	for _, s := range plan.Sequence.Steps {
		assert.Equal(t, "flat", s.Type)
		assert.Equal(t, int64(2_500_000), s.ExposureUs)
		assert.Equal(t, int64(139), s.Gain, "flats must share the lights' gain")
		got[s.Filter] = true
	}
	assert.Equal(t, map[string]bool{"L": true, "R": true, "G": true, "B": true}, got)
}

// Flat exposure depends on the light panel and cannot be guessed, so asking for flats without
// measuring first must be refused rather than filled in with a plausible number.
func TestBuildCalibrationPlan_RefusesFlatsWithoutAMeasuredExposure(t *testing.T) {
	_, err := BuildCalibrationPlan(CalibrationRequest{
		Lights: lrgbLights(), Kinds: []CalibrationKind{CalibFlat},
	})
	assert.ErrorContains(t, err, "measure the flat exposure first")
}

// Bias is the shortest exposure the sensor allows, at the lights' gain and offset.
func TestBuildCalibrationPlan_Bias(t *testing.T) {
	plan, err := BuildCalibrationPlan(CalibrationRequest{
		Lights: lrgbLights(), Kinds: []CalibrationKind{CalibBias},
	})
	require.NoError(t, err)
	require.Len(t, plan.Sequence.Steps, 1)

	s := plan.Sequence.Steps[0]
	assert.Equal(t, "bias", s.Type)
	assert.Equal(t, int64(biasExposureUs), s.ExposureUs)
	assert.Equal(t, int64(139), s.Gain)
	assert.Equal(t, int64(21), s.Offset)
	assert.Greater(t, s.Count, recommendedDarks, "bias frames are free, so take plenty")
}

// The order is chosen so the user covers the telescope once: everything shot capped comes before
// the flats, which need the cap off and a panel on.
func TestBuildCalibrationPlan_OrdersCappedFramesBeforeFlats(t *testing.T) {
	plan, err := BuildCalibrationPlan(CalibrationRequest{
		Lights:         lrgbLights(),
		Kinds:          []CalibrationKind{CalibFlat, CalibDark, CalibBias}, // deliberately out of order
		FlatExposureUs: 2_000_000,
	})
	require.NoError(t, err)

	var order []string
	for _, s := range plan.Sequence.Steps {
		if len(order) == 0 || order[len(order)-1] != s.Type {
			order = append(order, s.Type)
		}
	}
	assert.Equal(t, []string{"bias", "dark", "flat"}, order,
		"capped frames first, so the telescope is uncovered exactly once")
}

// Dark flats match the FLAT exposure, not the lights'. Getting this backwards is a classic error
// that leaves the flats' own pedestal in the master.
func TestBuildCalibrationPlan_DarkFlatsMatchTheFlats(t *testing.T) {
	plan, err := BuildCalibrationPlan(CalibrationRequest{
		Lights: lrgbLights(), Kinds: []CalibrationKind{CalibDarkFlat}, FlatExposureUs: 2_500_000,
	})
	require.NoError(t, err)
	require.Len(t, plan.Sequence.Steps, 1)
	assert.Equal(t, int64(2_500_000), plan.Sequence.Steps[0].ExposureUs,
		"a dark flat matches the flat, never the light")
}

func TestBuildCalibrationPlan_CustomCounts(t *testing.T) {
	plan, err := BuildCalibrationPlan(CalibrationRequest{
		Lights: lrgbLights(), Kinds: []CalibrationKind{CalibDark, CalibBias},
		DarkCount: 7, BiasCount: 11,
	})
	require.NoError(t, err)
	byType := map[string]int{}
	for _, s := range plan.Sequence.Steps {
		byType[s.Type] = s.Count
	}
	assert.Equal(t, 7, byType["dark"])
	assert.Equal(t, 11, byType["bias"])
}

// The estimate has to include readout, or a 30×300 s dark set looks like 2.5 hours when it is
// nearer 2 hours 40 — and the user plans their night around it.
func TestBuildCalibrationPlan_TimeEstimateIncludesReadout(t *testing.T) {
	plan, err := BuildCalibrationPlan(CalibrationRequest{
		Lights: LightSettings{ExposureUs: 10_000_000, Gain: 100, Bin: 1},
		Kinds:  []CalibrationKind{CalibDark}, DarkCount: 10,
	})
	require.NoError(t, err)
	assert.Equal(t, 10, plan.TotalFrames)
	assert.Greater(t, plan.EstimatedSec, 100.0, "10 × 10 s of exposure alone")
	assert.Less(t, plan.EstimatedSec, 130.0, "plus readout, not double")
}

// The warnings name the failures that are silent otherwise.
func TestBuildCalibrationPlan_WarnsAboutUnmatchableDarks(t *testing.T) {
	plan, err := BuildCalibrationPlan(CalibrationRequest{
		Lights: LightSettings{ExposureUs: 60_000_000, Gain: 100, Bin: 1}, // HasTemp false
		Kinds:  []CalibrationKind{CalibDark},
	})
	require.NoError(t, err)
	require.NotEmpty(t, plan.Warnings)
	assert.Contains(t, plan.Warnings[0], "temperature")
}

func TestBuildCalibrationPlan_WarnsWhenFlatsHaveNoFilters(t *testing.T) {
	plan, err := BuildCalibrationPlan(CalibrationRequest{
		Lights: LightSettings{Gain: 100, Bin: 1}, Kinds: []CalibrationKind{CalibFlat},
		FlatExposureUs: 1_000_000,
	})
	require.NoError(t, err)
	assert.Len(t, plan.Sequence.Steps, 1, "no wheel still needs one unfiltered flat set")
	require.NotEmpty(t, plan.Warnings)
	assert.Contains(t, plan.Warnings[0], "filter")
}

func TestBuildCalibrationPlan_RejectsEmptyRequests(t *testing.T) {
	_, err := BuildCalibrationPlan(CalibrationRequest{Lights: lrgbLights()})
	assert.ErrorContains(t, err, "at least one")

	_, err = BuildCalibrationPlan(CalibrationRequest{
		Lights: LightSettings{Gain: 100}, Kinds: []CalibrationKind{CalibDark},
	})
	assert.ErrorContains(t, err, "exposure", "darks with no light exposure to match must be refused")
}

// Duplicate filters in the light settings must not produce duplicate flat sets, and the plan is
// ordered the way the wheel is (L,R,G,B,Ha,OIII,SII) — not alphabetically, which scattered the
// narrowband rows through the broadband ones and made the plan awkward to shoot against.
func TestSortedFilters_DeduplicatesAndOrders(t *testing.T) {
	assert.Equal(t, []string{"L", "R", "G", "B"},
		sortedFilters([]string{"L", "R", "L", "G", "B", "R"}))
	assert.Equal(t, []string{"L", "R", "G", "B", "Ha", "OIII", "SII"},
		sortedFilters([]string{"SII", "B", "OIII", "R", "Ha", "L", "G"}))
	assert.Equal(t, []string{"L", "Ha", "Baader", "custom"},
		sortedFilters([]string{"custom", "Ha", "Baader", "L"}),
		"unknown filters sort after the canonical set, alphabetically among themselves")
	assert.Equal(t, []string{""}, sortedFilters(nil))
	assert.Equal(t, []string{""}, sortedFilters([]string{"", ""}))
}

func TestFormatExposure(t *testing.T) {
	assert.Equal(t, "300 s", formatExposure(300_000_000))
	assert.Equal(t, "2.5 s", formatExposure(2_500_000))
	assert.Equal(t, "500 ms", formatExposure(500_000))
	assert.Equal(t, "32 µs", formatExposure(32))
}
