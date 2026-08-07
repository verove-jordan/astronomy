package solar

import (
	"context"
	"fmt"
	"io"
	"math"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"golang.org/x/sync/errgroup"

	"github.com/verove-jordan/astronomy/internal/fits"
	"github.com/verove-jordan/astronomy/internal/fsutil"
)

// ingest.go is pass two: materialise only the frames worth stacking, cropped to the disc, as linear
// FITS the rest of the engine can consume.

const (
	// defaultKeepPercent is how much of a clip survives selection.
	//
	// It sits between classic planetary lucky imaging (~15%) and a plain average, because two things
	// pull in opposite directions here. At 40 mm the resolution limit is the aperture rather than the
	// seeing, so frame-to-frame variation is smaller than on a large scope and the stack benefits
	// from depth. But the variation is real, and once frames are ranked by a metric that actually
	// tracks detail rather than noise, being selective pays: the softest third of a clip contributes
	// more blur than signal.
	defaultKeepPercent = 35
	// defaultMaxFrames caps how many frames one source contributes. Beyond a few hundred the SNR
	// gain goes as the square root while the cost stays linear, and the chromosphere has visibly
	// moved on.
	defaultMaxFrames = 300
	// defaultCropMargin is how far past the limb the crop reaches, as a fraction of the radius —
	// enough to keep prominences, which is much of the point of an Hα scope.
	defaultCropMargin = 0.18
	// defaultTransparencyFloor is the transmission, as a fraction of the clip's clearest, below which
	// a frame is treated as clouded and dropped before sharpness is even considered.
	//
	// Measured on a real session with light cloud drifting through: clear frames held within half a
	// percent of each other, while the cloud took transmission to 90%. There is nothing between those
	// two populations, so the threshold only has to land in the gap — 95% does, and leaves ordinary
	// haze and the seeing-driven wobble of the level alone.
	defaultTransparencyFloor = 0.95
	// transparencyReferencePct is the percentile taken as "how clear this clip ever got". Not the
	// maximum, which is one frame and therefore noise; not the median, which sinks with the cloud and
	// would judge a mostly-clouded clip against itself.
	transparencyReferencePct = 90.0
	// transparencyMaxDrop is the largest fraction of a clip the gate may remove. A session shot
	// through broken cloud is still a session: past this the run keeps the clearest frames it has and
	// says what it did, rather than returning nothing.
	transparencyMaxDrop = 0.6
)

// IngestOptions tunes frame materialisation.
type IngestOptions struct {
	FFmpegBin  string
	WorkDir    string  // where the FITS frames are written
	KeepPct    int     // percent of the sharpest frames to keep; ≤0 → defaultKeepPercent
	MaxFrames  int     // cap per source; ≤0 → defaultMaxFrames
	CropMargin float64 // ≤0 → defaultCropMargin
	// TargetRadius is the group's disc radius in full-resolution pixels, normally carried over from
	// triage so the crop size is decided once for the whole group. 0 measures it from the frames.
	TargetRadius float64
	// Band forces the colour channel the signal is read from; empty or BandAuto detects it.
	Band Band
	// TransparencyFloor drops frames whose transmission fell below this fraction of the clip's
	// clearest. 0 disables the gate, the same way DeconvSigma does; the preset always sets it
	// explicitly, so only a zero-value IngestOptions — a test reaching for the ingest alone — sees
	// the gate off.
	TransparencyFloor float64
}

func (o IngestOptions) band() Band {
	if o.Band == "" {
		return BandAuto
	}
	return o.Band
}

func (o IngestOptions) keepPct() int {
	if o.KeepPct > 0 && o.KeepPct <= 100 {
		return o.KeepPct
	}
	return defaultKeepPercent
}

func (o IngestOptions) maxFrames() int {
	if o.MaxFrames > 0 {
		return o.MaxFrames
	}
	return defaultMaxFrames
}

func (o IngestOptions) cropMargin() float64 {
	if o.CropMargin > 0 {
		return o.CropMargin
	}
	return defaultCropMargin
}

// Frame is one materialised, linear-light frame ready for registration and stacking.
type Frame struct {
	Path   string  `json:"path"`
	Source string  `json:"source"`
	Index  int     `json:"index"`   // frame number within its source
	TimeMs int64   `json:"time_ms"` // capture instant, Unix ms
	Score  float64 `json:"score"`   // sharpness, comparable within a source
	Limb   Limb    `json:"limb"`    // geometry in the materialised frame's own coordinates
}

