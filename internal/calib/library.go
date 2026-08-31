package calib

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"

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
	for i := range masters {
		masters[i].FromLibrary = true
	}

	order := []inspect.FrameType{inspect.Bias, inspect.DarkFlat, inspect.Dark, inspect.Flat}
	for _, ft := range order {
		for _, set := range inv.SetsOfType(ft) {
			existing := findExisting(masters, set)
			reusable := existing != nil && fileExists(existing.Path)
			if reusable && builtFromSet(existing, set) {
				warnings = append(warnings, fmt.Sprintf("reused library master %s (%d frames — same source frames)",
					masterByFrameType[set.Key.Type], existing.FrameCount))
				existing.FromLibrary = false // the file lives in the library, but these are this run's frames
				continue
			}
			// The run brought its own frames for this category and the library cannot prove its
			// master came from them, so the run's frames win — that is the whole contract of putting
			// darks/flats next to the lights. On a DSLR this is not a nicety: gain and offset are
			// absent from the header, so every body and every session collide on one library key and
			// "a master with these settings exists" says nothing about whose frames made it.
			if reusable {
				warnings = append(warnings, fmt.Sprintf(
					"rebuilding the %s master from this run's %d frame(s) — the library master (%d frames) was built from different frames",
					masterByFrameType[set.Key.Type], set.Count, existing.FrameCount))
				masters = dropMaster(masters, existing.Path)
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

// builtFromSet reports whether the on-disk master was stacked from exactly this set's frames,
// read from the .sig sidecar buildOne/stackPooled write beside it. A master with no sidecar
// (built before masters recorded their pool) cannot prove anything and is rebuilt once, which
// writes the sidecar and makes every later run cheap again.
func builtFromSet(m *Master, set inspect.Set) bool {
	recorded, _ := readPoolSig(strings.TrimSuffix(m.Path, ".fits") + ".sig")
	return recorded != "" && recorded == poolSignature(framePaths(set.Frames))
}

// dropMaster removes the superseded library entry so the light matcher cannot pick it over the one
// this run is about to build in its place (same settings, so both would match).
func dropMaster(masters []Master, path string) []Master {
	out := masters[:0]
	for _, m := range masters {
		if m.Path != path {
			out = append(out, m)
		}
	}
	return out
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
