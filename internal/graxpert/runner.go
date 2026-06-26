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
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Runner executes GraXpert via its command-line interface.
type Runner struct {
	bin string
}

// New returns a Runner for the given GraXpert binary path. An empty path yields a Runner that
// reports Unavailable, so "not configured" and "not installed" are handled identically by callers.
func New(bin string) *Runner { return &Runner{bin: bin} }

// Progress is one line of GraXpert output with any embedded percentage extracted.
type Progress struct {
	Line    string
	Percent int // -1 when the line carried no percentage
}

// BackgroundOptions tune GraXpert background extraction. GraXpert 3.x exposes only GPU toggling on
// the command line; smoothing / interpolation method come from its saved preferences (or a
// -preferences_file, not yet wired). GPU defaults to false (CPU) — most portable on macOS.
type BackgroundOptions struct {
	GPU bool // -gpu true to use GPU/CoreML acceleration
}

// DenoiseOptions tune GraXpert denoising. Like background extraction, only GPU is CLI-settable in
// GraXpert 3.x (denoise strength is a preference).
type DenoiseOptions struct {
	GPU bool
}

var percentRe = regexp.MustCompile(`(\d+)\s?%`)

// Available reports whether the GraXpert binary can be found and executed. It is a soft check:
// callers log the error and fall back to the Siril path rather than aborting the run.
func (r *Runner) Available(_ context.Context) error {
	if r == nil || r.bin == "" {
		return fmt.Errorf("graxpert binary path is empty (set GRAXPERT_BIN)")
	}
	if _, err := exec.LookPath(r.bin); err != nil {
		return fmt.Errorf("graxpert binary %q not found: %w", r.bin, err)
	}
	return nil
}

// ExtractBackground runs GraXpert background extraction on inPath (a linear FITS), writing the
// gradient-removed image to outPath. Progress lines are streamed to onProgress (may be nil).
func (r *Runner) ExtractBackground(ctx context.Context, inPath, outPath string, opts BackgroundOptions, onProgress func(Progress)) error {
	if err := r.Available(ctx); err != nil {
		return err
	}
	return r.run(ctx, backgroundArgs(inPath, outPath, opts), onProgress)
}

// Denoise runs GraXpert AI denoising on inPath, writing the result to outPath.
func (r *Runner) Denoise(ctx context.Context, inPath, outPath string, opts DenoiseOptions, onProgress func(Progress)) error {
	if err := r.Available(ctx); err != nil {
		return err
	}
	return r.run(ctx, denoiseArgs(inPath, outPath, opts), onProgress)
}

// backgroundArgs builds the GraXpert CLI args for background extraction. Kept pure and central so
// the exact flag spelling is easy to adjust and unit-test. Verified against GraXpert 3.2:
//
//	graxpert <filename> -cmd background-extraction -output <out> -gpu {true,false}
//
// The filename is positional and placed first to keep it unambiguous from `-output`'s optional arg.
func backgroundArgs(inPath, outPath string, opts BackgroundOptions) []string {
	return cmdArgs("background-extraction", inPath, outPath, opts.GPU)
}

// denoiseArgs builds the GraXpert CLI args for denoising (pure; see backgroundArgs).
func denoiseArgs(inPath, outPath string, opts DenoiseOptions) []string {
	return cmdArgs("denoising", inPath, outPath, opts.GPU)
}

// cmdArgs is the shared GraXpert 3.x command form: `<filename> -cmd <op> -output <out> -gpu <bool>`.
func cmdArgs(op, inPath, outPath string, gpu bool) []string {
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

	var log strings.Builder
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		log.WriteString(line)
		log.WriteByte('\n')
		if onProgress != nil {
			onProgress(Progress{Line: line, Percent: parsePercent(line)})
		}
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
