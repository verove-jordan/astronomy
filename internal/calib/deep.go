package calib

import (
	"context"

	"github.com/verove-jordan/astronomy/internal/fsutil"
	"github.com/verove-jordan/astronomy/internal/inspect"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// RawFrame is a single raw calibration exposure pulled from the persistent catalog (prior sessions).
type RawFrame struct {
	Path       string
	ExposureMs int64
	TempMilliC int64
	HasTemp    bool
	SessionID  int64
}

// RawQuery selects raw calibration frames of one type for a camera signature within a recency window.
type RawQuery struct {
	Type    inspect.FrameType
	Gain    int64
	Offset  int64
	Bin     int
	SinceMs int64 // 0 = no recency bound
}

// RawCalibProvider supplies raw calibration frames from the catalog so masters can be deepened with
// every matching frame ever captured. Implemented by the store (bridged in package pipeline) to keep
// calib free of a database dependency, mirroring MasterStore.
type RawCalibProvider interface {
	RawCalibPaths(ctx context.Context, q RawQuery) ([]RawFrame, error)
}

// DeepOptions tunes the raw-frame pool: how far back darks may be reused and the temperature
// tolerance used to bucket them against a light's needs.
type DeepOptions struct {
	DarkSinceMs int64
	TempTolC    float64
}

func (o DeepOptions) tempTol() float64 {
	if o.TempTolC > 0 {
		return o.TempTolC
	}
	return tempTolC
}

// BuildDeepMasters builds the masters the current run should use, pooling raw bias/dark frames from
// every prior session (the provider) with this session's own — so noise is minimized — while keeping
// flats and dark-flats session-local (they encode this night's optical train). The returned set is
// exactly what light matching should see: deep bias/darks + this session's flats. Newly built masters
// are also saved to the library. Per-set failures are warnings, not fatal.
func BuildDeepMasters(ctx context.Context, runner *siril.Runner, inv *inspect.Inventory,
	provider RawCalibProvider, lib MasterStore, opts DeepOptions, mastersDir, workDir string,
	onProgress func(siril.Progress)) ([]Master, []string, error) {
	if err := fsutil.EnsureDir(mastersDir); err != nil {
		return nil, nil, err
	}
	var masters []Master
	var warnings []string
	add := func(m Master, ok bool, warn string) {
		if warn != "" {
			warnings = append(warnings, warn)
		}
		if ok {
			masters = append(masters, m)
		}
	}

	for _, sig := range biasSigs(inv) {
		m, ok, warn := buildDeepBias(ctx, runner, inv, provider, sig, mastersDir, workDir, onProgress)
		add(m, ok, warn)
	}
	for _, set := range inv.SetsOfType(inspect.DarkFlat) { // session-local (used to calibrate flats)
		m, qc, err := buildOne(ctx, runner, set, masters, mastersDir, workDir, onProgress)
		warnings = append(warnings, qc...)
		add(m, err == nil, errString(err))
	}
	for _, sig := range darkSigs(inv) {
		m, ok, warn := buildDeepDark(ctx, runner, inv, provider, sig, opts, mastersDir, workDir, onProgress)
		add(m, ok, warn)
	}
	for _, set := range inv.SetsOfType(inspect.Flat) { // session-local: this night's dust/vignetting
		m, qc, err := buildOne(ctx, runner, set, masters, mastersDir, workDir, onProgress)
		warnings = append(warnings, qc...)
		add(m, err == nil, errString(err))
	}

	for _, m := range masters {
		if err := lib.SaveMaster(ctx, m); err != nil {
			warnings = append(warnings, "could not add master to library: "+err.Error())
		}
	}
	return masters, warnings, nil
}

// cameraSig identifies a sensor configuration (bias is keyed by this alone).
type cameraSig struct {
	Gain, Offset int64
	Bin          int
}

// darkSig adds the exposure and temperature bucket a dark must match.
type darkSig struct {
	cameraSig
	ExposureMs int64
	TempBucket int
}

// biasSigs collects every camera signature the lights (and any bias sets) need.
func biasSigs(inv *inspect.Inventory) []cameraSig {
	seen := map[cameraSig]bool{}
	var out []cameraSig
	addSig := func(k inspect.SetKey) {
		c := cameraSig{k.Gain, k.Offset, k.Bin}
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	for _, set := range inv.SetsOfType(inspect.Light) {
		addSig(set.Key)
	}
	for _, set := range inv.SetsOfType(inspect.Bias) {
		addSig(set.Key)
	}
	return out
}

// darkSigs collects every dark signature the lights (and any dark sets) need.
func darkSigs(inv *inspect.Inventory) []darkSig {
	seen := map[darkSig]bool{}
	var out []darkSig
	addSig := func(k inspect.SetKey) {
		d := darkSig{cameraSig{k.Gain, k.Offset, k.Bin}, k.ExposureMs, k.TempBucket}
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	for _, set := range inv.SetsOfType(inspect.Light) {
		addSig(set.Key)
	}
	for _, set := range inv.SetsOfType(inspect.Dark) {
		addSig(set.Key)
	}
	return out
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
