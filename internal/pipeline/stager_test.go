package pipeline

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/verove-jordan/astronomy/internal/inspect"
)

func TestCalibStagePaths(t *testing.T) {
	inv := &inspect.Inventory{Sets: []inspect.Set{
		{Key: inspect.SetKey{Type: inspect.Light}, Frames: []*inspect.Frame{{Path: "L1"}, {Path: "L2"}}},
		{Key: inspect.SetKey{Type: inspect.Dark}, Frames: []*inspect.Frame{{Path: "D1"}, {Path: "D2"}}},
		{Key: inspect.SetKey{Type: inspect.Flat}, Frames: []*inspect.Frame{{Path: "F1"}}},
		{Key: inspect.SetKey{Type: inspect.Bias}, Frames: []*inspect.Frame{{Path: "B1"}}},
		{Key: inspect.SetKey{Type: inspect.DarkFlat}, Frames: []*inspect.Frame{{Path: "DF1"}}},
	}}
	// Only calibration frames are staged; lights are staged per-channel later.
	assert.ElementsMatch(t, []string{"D1", "D2", "F1", "B1", "DF1"}, calibStagePaths(inv))
}

func TestCurrentGroupPaths(t *testing.T) {
	plan := &ReusePlan{byFilter: map[string][]lightGroup{
		"L": {
			{Current: true, Frames: []*inspect.Frame{{Path: "cur1"}, {Path: "cur2"}}}, // this session
			{Current: false, Frames: []*inspect.Frame{{Path: "prior1"}}},              // a prior session (freed to S3)
		},
	}}
	// Only the CURRENT session's frames are staged; prior-session reuse frames keep their local-or-skip
	// semantics (they are catalogued, freed, and dropped).
	assert.Equal(t, []string{"cur1", "cur2"}, currentGroupPaths(plan, "L"))
	assert.Nil(t, currentGroupPaths(plan, "R")) // absent filter
}

func TestStagePullError_Unwrap(t *testing.T) {
	inner := errors.New("network down")
	e := &StagePullError{RunID: "r1", OutDir: "/o/r1", Err: inner}
	assert.ErrorIs(t, e, inner)
	var sp *StagePullError
	assert.True(t, errors.As(error(e), &sp))
	assert.Equal(t, "r1", sp.RunID)
}
