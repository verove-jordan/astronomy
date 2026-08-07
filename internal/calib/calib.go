// Package calib builds master calibration frames (bias/dark/flat/dark-flat) from grouped
// calibration sets and matches the most appropriate masters to each light set — the logic that
// automatically picks the right noise-reduction frames.
package calib

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/stackalg"
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
	// Session is the capture-night key of a per-night master ("" = night-blind). Only FLATS of a
	// multi-night scan carry one (dust/orientation state is per-night); such masters are run-local —
	// never saved to the library (master_frames has no night column) and never satisfied BY a
	// night-blind library master (see masterMatchesSet). In-memory + run.json only.
	Session string `json:"session,omitempty"`
}

// tempTolC is the sensor-temperature tolerance (°C) when matching a dark to a light.
const tempTolC = 5.0

var masterByFrameType = map[inspect.FrameType]MasterType{
	inspect.Bias:     MasterBias,
	inspect.Dark:     MasterDark,
	inspect.Flat:     MasterFlat,
	inspect.DarkFlat: MasterDarkFlat,
}

// MasterStackOptions picks a frame type's stacking recipe out of the run's MasterOptions. Bias and
// dark (and a flat's own dark) stack UN-normalized — their pedestal IS the signal — while a flat
// stacks multiplicatively, because only its relative shape matters.
func MasterStackOptions(mt MasterType, o stackalg.MasterOptions) stackalg.Options {
	switch mt {
	case MasterFlat:
		return o.Flat
	case MasterBias:
		return o.Bias
	case MasterDarkFlat:
		return o.DarkFlat
	default:
		return o.Dark
	}
}

// BuildMasters stacks every calibration set in inv into a master under mastersDir, using workDir
// for the per-set Siril sequences. Bias/dark-flats are built before flats (flats use them). A set
// that fails to stack is reported as a warning rather than aborting the run.
func BuildMasters(ctx context.Context, runner *siril.Runner, inv *inspect.Inventory,
	mastersDir, workDir string, stacks stackalg.MasterOptions,
	onProgress func(siril.Progress)) ([]Master, []string, error) {
	if err := fsutil.EnsureDir(mastersDir); err != nil {
		return nil, nil, err
	}
	order := []inspect.FrameType{inspect.Bias, inspect.DarkFlat, inspect.Dark, inspect.Flat}
	var masters []Master
	var warnings []string
	for _, ft := range order {
		for _, set := range inv.SetsOfType(ft) {
			m, qc, err := buildOne(ctx, runner, set, masters, mastersDir, workDir, stacks, onProgress)
			warnings = append(warnings, qc...)
			if err != nil {
				warnings = append(warnings, err.Error())
				continue
			}
			masters = append(masters, m)
		}
	}
	return masters, warnings, nil
}

// buildOne stacks one calibration set into a master. For a master FLAT it also runs optical-defect +
// quality analysis (dust donuts, saturation, vignetting), returning any QC warnings; other master
// types return no warnings.
func buildOne(ctx context.Context, runner *siril.Runner, set inspect.Set, built []Master,
	mastersDir, workDir string, stacks stackalg.MasterOptions,
	onProgress func(siril.Progress)) (Master, []string, error) {
	mt := masterByFrameType[set.Key.Type]
	name := masterName(mt, set.Key, MasterStackOptions(mt, stacks))
	outBase := filepath.Join(mastersDir, name)
	seqDir := filepath.Join(workDir, "cal_"+name)

	paths := framePaths(set.Frames)
	if _, err := fsutil.LinkFrames(seqDir, paths); err != nil {
		return Master{}, nil, err
	}

	stackNote, err := stackMasterSet(ctx, runner, set.Key, built, seqDir, outBase, paths, stacks, onProgress)
	if err != nil {
		return Master{}, nil, fmt.Errorf("stack master %s: %w", name, err)
	}

	var qcWarn []string
	if stackNote != "" {
		qcWarn = append(qcWarn, stackNote)
	}
	if mt == MasterFlat {
		qcWarn = append(qcWarn, analyzeFlatMaster(outBase+".fits", paths)...)
	}
	if mt == MasterDark {
		if note := buildDefectList(outBase+".fits", paths); note != "" {
			qcWarn = append(qcWarn, note)
		}
	}
	_ = os.RemoveAll(seqDir) // master is saved to mastersDir; drop the per-set scratch

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
		Session:    set.Key.Session,
	}, qcWarn, nil
}