// IngestVideo scans a clip, selects its sharpest frames and writes them out cropped to the disc.
func IngestVideo(ctx context.Context, path string, info VideoInfo, opts IngestOptions) ([]Frame, []string, error) {
	if err := fsutil.EnsureDir(opts.WorkDir); err != nil {
		return nil, nil, err
	}
	scan, err := scanVideo(ctx, opts.FFmpegBin, path, info, opts.cropMargin())
	if err != nil {
		return nil, nil, err
	}
	var warnings []string
	unclouded, cloudNote := gateTransparency(scan.frames, opts.TransparencyFloor)
	if cloudNote != "" {
		warnings = append(warnings, filepath.Base(path)+": "+cloudNote)
	}
	keep := selectFrames(unclouded, opts.keepPct(), opts.maxFrames())
	if len(keep) == 0 {
		return nil, nil, fmt.Errorf("ingest %s: no frame had a measurable limb", filepath.Base(path))
	}
	if dropped := len(scan.frames) - len(keep); dropped > 0 {
		warnings = append(warnings, fmt.Sprintf("%s: kept %d of %d frames (%s crop)",
			filepath.Base(path), len(keep), len(scan.frames), scan.crop))
	}
	frames, err := extractSelected(ctx, path, info, scan, keep, opts)
	if err != nil {
		return nil, warnings, err
	}
	return frames, warnings, nil
}

// gateTransparency drops the frames a cloud was in front of, and says what it removed.
//
// It runs BEFORE the sharpness ranking rather than being folded into it, and that ordering is the
// whole idea. Sharpness here is contrast — band-pass energy over the frame's own median — so it sees
// a cloud's veiling glow but is blind to its extinction, and it puts what it does see on the same
// axis as seeing. Those two are not comparable. A frame blurred by seeing is a fair sample of the
// Sun that registration and averaging improve; a frame behind cloud is a fair sample of the Sun plus
// a glow, and no amount of averaging removes an additive veil. Worse, photometric normalisation
// downstream then maps that frame's disc back onto the group median — scaling the veil up with the
// signal, so a clouded frame arrives at the stack looking correctly exposed and quietly pulls the
// contrast of every pixel it touches.
//
// The reference is per CLIP. Each clip is asked to contribute its own best frames, and the levels
// between clips are the business of normalisation and, when they differ by enough to matter, of the
// exposure tiering.
func gateTransparency(frames []frameScan, floor float64) ([]frameScan, string) {
	if floor <= 0 {
		return frames, ""
	}
	levels := make([]float64, 0, len(frames))
	for _, f := range frames {
		if f.ok && f.level > 0 {
			levels = append(levels, f.level)
		}
	}
	if len(levels) < 8 { // too few to know what "clear" looked like
		return frames, ""
	}
	ref := percentileOf(levels, transparencyReferencePct)
	if ref <= 0 {
		return frames, ""
	}
	cut := floor * ref
	kept := make([]frameScan, 0, len(frames))
	worst := math.Inf(1)
	for _, f := range frames {
		if f.ok && f.level > 0 && f.level < cut {
			worst = math.Min(worst, f.level/ref)
			continue
		}
		kept = append(kept, f)
	}
	dropped := len(frames) - len(kept)
	if dropped == 0 {
		return frames, ""
	}
	if float64(dropped) > transparencyMaxDrop*float64(len(frames)) {
		// Broken cloud all session: keep the clearest frames up to the cap rather than the handful
		// that happened to clear the bar, and be explicit that the whole run is compromised.
		kept = clearestFrames(frames, int(float64(len(frames))*(1-transparencyMaxDrop)))
		return kept, fmt.Sprintf(
			"cloud through most of the clip — transmission fell to %.0f%% of its clearest; kept the %d clearest of %d frames",
			100*worst, len(kept), len(frames))
	}
	return kept, fmt.Sprintf(
		"cloud dropped %d of %d frames below %.0f%% transmission (worst %.0f%%)",
		dropped, len(frames), 100*floor, 100*worst)
}

// clearestFrames keeps the n most transparent frames, back in capture order.
func clearestFrames(frames []frameScan, n int) []frameScan {
	if n < 1 {
		n = 1
	}
	byLevel := append([]frameScan(nil), frames...)
	sort.SliceStable(byLevel, func(i, j int) bool { return byLevel[i].level > byLevel[j].level })
	if n > len(byLevel) {
		n = len(byLevel)
	}
	out := byLevel[:n]
	sort.SliceStable(out, func(i, j int) bool { return out[i].index < out[j].index })
	return out
}

// percentileOf is a percentile over a float64 slice, which imgops only offers for float32 planes.
func percentileOf(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	i := clampInt(int(p/100*float64(len(s)-1)+0.5), 0, len(s)-1)
	return s[i]
}

// selectFrames keeps the sharpest frames, bounded by both the percentage and the hard cap, and
// returns their indices in capture order.
func selectFrames(all []frameScan, keepPct, maxFrames int) []frameScan {
	usable := make([]frameScan, 0, len(all))
	for _, f := range all {
		if f.ok && f.score > 0 {
			usable = append(usable, f)
		}
	}
	if len(usable) == 0 {
		return nil
	}
	n := len(usable) * keepPct / 100
	if n > maxFrames {
		n = maxFrames
	}
	if n < 1 {
		n = 1
	}
	sort.SliceStable(usable, func(i, j int) bool { return usable[i].score > usable[j].score })
	keep := usable[:n]
	// Back into capture order: the frames become a time series (the session time-lapse, and the
	// windowing that keeps a stack inside the chromosphere's coherence time both depend on it).
	sort.SliceStable(keep, func(i, j int) bool { return keep[i].index < keep[j].index })
	return keep
}

