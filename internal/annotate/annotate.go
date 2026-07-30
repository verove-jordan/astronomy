// Package annotate computes a completed run's star annotation: the number of stars on the
// persisted LINEAR master (windowed to the final image's crop) plus star/DSO name labels
// projected into final-image pixel coordinates through the run's plate-solve WCS. An unsolvable
// field is not an error — the count always comes back, labels only when the solution validates
// against the detected stars. Results persist as <runDir>/stars.json.
package annotate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/verove-jordan/astronomy/internal/buildinfo"
	"github.com/verove-jordan/astronomy/internal/deepstars"
	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/siril"
)

// Sentinel errors callers branch on (the API maps them to 4xx codes).
var (
	ErrUnsupportedMode = errors.New("annotate: mode has no star field")
	ErrNoMaster        = errors.New("annotate: no linear master found")
	ErrNoFinal         = errors.New("annotate: no final image found")
)

// Options configure one annotation pass over a completed run directory.
type Options struct {
	RunDir string
	Mode   string // run mode; "" → read from run.json
	// Locate resolves a run-relative file to a local absolute path (the API wires the S3
	// ensureServable pull-through here); nil → plain os.Stat inside RunDir.
	Locate     func(rel string) (string, bool)
	Runner     *siril.Runner      // nil → no re-solve (count-only when no stored WCS)
	Solve      siril.SolveOptions // focal/pixel/local-Gaia hints (Coords is filled per run)
	CatalogDir string             // Siril DSO catalogue dir (skycat name→coords + DSO labels)
	Now        func() time.Time   // test seam; nil → time.Now
}

// Run counts stars on the run's persisted linear master, projects catalogue names into
// final-image pixels when a WCS is available (re-solving once via Siril when it is not),
// persists <runDir>/stars.json and returns it.
func Run(ctx context.Context, o Options) (*Result, error) {
	if o.Now == nil {
		o.Now = time.Now
	}
	locate := o.Locate
	if locate == nil {
		locate = func(rel string) (string, bool) {
			abs := filepath.Join(o.RunDir, rel)
			_, err := os.Stat(abs)
			return abs, err == nil
		}
	}

	in, err := selectInputs(o.Mode, locate)
	if err != nil {
		return nil, err
	}

	f, err := fits.Open(in.masterAbs)
	if err != nil {
		return nil, fmt.Errorf("annotate: read master header %s: %w", in.masterRel, err)
	}
	im, err := fits.ReadImage(in.masterAbs)
	if err != nil {
		return nil, fmt.Errorf("annotate: read master %s: %w", in.masterRel, err)
	}
	wf, hf, err := pngDims(in.finalAbs)
	if err != nil {
		return nil, fmt.Errorf("annotate: read final image %s: %w", in.finalRel, err)
	}
	m, err := newMapping(im.W, im.H, wf, hf, rowOrderBottomUp(f.Header))
	if err != nil {
		return nil, err
	}

	peaks, count := detectAndCount(im, m)
	res := &Result{
		Version:    1,
		Engine:     buildinfo.String(),
		ComputedAt: o.Now().UTC().Format(time.RFC3339),
		SourceFits: filepath.ToSlash(in.masterRel),
		Count:      count,
		Image:      Dims{Width: wf, Height: hf},
		Solve:      Solve{Method: "none"},
		Labels:     []Label{},
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	wcs, method, reason := findWCS(ctx, o, in, f.Header)
	if method == "" {
		res.Solve.Reason = reason
		return res, res.write(o.RunDir)
	}
	res.Solve.Method = method
	ra, dec, radius := fieldCenter(wcs, m)
	res.Solve.CenterRA, res.Solve.CenterDec = ra, dec
	res.Solve.RadiusDeg = radius
	res.Solve.ScaleArcsec = wcs.ScaleArcsecPerPix()

	grid := newPeakGrid(peaks)
	var probes []probeStar
	for _, s := range deepstars.InField(ra, dec, radius, flipProbeStars, o.Now()) {
		if x, y, ok := wcs.SkyToPix(s.RADeg, s.DecDeg); ok {
			probes = append(probes, probeStar{x, y})
		}
	}
	matched, tried, ok := chooseFlip(&m, grid, probes)
	res.Solve.Matched, res.Solve.Tried = matched, tried
	if !ok {
		res.Solve.Reason = reasonValidationFailed
		return res, res.write(o.RunDir)
	}

	res.Solved = true
	res.Solve.Flip = m.wcsFlip
	labels := append(starLabels(wcs, m, grid, o.Now()), dsoLabels(wcs, m, o.CatalogDir)...)
	sort.SliceStable(labels, func(i, j int) bool { return labels[i].Mag < labels[j].Mag })
	res.Labels = labels
	return res, res.write(o.RunDir)
}
