// Package starnet drives a host-installed StarNet++ v2 (https://www.starnetastro.com) headlessly to
// remove stars from a stretched image, producing a "starless" frame used for star-reduced
// finishing (see internal/pipeline finishWithGimp).
//
// StarNet++ is an optional host tool, invoked like Siril/GIMP: we shell out to the user's own
// install (set via STARNET_BIN) and stream its stdout. It is never vendored. When absent, the
// finish keeps full stars.
package starnet

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

// Runner executes StarNet++ via its command-line interface.
type Runner struct {
	bin string
}

// New returns a Runner for the given StarNet++ binary path. An empty path yields a Runner that
// reports Unavailable, so "not configured" and "not installed" are handled identically.
func New(bin string) *Runner { return &Runner{bin: bin} }

// Progress is one line of StarNet++ output with any embedded percentage extracted.
type Progress struct {
	Line    string
	Percent int // -1 when the line carried no percentage
}

// Options tune star removal.
type Options struct {
	Stride int // tile stride in px; <=0 → StarNet++ default (256)
}

var percentRe = regexp.MustCompile(`(\d+)\s?%`)

// Available reports whether the StarNet++ binary can be found and executed. Soft check: callers log
// the error and keep full stars rather than aborting the run.
func (r *Runner) Available(_ context.Context) error {
	if r == nil || r.bin == "" {
		return fmt.Errorf("starnet binary path is empty (set STARNET_BIN)")
	}
	if _, err := exec.LookPath(r.bin); err != nil {
		return fmt.Errorf("starnet binary %q not found: %w", r.bin, err)
	}
	return nil
}

// RemoveStars runs StarNet++ on inTIFF (a 16-bit TIFF), writing the starless image to outTIFF.
// Progress lines are streamed to onProgress (may be nil).
func (r *Runner) RemoveStars(ctx context.Context, inTIFF, outTIFF string, opts Options, onProgress func(Progress)) error {
	if err := r.Available(ctx); err != nil {
		return err
	}
	return r.run(ctx, removeArgs(inTIFF, outTIFF, opts), onProgress)
}

// removeArgs builds the StarNet++ CLI args. Kept pure and central so the positional form (which
// varies across StarNet builds — verify with the binary's usage) is easy to adjust and unit-test.
func removeArgs(inTIFF, outTIFF string, opts Options) []string {
	args := []string{inTIFF, outTIFF}
	if opts.Stride > 0 {
		args = append(args, strconv.Itoa(opts.Stride))
	}
	return args
}

// run executes StarNet++ with the given args, streaming each output line to onProgress and folding
// stderr into the same stream. A non-zero exit returns an error with the captured log attached.
func (r *Runner) run(ctx context.Context, args []string, onProgress func(Progress)) error {
	cmd := exec.CommandContext(ctx, r.bin, args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start starnet: %w", err)
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
			return fmt.Errorf("starnet failed (exit %d): %w\n%s", exitErr.ExitCode(), err, log.String())
		}
		return fmt.Errorf("starnet: %w", err)
	}
	return nil
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
