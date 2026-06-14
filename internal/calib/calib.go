// Package calib builds master calibration frames (bias/dark/flat/dark-flat) from grouped
// calibration sets and matches the most appropriate masters to each light set — the logic that
// automatically picks the right noise-reduction frames.
package calib

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// MasterType is the kind of a master calibration frame.
type MasterType string

const (
	MasterBias     MasterType = "BIAS"
	MasterDark     MasterType = "DARK"
	MasterFlat     MasterType = "FLAT"
	MasterDarkFlat MasterType = "DARKFLAT"
)

// Master describes a built (or library) master calibration frame.
type Master struct {
	Type       MasterType `json:"type"`
	Filter     string     `json:"filter,omitempty"`
	ExposureMs int64      `json:"exposure_ms"`
	Gain       int64      `json:"gain"`
	Offset     int64      `json:"offset"`
	TempMilliC int64      `json:"temp_milli_c"`
	HasTemp    bool       `json:"has_temp"`
	Bin        int        `json:"bin"`
	FrameCount int        `json:"frame_count"`
	Path       string     `json:"path"`
}

// tempTolC is the sensor-temperature tolerance (°C) when matching a dark to a light.
const tempTolC = 5.0

var masterByFrameType = map[inspect.FrameType]MasterType{
	inspect.Bias:     MasterBias,
	inspect.Dark:     MasterDark,
	inspect.Flat:     MasterFlat,
	inspect.DarkFlat: MasterDarkFlat,
}

// BuildMasters stacks every calibration set in inv into a master under mastersDir, using workDir
// for the per-set Siril sequences. Bias/dark-flats are built before flats (flats use them). A set
// that fails to stack is reported as a warning rather than aborting the run.
func BuildMasters(ctx context.Context, runner *siril.Runner, inv *inspect.Inventory,
	mastersDir, workDir string, onProgress func(siril.Progress)) ([]Master, []string, error) {
	if err := fsutil.EnsureDir(mastersDir); err != nil {
		return nil, nil, err
	}
	order := []inspect.FrameType{inspect.Bias, inspect.DarkFlat, inspect.Dark, inspect.Flat}
	var masters []Master
	var warnings []string
	for _, ft := range order {
		for _, set := range inv.SetsOfType(ft) {
			m, err := buildOne(ctx, runner, set, masters, mastersDir, workDir, onProgress)
			if err != nil {
				warnings = append(warnings, err.Error())
				continue
			}
			masters = append(masters, m)
		}
	}
	return masters, warnings, nil
}

func buildOne(ctx context.Context, runner *siril.Runner, set inspect.Set, built []Master,
	mastersDir, workDir string, onProgress func(siril.Progress)) (Master, error) {
	mt := masterByFrameType[set.Key.Type]
	name := masterName(mt, set.Key)
	outBase := filepath.Join(mastersDir, name)
	seqDir := filepath.Join(workDir, "cal_"+name)

	paths := framePaths(set.Frames)
	if _, err := fsutil.LinkFrames(seqDir, paths); err != nil {
		return Master{}, err
	}

	var script string
	if mt == MasterFlat {
		biasPath := flatBias(set.Key, built)
		script = siril.StackFlatScript("cal", outBase, biasPath)
	} else {
		script = siril.StackMasterScript("cal", outBase)
	}
	if _, err := runner.Run(ctx, seqDir, script, onProgress); err != nil {
		return Master{}, fmt.Errorf("stack master %s: %w", name, err)
	}

	rep := set.Frames[0]
	return Master{
		Type:       mt,
		Filter:     set.Key.Filter,
		ExposureMs: set.Key.ExposureMs,
		Gain:       set.Key.Gain,
		Offset:     set.Key.Offset,
		TempMilliC: rep.TempMilliC,
		HasTemp:    rep.HasTemp,
		Bin:        set.Key.Bin,
		FrameCount: set.Count,
		Path:       outBase + ".fits",
	}, nil
}

// flatBias finds a bias (or dark-flat) master matching a flat set's gain/offset/bin.
func flatBias(flat inspect.SetKey, built []Master) string {
	for _, m := range built {
		if m.Type == MasterDarkFlat && m.Gain == flat.Gain && m.Offset == flat.Offset && m.Bin == flat.Bin {
			return m.Path
		}
	}
	for _, m := range built {
		if m.Type == MasterBias && m.Gain == flat.Gain && m.Offset == flat.Offset && m.Bin == flat.Bin {
			return m.Path
		}
	}
	return ""
}

func framePaths(frames []*inspect.Frame) []string {
	out := make([]string, len(frames))
	for i, f := range frames {
		out[i] = f.Path
	}
	return out
}

func masterName(mt MasterType, k inspect.SetKey) string {
	name := "master_" + string(mt)
	if k.Filter != "" {
		name += "_" + k.Filter
	}
	if mt == MasterDark || mt == MasterDarkFlat || mt == MasterFlat {
		name += fmt.Sprintf("_%dms", k.ExposureMs)
	}
	name += fmt.Sprintf("_g%do%d_b%d", k.Gain, k.Offset, k.Bin)
	if mt != MasterBias {
		name += fmt.Sprintf("_%dC", k.TempBucket)
	}
	return name
}
