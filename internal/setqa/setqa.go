// Package setqa flags light sets carrying a stray-light or gradient artifact (one-sided border
// halo, strong background plane, OSC channel imbalance) BEFORE stacking, and quantifies what
// excluding each flagged set would cost (frames, integration time, predicted SNR). Pure read:
// sampled frames are loaded from disk, nothing is written. The Import "check frame sets" button
// drives it via POST /api/quality/sets; the SetKey.ID tokens it returns feed
// RunRequest.exclude_sets.
package setqa

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/inspect"
)

// Options tunes the detector. The zero Load falls back to fits.ReadImage (it is a seam so tests
// can inject synthetic images without disk I/O).
type Options struct {
	MaxProbesPerSet int     // frames sampled per set
	MaxProbesTotal  int     // global probe budget, evenly distributed (min 3/set)
	Workers         int     // concurrent frame loads
	AbsBorderSigma  float64 // absolute floor: border excess in tile-noise sigmas
	AbsBorderPct    float64 // absolute floor: border amplitude as % of sky background
	RelSigma        float64 // MAD multiplier vs same-filter sibling sets
	MinRelExcess    float64 // relative rule also requires raw > median*(1+MinRelExcess)
	StackSigma      float64 // stack-visibility rule: flag when raw·√frames reaches this
	MinAmplitudePct float64 // stack-visibility rule: minimum amplitude as % of sky
	Load            func(path string) (*fits.Image, error)
}

// DefaultOptions keeps a full analysis interactive (≤64 subsampled FITS reads) while staying
// conservative enough that ordinary light-pollution gradients don't false-positive.
func DefaultOptions() Options {
	return Options{
		MaxProbesPerSet: 8,
		MaxProbesTotal:  64,
		Workers:         4,
		AbsBorderSigma:  6.0,
		AbsBorderPct:    8.0,
		RelSigma:        3.5,
		MinRelExcess:    0.5,
		StackSigma:      10.0,
		MinAmplitudePct: 1.0,
		Load:            fits.ReadImage,
	}
}

// Report is the full analysis result for one capture selection.
type Report struct {
	Sets     []SetReport `json:"sets"` // every light set — clean ones give the modal context
	Flagged  int         `json:"flagged"`
	Warnings []string    `json:"warnings,omitempty"`
}

// SetReport is the verdict + exclusion impact for one light set.
type SetReport struct {
	ID                 string         `json:"id"` // inspect.SetKey.ID() — the exclusion token
	Key                inspect.SetKey `json:"key"`
	Count              int            `json:"count"`
	TotalIntegrationMs int64          `json:"total_integration_ms"`
	Sampled            int            `json:"sampled"`
	Measured           bool           `json:"measured"` // false when <2 probes were readable
	AffectedFrac       float64        `json:"affected_frac"`
	// Per-set diagnostics (medians over the sampled frames): the border/gradient components, the
	// worst side, and the stack-amplified severity — a consistent per-frame pattern integrates
	// coherently while noise averages out, so visibility in the FINAL scales with raw·√frames.
	BorderSigma  float64 `json:"border_sigma"`
	BorderPct    float64 `json:"border_pct"`
	GradSigma    float64 `json:"grad_sigma"`
	GradPct      float64 `json:"grad_pct"`
	WorstBorder  string  `json:"worst_border,omitempty"`
	StackedSigma float64 `json:"stacked_sigma"`
	Score        float64 `json:"score"` // 0..100, 50 == at the stack-visibility threshold
	Flagged      bool    `json:"flagged"`
	Reasons            []Reason       `json:"reasons,omitempty"`
	PreviewFrame       string         `json:"preview_frame,omitempty"` // worst sampled frame, for GET /api/preview
	Impact             Impact         `json:"impact"`
}

// Reason is structured so the frontend can translate it; Text is the English fallback used in
// job logs and provenance.
type Reason struct {
	Code         string  `json:"code"` // border_glow | strong_gradient | outlier_vs_siblings | channel_imbalance
	Border       string  `json:"border,omitempty"`  // left|right|top|bottom
	Channel      string  `json:"channel,omitempty"` // "", or R|G|B for color frames
	AmplitudePct float64 `json:"amplitude_pct"`     // % of sky background
	Sigma        float64 `json:"sigma"`
	Text         string  `json:"text"`
}

// Impact quantifies what excluding the set costs its filter channel.
type Impact struct {
	Filter              string  `json:"filter"`
	FilterFrames        int     `json:"filter_frames"`
	FilterIntegrationMs int64   `json:"filter_integration_ms"`
	LostFrames          int     `json:"lost_frames"`
	LostIntegrationMs   int64   `json:"lost_integration_ms"`
	LostIntegrationPct  float64 `json:"lost_integration_pct"`
	SNRFactor           float64 `json:"snr_factor"` // sqrt(kept/total) — predicted channel SNR multiplier
	EmptiesFilter       bool    `json:"empties_filter"`
}

// Analyze probes every light set of inv and reports per-set artifact verdicts + exclusion impact.
func Analyze(ctx context.Context, inv *inspect.Inventory, opts Options) (*Report, error) {
	if opts.Load == nil {
		opts.Load = fits.ReadImage
	}
	sets := lightSets(inv)
	probes, warnings, err := probeAll(ctx, sets, opts)
	if err != nil {
		return nil, err
	}
	reports := flagSets(sets, probes, opts)
	flagged := 0
	for _, r := range reports {
		if r.Flagged {
			flagged++
		}
	}
	return &Report{Sets: reports, Flagged: flagged, Warnings: warnings}, nil
}

func lightSets(inv *inspect.Inventory) []inspect.Set {
	sets := make([]inspect.Set, 0, len(inv.Sets))
	for _, s := range inv.Sets {
		if s.Key.Type == inspect.Light {
			sets = append(sets, s)
		}
	}
	return sets
}

// probesPerSet spreads the global budget evenly, clamped to the per-set cap and a floor of 3
// (a median over fewer samples says nothing).
func probesPerSet(nSets int, opts Options) int {
	if nSets == 0 {
		return 0
	}
	return max(min(opts.MaxProbesTotal/nSets, opts.MaxProbesPerSet), 3)
}

const maxProbeWarnings = 8

// probeAll loads the sampled frames of every set through a bounded worker pool. A frame that
// fails to load is skipped with a warning — S3-freed inputs must degrade, never fail the analysis.
func probeAll(ctx context.Context, sets []inspect.Set, opts Options) ([][]FrameProbe, []string, error) {
	perSet := probesPerSet(len(sets), opts)
	probes := make([][]FrameProbe, len(sets))
	var warnings []string
	skipped := 0

	sem := make(chan struct{}, max(opts.Workers, 1))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for si, set := range sets {
		for _, fr := range sampleFrames(set, perSet) {
			wg.Add(1)
			go func(si int, path string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				if ctx.Err() != nil {
					return
				}
				probe, err := measureFrame(opts.Load, path)
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					skipped++
					if len(warnings) < maxProbeWarnings {
						warnings = append(warnings, fmt.Sprintf("probe %s: %v", filepath.Base(path), err))
					}
					return
				}
				probes[si] = append(probes[si], probe)
			}(si, fr.Path)
		}
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if extra := skipped - maxProbeWarnings; extra > 0 {
		warnings = append(warnings, fmt.Sprintf("+%d more unreadable frame(s)", extra))
	}
	return probes, warnings, nil
}