// stackMasterSet runs one calibration set's stack script. A FLAT whose own calibration step
// failed (an unusable/mismatched bias) is retried UNCALIBRATED with a warning note: the bias
// pedestal is a tiny fraction of a flat's illumination level, while losing the flat costs the
// whole night's vignetting/dust correction (a 2020 flat set died this way in task #312 and its
// lights went un-flat-fielded). The retry runs on fresh links in a sibling dir — the first
// attempt's convert products would otherwise poison a re-conversion in place.
func stackMasterSet(ctx context.Context, runner *siril.Runner, key inspect.SetKey, built []Master,
	seqDir, outBase string, paths []string, stacks stackalg.MasterOptions,
	onProgress func(siril.Progress)) (string, error) {
	// A single-frame pool (an S3-freed set's last frame on disk, a vendor-made master file) cannot
	// be stacked — Siril has no one-image sequences — and used to fail the whole category ("No
	// sequence `cal' found"), silently costing the night its flat. Convert and promote the lone
	// frame instead: the task-#352 trade, applied to calibration masters.
	if len(paths) == 1 {
		if err := promoteLoneCalFrame(ctx, runner, seqDir, outBase, onProgress); err != nil {
			return "", err
		}
		return fmt.Sprintf("master %s: single-frame pool — the lone frame was promoted unstacked (no outlier rejection%s)",
			filepath.Base(outBase), loneFlatSuffix(key)), nil
	}
	mt := masterByFrameType[key.Type]
	opts := MasterStackOptions(mt, stacks)
	if mt != MasterFlat {
		_, err := runner.Run(ctx, seqDir, siril.StackMasterScript("cal", outBase, len(paths), opts), onProgress)
		return "", err
	}
	biasPath := flatBias(key, built)
	_, err := runner.Run(ctx, seqDir, siril.StackFlatScript("cal", outBase, biasPath, len(paths), opts), onProgress)
	if err == nil || biasPath == "" {
		return "", err
	}
	retryDir := seqDir + "_uncal"
	defer func() { _ = os.RemoveAll(retryDir) }()
	if _, lerr := fsutil.LinkFrames(retryDir, paths); lerr != nil {
		return "", err // the retry could not even start — report the original failure
	}
	if _, rerr := runner.Run(ctx, retryDir, siril.StackFlatScript("cal", outBase, "", len(paths), opts), onProgress); rerr != nil {
		return "", err // the retry changed nothing — report the original failure
	}
	return fmt.Sprintf("flat %s: stacked WITHOUT its bias — flat calibration failed (%v)",
		filepath.Base(outBase), err), nil
}

// promoteLoneCalFrame converts a single-frame calibration pool and publishes the lone converted
// frame as the master.
func promoteLoneCalFrame(ctx context.Context, runner *siril.Runner, seqDir, outBase string,
	onProgress func(siril.Progress)) error {
	if _, err := runner.Run(ctx, seqDir, siril.ConvertScript("cal"), onProgress); err != nil {
		return err
	}
	return fsutil.CopyFile(filepath.Join(seqDir, "cal_00001.fits"), outBase+".fits")
}

// loneFlatSuffix adds the flat-specific caveat to the single-frame promotion note.
func loneFlatSuffix(key inspect.SetKey) string {
	if masterByFrameType[key.Type] == MasterFlat {
		return ", no flat-bias calibration"
	}
	return ""
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

// masterName is the master's filename. A NON-DEFAULT stacking recipe adds a short fingerprint, so
// a master built with, say, GESD rejection can never overwrite — or be silently reused in place of —
// the default-options master the shared library and every other run depend on. Default options add
// nothing, so existing library masters keep their names and are reused byte-identically.
func masterName(mt MasterType, k inspect.SetKey, stack stackalg.Options) string {
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
	if k.Session != "" {
		// Per-night flat sets of a multi-night scan: unique file per night, else the two nights'
		// stacks silently overwrite each other. Single-night keys carry no Session → names unchanged.
		name += "_n" + k.Session
	}
	if fp := stack.Fingerprint(MasterStackOptions(mt, stackalg.DefaultMasters())); fp != "" {
		name += "_s" + fp
	}
	return name
}

// defaultStack is the default recipe for a master type — the shorthand tests and callers use when
// they are not exercising the per-frame-type knobs.
func defaultStack(mt MasterType) stackalg.Options {
	return MasterStackOptions(mt, stackalg.DefaultMasters())
}
