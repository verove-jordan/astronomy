// Package graxpert drives a host-installed GraXpert (https://www.graxpert.com) headlessly to
// remove background light-pollution gradients (and optionally denoise) from a linear FITS stack.
//
// GraXpert is an optional host tool, invoked exactly like Siril/GIMP: we shell out to the user's
// own install (set via GRAXPERT_BIN) and stream its stdout for progress. It is never vendored or
// bundled, so its AGPL licence stays with the user's binary. When GraXpert is absent the pipeline
// falls back to Siril's polynomial subsky (see internal/pipeline).
package graxpert

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/verove-jordan/astronomy/internal/sysmon"
)

// Runner executes GraXpert. In the default (local) mode it shells out to the host-installed binary; when
// a host-service URL is set it OFFLOADS each operation to a native GraXpert HTTP service (cmd/graxpert-host)
// over that URL — the only way the containerized engine can run GraXpert natively (Docker on macOS can't
// exec a host binary, and CoreML is unreachable from the Linux container). Both modes share this type.
type Runner struct {
	bin string
	url string // optional host GraXpert service base URL; when non-empty, Denoise/ExtractBackground offload to it
	hc  *http.Client

	gpu   bool // default -gpu (from ASTRO_GRAXPERT_GPU); a per-call opts.GPU still forces it on
	batch int  // default denoise -batch_size (from ASTRO_GRAXPERT_BATCH); a per-call opts.Batch overrides

	// Deep-health memo (see health.go). Guarded by healthMu; a probe can take minutes on first
	// run (model download), so callers needing a non-blocking answer use HealthCached.
	healthMu   sync.Mutex
	healthDone bool
	healthErr  error
}

// SetDefaults sets the GPU flag and denoise batch size the runner falls back to when a call leaves them
// unset (wired from config). Returns r for chaining after New.
func (r *Runner) SetDefaults(gpu bool, batch int) *Runner {
	if r != nil {
		r.gpu, r.batch = gpu, batch
	}
	return r
}

// New returns a Runner for the given GraXpert binary path and optional host-service URL. url wins: when
// set, operations offload to the native host service; when empty the local bin is exec'd. An empty bin
// AND empty url yields a Runner that reports Unavailable, so "not configured" and "not installed" are
// handled identically by callers.
func New(bin, url string) *Runner {
	return &Runner{bin: bin, url: strings.TrimRight(url, "/"), hc: &http.Client{}}
}

// Progress is one line of GraXpert output with any embedded percentage extracted. When Sample is
// non-nil the Progress carries a live resource reading instead of a log line (Line is empty).
type Progress struct {
	Line    string
	Percent int // -1 when the line carried no percentage
	Sample  *sysmon.Sample
}

// BackgroundOptions tune GraXpert background extraction. GPU defaults to false (CPU). On Apple Silicon
// the background-extraction model IS CoreML-compatible, so -gpu true accelerates it natively (unlike the
// denoise model — see DenoiseOptions).
type BackgroundOptions struct {
	GPU bool // -gpu true to use GPU/CoreML acceleration
}

// DenoiseOptions tune GraXpert denoising. NOTE: GraXpert's denoise AI model does NOT compile on Apple's
// CoreML (verified: both MLProgram and NeuralNetwork backends fail), so -gpu true has no effect for
// denoise on macOS — it runs on CPU regardless. Batch (-batch_size) and Strength (-strength) DO help:
// a larger batch denoises more tiles in parallel.
type DenoiseOptions struct {
	GPU      bool
	Batch    int     // -batch_size (tiles in parallel; 0 → GraXpert default 4). Higher = faster, more RAM.
	Strength float64 // -strength in 0..1 (0 → GraXpert default 0.5)
}

var percentRe = regexp.MustCompile(`(\d+)\s?%`)

// Available reports whether the GraXpert binary can be found and executed. It is a soft check:
// callers log the error and fall back to the Siril path rather than aborting the run.
func (r *Runner) Available(ctx context.Context) error {
	if r == nil {
		return fmt.Errorf("graxpert runner is nil")
	}
	if r.url != "" {
		return r.remotePing(ctx) // host-offload mode: the service reachable == available
	}
	if r.bin == "" {
		return fmt.Errorf("graxpert binary path is empty (set GRAXPERT_BIN)")
	}
	if _, err := exec.LookPath(r.bin); err != nil {
		return fmt.Errorf("graxpert binary %q not found: %w", r.bin, err)
	}
	return nil
}

// GraXpert operation names (the -cmd values), shared by the local args and the remote request.
const (
	OpBackground = "background-extraction"
	OpDenoise    = "denoising"
)

