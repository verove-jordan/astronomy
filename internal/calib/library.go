package calib

import (
	"context"
	"fmt"
	"math"
	"os"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/siril"
	"github.com/verove-jordan/astronomy/internal/stackalg"
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
	lib MasterStore, libDir, workDir string, stacks stackalg.MasterOptions,
	onProgress func(siril.Progress)) ([]Master, []string, error) {
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
			built, qc, err := buildOne(ctx, runner, set, masters, libDir, workDir, stacks, onProgress)
			warnings = append(warnings, qc...)
			if err != nil {
				warnings = append(warnings, err.Error())
				continue
			}
			// Per-night masters (multi-night flats) are RUN-LOCAL: master_frames has no night column,
			// so SaveMaster's dedup key would make two nights overwrite each other in the library.
			// They rebuild in seconds; only night-blind masters persist.
			if built.Session == "" {
				if err := lib.SaveMaster(ctx, built); err != nil {
					warnings = append(warnings, "could not add master to library: "+err.Error())
				}
			}
			masters = append(masters, built)
		}
	}
	return masters, warnings, nil
}

// findExisting returns a library master compatible with a calibration set, or nil.
func findExisting(masters []Master, set inspect.Set) *Master {
	for i := range masters {
		if masterMatchesSet(&masters[i], set) {
			return &masters[i]
		}
	}
	return nil
}

// masterMatchesSet reports whether a master is field-compatible with a calibration set — the reuse
// predicate shared by the run's build-or-reuse above and the preview's candidate assembly
// (PreviewCandidates): same type, camera settings, filter and capture night; same exposure for
// non-bias; sensor temperature within tolerance for darks. The night term makes a night-stamped
// flat set (multi-night scan) ALWAYS rebuild — library masters load night-blind (Session ""), and a
// night-blind flat must never satisfy a per-night set (dust may have moved).
func masterMatchesSet(m *Master, set inspect.Set) bool {
	mt := masterByFrameType[set.Key.Type]
	k := set.Key
	if m.Type != mt || m.Gain != k.Gain || m.Offset != k.Offset || m.Bin != k.Bin || m.Filter != k.Filter {
		return false
	}
	if m.Session != k.Session {
		return false
	}
	if mt != MasterBias && m.ExposureMs != k.ExposureMs {
		return false
	}
	if (mt == MasterDark || mt == MasterDarkFlat) &&
		math.Abs(float64(m.TempMilliC-int64(k.TempBucket)*1000))/1000 > tempTolC {
		return false
	}
	return true
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
