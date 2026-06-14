package calib

import (
	"context"
	"fmt"
	"math"
	"os"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// MasterStore is the persistent calibration-master library (implemented by package store).
type MasterStore interface {
	ListMasters(ctx context.Context) ([]Master, error)
	SaveMaster(ctx context.Context, m Master) error
}

// BuildOrReuseMasters builds master calibration frames into the persistent library directory,
// reusing any existing library master that matches a set instead of rebuilding it. Newly built
// masters are added to the library. Returned masters (library + newly built) feed light matching.
func BuildOrReuseMasters(ctx context.Context, runner *siril.Runner, inv *inspect.Inventory,
	lib MasterStore, libDir, workDir string, onProgress func(siril.Progress)) ([]Master, []string, error) {
	if err := fsutil.EnsureDir(libDir); err != nil {
		return nil, nil, err
	}
	var warnings []string
	masters, err := lib.ListMasters(ctx)
	if err != nil {
		warnings = append(warnings, "could not read calibration library: "+err.Error())
		masters = nil
	}

	order := []inspect.FrameType{inspect.Bias, inspect.DarkFlat, inspect.Dark, inspect.Flat}
	for _, ft := range order {
		for _, set := range inv.SetsOfType(ft) {
			if existing := findExisting(masters, set); existing != nil && fileExists(existing.Path) {
				warnings = append(warnings, fmt.Sprintf("reused library master %s (%d frames)", masterByFrameType[set.Key.Type], existing.FrameCount))
				continue
			}
			built, err := buildOne(ctx, runner, set, masters, libDir, workDir, onProgress)
			if err != nil {
				warnings = append(warnings, err.Error())
				continue
			}
			if err := lib.SaveMaster(ctx, built); err != nil {
				warnings = append(warnings, "could not add master to library: "+err.Error())
			}
			masters = append(masters, built)
		}
	}
	return masters, warnings, nil
}

// findExisting returns a library master compatible with a calibration set, or nil.
func findExisting(masters []Master, set inspect.Set) *Master {
	mt := masterByFrameType[set.Key.Type]
	k := set.Key
	for i := range masters {
		m := &masters[i]
		if m.Type != mt || m.Gain != k.Gain || m.Offset != k.Offset || m.Bin != k.Bin || m.Filter != k.Filter {
			continue
		}
		if mt != MasterBias && m.ExposureMs != k.ExposureMs {
			continue
		}
		if (mt == MasterDark || mt == MasterDarkFlat) &&
			math.Abs(float64(m.TempMilliC-int64(k.TempBucket)*1000))/1000 > tempTolC {
			continue
		}
		return m
	}
	return nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