// ExtractBackground runs GraXpert background extraction on inPath (a linear FITS), writing the
// gradient-removed image to outPath. Offloads to the host service when a URL is set. Progress lines are
// streamed to onProgress (may be nil).
func (r *Runner) ExtractBackground(ctx context.Context, inPath, outPath string, opts BackgroundOptions, onProgress func(Progress)) error {
	opts.GPU = opts.GPU || r.gpu
	if r.url != "" {
		return r.runRemote(ctx, RemoteRequest{Op: OpBackground, In: inPath, Out: outPath, GPU: opts.GPU}, onProgress)
	}
	if err := r.Available(ctx); err != nil {
		return err
	}
	return r.run(ctx, backgroundArgs(inPath, outPath, opts), onProgress)
}

// Denoise runs GraXpert AI denoising on inPath, writing the result to outPath. Offloads to the host
// service when a URL is set.
func (r *Runner) Denoise(ctx context.Context, inPath, outPath string, opts DenoiseOptions, onProgress func(Progress)) error {
	opts.GPU = opts.GPU || r.gpu
	if opts.Batch == 0 {
		opts.Batch = r.batch
	}
	if r.url != "" {
		return r.runRemote(ctx, RemoteRequest{Op: OpDenoise, In: inPath, Out: outPath, GPU: opts.GPU, Batch: opts.Batch, Strength: opts.Strength}, onProgress)
	}
	if err := r.Available(ctx); err != nil {
		return err
	}
	return r.run(ctx, denoiseArgs(inPath, outPath, opts), onProgress)
}

// backgroundArgs builds the GraXpert CLI args for background extraction (pure; unit-tested).
func backgroundArgs(inPath, outPath string, opts BackgroundOptions) []string {
	return baseArgs(OpBackground, inPath, outPath, opts.GPU)
}

// denoiseArgs builds the GraXpert CLI args for denoising, appending the denoise-only -batch_size /
// -strength knobs when set (they are dropped by GraXpert for background extraction).
func denoiseArgs(inPath, outPath string, opts DenoiseOptions) []string {
	args := baseArgs(OpDenoise, inPath, outPath, opts.GPU)
	if opts.Batch > 0 {
		args = append(args, "-batch_size", strconv.Itoa(opts.Batch))
	}
	if opts.Strength > 0 {
		args = append(args, "-strength", strconv.FormatFloat(opts.Strength, 'f', 2, 64))
	}
	return args
}

// baseArgs is the shared GraXpert 3.x command form: `<filename> -cmd <op> -output <out> -gpu <bool>`.
// The filename is positional and placed first to keep it unambiguous from `-output`'s optional arg.
func baseArgs(op, inPath, outPath string, gpu bool) []string {
	return []string{
		inPath,
		"-cmd", op,
		"-output", outPath,
		"-gpu", strconv.FormatBool(gpu),
	}
}

// run executes GraXpert with the given args, streaming each output line to onProgress and folding
// stderr into the same stream. A non-zero exit returns an error with the captured log attached.
func (r *Runner) run(ctx context.Context, args []string, onProgress func(Progress)) error {
	cmd := exec.CommandContext(ctx, r.bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start graxpert: %w", err)
	}

	// emit serializes callbacks so the monitor goroutine's samples can't race the scan loop's lines.
	var emitMu sync.Mutex
	emit := func(p Progress) {
		if onProgress == nil {
			return
		}
		emitMu.Lock()
		defer emitMu.Unlock()
		onProgress(p)
	}

	mon := sysmon.Start(ctx, cmd.Process.Pid, 0, func(s sysmon.Sample) {
		emit(Progress{Sample: &s})
	})
	defer mon.Stop()

	var log strings.Builder
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		log.WriteString(line)
		log.WriteByte('\n')
		emit(Progress{Line: line, Percent: parsePercent(line)})
	}

	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("graxpert failed (exit %d): %w\n%s", exitErr.ExitCode(), err, log.String())
		}
		return fmt.Errorf("graxpert: %w", err)
	}
	// GraXpert logs fatal failures (e.g. a misconfigured ONNX runtime) but still exits 0, so a clean
	// exit alone is not proof of success — surface a logged critical error as a failure.
	if line := firstErrorLine(log.String()); line != "" {
		return fmt.Errorf("graxpert reported an error: %s", line)
	}
	return nil
}

// firstErrorLine returns GraXpert's first "Critical error" log line, if any. GraXpert can exit 0
// even after a fatal AI/ONNX error, so the pipeline checks the log (and the output file) too.
func firstErrorLine(log string) string {
	for _, line := range strings.Split(log, "\n") {
		if strings.Contains(line, "Critical error") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func parsePercent(line string) int {
	if m := percentRe.FindStringSubmatch(line); m != nil {
		n, err := strconv.Atoi(m[1])
		if err != nil || n > 100 {
			return -1
		}
		return n
	}
	return -1
}
