package nightscape

// library.go persists phone calibration masters to the reusable library and reuses them across runs.
// A master built from this run's cal frames is saved keyed by ISO/exposure/dimensions; a later run with
// no cal frames but the same-ISO lights matches and loads it. This is the "load them in the library so
// they can be used at any moment" half of the feature — the apply math lives in calibrate.go.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/verove-jordan/astronomy/internal/calib"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/rawmeta"
)

// calPlan is the resolved phone-calibration plan for a run.
type calPlan struct {
	active  bool                // whether calibration will run (else the proven single-pass path)
	masters []calib.PhoneMaster // the reusable library, fetched once
	light   calib.PhoneKey      // light key: ISO/model/exposure from EXIF; dimensions filled after develop
}

// planCalibration decides whether the run calibrates and gathers the inputs. Calibration runs when the
// user supplied cal frames/folders, or when the reusable library holds a master for this light's
// ISO/camera; otherwise the proven single-pass path is kept. The library is read once here and reused.
func planCalibration(ctx context.Context, o Options) calPlan {
	var p calPlan
	if len(o.Frames) > 0 {
		m := rawmeta.Read(o.Frames[0])
		p.light = calib.PhoneKey{CameraModel: m.CameraModel, ISO: m.ISO, ExposureMs: m.ExposureMs}
	}
	if o.PhoneCalib != nil {
		if masters, err := o.PhoneCalib.ListPhoneMasters(ctx); err == nil {
			p.masters = masters
		}
	}
	p.active = hasCalibration(o) || libraryHasCandidate(p)
	return p
}

// libraryHasCandidate reports whether the library holds a master for the light's ISO/camera. It gates
// the two-pass branch before the lights are developed (dimensions unknown yet); the exact dimension
// match is enforced later by MatchPhoneCalibration / matchOrDrop.
func libraryHasCandidate(p calPlan) bool {
	for i := range p.masters {
		m := &p.masters[i]
		if m.ISO == p.light.ISO &&
			(m.CameraModel == "" || p.light.CameraModel == "" || m.CameraModel == p.light.CameraModel) {
			return true
		}
	}
	return false
}

// buildOrReusePhoneMaster returns the master image for one calibration role. Frames captured THIS run
// (an explicit folder or auto-detected stills) win: they are built into a master AND persisted to the
// library for future reuse. Otherwise the matched library master is loaded. Returns (nil, note) when
// neither is available; a note also carries any (soft-fail) persistence problem.
func buildOrReusePhoneMaster(ctx context.Context, o Options, tag string, key calib.PhoneKey, libSel *calib.PhoneMaster) (*fits.Image, string) {
	if raws := calFrames(o, tag); len(raws) > 0 {
		master, note := buildMaster(ctx, o, raws, tag)
		if master == nil {
			return nil, note
		}
		return master, joinNotes(note, persistPhoneMaster(ctx, o, tag, master, raws))
	}
	if libSel != nil {
		im, err := fits.ReadImage(libSel.Path)
		if err != nil {
			return nil, tag + ": load library master: " + err.Error()
		}
		return im, fmt.Sprintf("%s reused from library (%d frames)", tag, libSel.FrameCount)
	}
	return nil, ""
}

// persistPhoneMaster writes a freshly-built master to the library dir (atomic temp+rename) and indexes
// it in the reusable library, keyed by ISO/exposure/dimensions read from the cal frames' EXIF. Best-
// effort: returns a note on any problem and never fails the run. No-op without a library + dir.
func persistPhoneMaster(ctx context.Context, o Options, tag string, master *fits.Image, raws []string) string {
	if o.PhoneCalib == nil || o.LibraryDir == "" {
		return ""
	}
	mt := masterTypeForTag(tag)
	if mt == "" {
		return ""
	}
	meta := rawmeta.Read(raws[0])
	pm := calib.PhoneMaster{
		Type: mt, ISO: meta.ISO, CameraModel: meta.CameraModel,
		Width: master.W, Height: master.H, FrameCount: len(raws),
	}
	if mt == calib.MasterDark {
		pm.ExposureMs = meta.ExposureMs // exposure is only meaningful (and matched) for darks
	}
	path := filepath.Join(o.LibraryDir, phoneMasterName(pm))
	if err := writeMasterAtomic(master, path); err != nil {
		return tag + ": save master: " + err.Error()
	}
	pm.Path = path
	if err := o.PhoneCalib.SavePhoneMaster(ctx, pm); err != nil {
		return tag + ": index master: " + err.Error()
	}
	return ""
}

// phoneMasterName is the library filename for a phone master, e.g.
// phone_master_DARK_iso3200_30000ms_4032x3024.fits (exposure suffix only for darks).
func phoneMasterName(m calib.PhoneMaster) string {
	name := fmt.Sprintf("phone_master_%s_iso%d", m.Type, m.ISO)
	if m.Type == calib.MasterDark {
		name += fmt.Sprintf("_%dms", m.ExposureMs)
	}
	return name + fmt.Sprintf("_%dx%d.fits", m.Width, m.Height)
}

// writeMasterAtomic writes m to path via a temp file + rename, so a concurrent run never reads a
// half-written master from the shared library.
func writeMasterAtomic(m *fits.Image, path string) error {
	if err := fsutil.EnsureDir(filepath.Dir(path)); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := m.WriteFITS(tmp); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func masterTypeForTag(tag string) calib.MasterType {
	switch tag {
	case "dark":
		return calib.MasterDark
	case "bias":
		return calib.MasterBias
	case "flat":
		return calib.MasterFlat
	}
	return ""
}