// extractSelected re-decodes the clip, keeping only the chosen frames, and writes each as FITS.
func extractSelected(ctx context.Context, path string, info VideoInfo, scan scanResult,
	keep []frameScan, opts IngestOptions) ([]Frame, error) {

	ffmpegBin := opts.FFmpegBin
	if ffmpegBin == "" {
		ffmpegBin = "ffmpeg"
	}
	dw, dh := displayDims(info)
	crop := scan.crop
	if crop.w <= 0 || crop.h <= 0 {
		crop = cropRect{0, 0, dw, dh}
	}

	// Every frame is streamed and the wanted ones are picked here, rather than asking ffmpeg to
	// select them. A `select=eq(n\,..)+...` expression is the obvious approach and does not scale:
	// its parser gives up somewhere past a hundred terms ("Error while parsing expression"), and a
	// real clip selects hundreds. Streaming costs pipe bandwidth on frames we discard — the decode
	// happens either way — and buys an unambiguous mapping, since with passthrough timing the Nth
	// frame off the pipe is the Nth frame of the clip, with nothing able to silently renumber them.
	args := []string{"-v", "error", "-i", path}
	if !crop.covers(dw, dh) {
		args = append(args, "-vf", fmt.Sprintf("crop=%d:%d:%d:%d", crop.w, crop.h, crop.x, crop.y))
	}
	args = append(args, "-fps_mode", "passthrough", "-f", "rawvideo", "-pix_fmt", "gray16be", "-")
	cmd := exec.CommandContext(ctx, ffmpegBin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	out, err := readAndWriteFrames(stdout, crop, info, path, keep, opts.WorkDir)
	if err != nil {
		return nil, fmt.Errorf("extract %s: %w\n%s", filepath.Base(path), err, tailLines(stderr.String(), 4))
	}
	return out, nil
}

// readAndWriteFrames consumes the selected frames off the pipe and persists them.
func readAndWriteFrames(r io.Reader, crop cropRect, info VideoInfo, src string,
	keep []frameScan, workDir string) ([]Frame, error) {

	wanted := make(map[int]float64, len(keep))
	for _, f := range keep {
		wanted[f.index] = f.score
	}
	last := keep[len(keep)-1].index
	msPerFrame := 0.0
	if info.FPS > 0 {
		msPerFrame = 1000 / info.FPS
	}
	base := sanitizeName(src)

	// The reader must keep up with ffmpeg or the pipe stalls the decoder, while the per-frame work
	// — linearise, fit the limb, write FITS — is independent and several times more expensive than
	// the read. So the loop below only reads and dispatches; workers do the rest.
	type job struct {
		index int
		score float64
		plane []float32
	}
	jobs := make(chan job, ingestWorkers())
	results := make(chan Frame, ingestWorkers())
	g, gctx := errgroup.WithContext(context.Background())
	for w := 0; w < ingestWorkers(); w++ {
		g.Go(func() error {
			im := &fits.Image{W: crop.w, H: crop.h, C: 1, Pix: make([][]float32, 1)}
			for j := range jobs {
				im.Pix[0] = j.plane
				Linearize(im.Pix[0], info)
				dst := filepath.Join(workDir, fmt.Sprintf("%s_%05d.fits", base, j.index))
				if err := im.WriteFITS(dst); err != nil {
					return err
				}
				f := Frame{Path: dst, Source: src, Index: j.index, Score: j.score,
					TimeMs: info.CreatedMs + int64(float64(j.index)*msPerFrame)}
				if l, ok := FitLimb(im); ok {
					f.Limb = l
				}
				select {
				case results <- f:
				case <-gctx.Done():
					return gctx.Err()
				}
			}
			return nil
		})
	}
	done := make(chan []Frame, 1)
	go func() {
		out := make([]Frame, 0, len(keep))
		for f := range results {
			out = append(out, f)
		}
		done <- out
	}()

	readErr := func() error {
		defer close(jobs)
		buf := make([]byte, crop.w*crop.h*2)
		for n := 0; n <= last; n++ {
			if _, err := io.ReadFull(r, buf); err != nil {
				if err == io.EOF || err == io.ErrUnexpectedEOF {
					return nil // the clip ended early; keep what arrived rather than losing the run
				}
				return err
			}
			score, want := wanted[n]
			if !want {
				continue
			}
			plane := make([]float32, crop.w*crop.h)
			decodeGray16BE(buf, plane)
			select {
			case jobs <- job{index: n, score: score, plane: plane}:
			case <-gctx.Done():
				return gctx.Err()
			}
		}
		return nil
	}()

	werr := g.Wait()
	close(results)
	out := <-done
	if readErr != nil {
		return out, readErr
	}
	if werr != nil {
		return out, werr
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no frames were delivered")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Index < out[j].Index })
	return out, nil
}

// ingestWorkers sizes the per-frame worker pool, leaving headroom so a long ingest does not starve
// the rest of the engine.
func ingestWorkers() int {
	n := runtime.NumCPU() - 2
	if n < 2 {
		return 2
	}
	if n > 8 {
		return 8
	}
	return n
}
