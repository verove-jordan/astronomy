// Package siril drives a host-installed Siril (siril-cli) headlessly: it generates .ssf
// scripts, runs them, and parses Siril's log/progress output. Siril 1.4 prints "log:"-prefixed
// lines to stdout in script mode; its Python-venv init may warn but does not affect SSF runs.
package siril

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/verove-jordan/astronomy/internal/sysmon"
)

// Limits bound a Siril run's resource use so a heavy stack does not freeze the host. MaxCPUs and
// MemRatio are applied via Siril's own `setcpu`/`setmem` script commands; Nice lowers the
// siril-cli OS scheduling priority. A zero value in any field leaves that knob at Siril's default.
type Limits struct {
	MaxCPUs  int     // setcpu N — cap processing threads (<=0 → Siril default: all cores)
	MemRatio float64 // setmem R — fraction of available RAM Siril may use (<=0 → Siril default 0.9)
	Nice     int     // OS niceness added to the child process (<=0 → unchanged)
}

// Runner executes Siril scripts via the siril-cli binary.
type Runner struct {
	bin string
	lim Limits
}

// New returns a Runner for the given siril-cli path and resource limits.
func New(bin string, lim Limits) *Runner { return &Runner{bin: bin, lim: lim} }

// Limits returns the runner's configured resource limits.
func (r *Runner) Limits() Limits { return r.lim }

// WithLimits returns a copy of the runner with different limits (same binary) — the parallel
// channel waves divide the CPU/memory budget between concurrent Siril instances.
func (r *Runner) WithLimits(lim Limits) *Runner { return &Runner{bin: r.bin, lim: lim} }

// Progress is one line of Siril output, with any embedded percentage extracted. When Sample is
// non-nil the Progress carries a live resource reading instead of a log line (Line is empty).
type Progress struct {
	Line    string
	Percent int // -1 when the line carried no percentage
	Sample  *sysmon.Sample
}

// Result is the outcome of a script run.
type Result struct {
	Log      string
	ExitCode int
}

var (
	percentRe     = regexp.MustCompile(`(\d+)\s?%`)
	logPrefix     = regexp.MustCompile(`^log:\s?`)
	versionRe     = regexp.MustCompile(`(?i)siril\s+v?(\d+)\.(\d+)`)
	fullVersionRe = regexp.MustCompile(`(?i)siril\s+v?(\d+\.\d+(?:\.\d+)?)`)
	errLocatorRe  = regexp.MustCompile(`(?i)^error in line \d+`)
)

// The pipeline emits Siril 1.4 script syntax (e.g. rgbcomp -lum=/-out=, which older Siril silently
// ignores), so anything below this is rejected up-front instead of failing mid-run.
const (
	minSirilMajor = 1
	minSirilMinor = 4
)

// Available reports whether the siril-cli binary can be found and executed, and is new enough.
func (r *Runner) Available(ctx context.Context) error {
	_, err := r.Version(ctx)
	return err
}

// Version returns the Siril version string ("1.4.3", or "1.4" when no patch level is printed),
// having already rejected anything below the 1.4 floor.
//
// It is split out of Available because the version is worth SHOWING, not merely gating on:
// `astrostack doctor` and /api/environment both name it, and in the container it is the one number
// that says which of the two Siril install paths — the pinned x86_64 AppImage or the arm64 distro
// package — the image was built through.
func (r *Runner) Version(ctx context.Context) (string, error) {
	if r.bin == "" {
		return "", fmt.Errorf("siril binary path is empty (set SIRIL_BIN)")
	}
	out, err := exec.CommandContext(ctx, r.bin, "--version").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("run %s --version: %w", r.bin, err)
	}
	if !strings.Contains(strings.ToLower(string(out)), "siril") {
		return "", fmt.Errorf("%s did not report a Siril version", r.bin)
	}
	m := versionRe.FindStringSubmatch(string(out))
	if m == nil {
		return "", fmt.Errorf("could not parse a Siril version from %q", strings.TrimSpace(string(out)))
	}
	major, _ := strconv.Atoi(m[1])
	minor, _ := strconv.Atoi(m[2])
	if major < minSirilMajor || (major == minSirilMajor && minor < minSirilMinor) {
		return "", fmt.Errorf("siril %d.%d is too old: the pipeline needs >= %d.%d — it emits 1.4 script "+
			"syntax (e.g. `rgbcomp -lum=/-out=`) that older Siril silently ignores; install Siril 1.4.x",
			major, minor, minSirilMajor, minSirilMinor)
	}
	// Prefer the full dotted version Siril printed over the two components the floor check parsed:
	// the patch level is exactly what distinguishes the arm64 distro build from the pinned AppImage.
	if full := fullVersionRe.FindStringSubmatch(string(out)); full != nil {
		return full[1], nil
	}
	return fmt.Sprintf("%d.%d", major, minor), nil
}

// Run writes script to workDir and executes it with `siril-cli -d workDir -s script`, streaming
// each output line to onProgress (may be nil) and returning the full log. A non-zero Siril exit
// is returned as an error (with the log attached for context).
func (r *Runner) Run(ctx context.Context, workDir, script string, onProgress func(Progress)) (*Result, error) {
	// Siril changes its CWD to -d, so both the script path and any -out paths must be absolute.
	absWork, err := filepath.Abs(workDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(absWork, 0o755); err != nil {
		return nil, fmt.Errorf("create work dir: %w", err)
	}
	scriptPath := filepath.Join(absWork, "_astrostack.ssf")
	if err := os.WriteFile(scriptPath, []byte(r.lim.apply(script)), 0o644); err != nil {
		return nil, fmt.Errorf("write script: %w", err)
	}

	cmd := exec.CommandContext(ctx, r.bin, "-d", absWork, "-s", scriptPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout // fold stderr into the same stream
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start siril: %w", err)
	}

	// emit serializes callbacks: the resource monitor runs on its own goroutine, so without this
	// lock its samples would race the stdout scan loop's log lines into a single onProgress.
	var emitMu sync.Mutex
	emit := func(p Progress) {
		if onProgress == nil {
			return
		}
		emitMu.Lock()
		defer emitMu.Unlock()
		onProgress(p)
	}

	// Keep the host responsive and report this step's live CPU/RAM. Both hang off the child PID.
	setPriority(cmd.Process.Pid, r.lim.Nice)
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
		clean := logPrefix.ReplaceAllString(line, "")
		emit(Progress{Line: clean, Percent: parsePercent(clean)})
	}

	res := &Result{Log: log.String()}
	if err := cmd.Wait(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			res.ExitCode = exitErr.ExitCode()
		}
		// Fold Siril's own error line(s) into the error so callers (and the job UI) see the real cause
		// ("Error opening image …: file not found") instead of a bare "exit status 1".
		if hint := sirilErrorHint(res.Log); hint != "" {
			return res, fmt.Errorf("siril script failed (exit %d): %s", res.ExitCode, hint)
		}
		return res, fmt.Errorf("siril script failed (exit %d): %w", res.ExitCode, err)
	}
	return res, nil
}

// sirilErrorHint pulls the real cause out of a Siril log. On a script failure Siril prints the
// cause line(s), then a locator ("Error in line N: '<cmd>'."), then "Script execution failed." —
// the hint is the informative lines right before the LAST locator plus the locator itself.
// Recovery noise ("Reading sequence failed: …", printed while Siril retries a sequence-name
// lookup) is skipped so it can't shadow the true error, which may carry no error keyword at all
// ("Provided filtering options do not allow at least two images to be processed"). Without a
// locator it falls back to keyword matching.
func sirilErrorHint(logText string) string {
	lines := cleanedLogLines(logText)
	if hint := hintAtLocator(lines); hint != "" {
		return hint
	}
	return hintByKeywords(lines)
}

func cleanedLogLines(logText string) []string {
	var out []string
	for _, ln := range strings.Split(logText, "\n") {
		if l := strings.TrimSpace(logPrefix.ReplaceAllString(ln, "")); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// causeLookback bounds how far above the locator the cause is searched for, so an old unrelated
// line can never be presented as the reason a later command failed.
const causeLookback = 8

// hintAtLocator returns up to 3 cause lines preceding the last "Error in line N" locator, plus the
// locator itself; "" when the log has no locator.
func hintAtLocator(lines []string) string {
	last := -1
	for i, l := range lines {
		if errLocatorRe.MatchString(l) {
			last = i
		}
	}
	if last == -1 {
		return ""
	}
	var cause []string
	for i := last - 1; i >= 0 && i >= last-causeLookback && len(cause) < 3; i-- {
		if isSirilNoise(lines[i]) || errLocatorRe.MatchString(lines[i]) {
			continue
		}
		cause = append([]string{lines[i]}, cause...)
	}
	return strings.Join(append(cause, lines[last]), "; ")
}

var errorKeywords = []string{"error", "not found", "failed", "do not allow", "cannot", "could not",
	"invalid", "not enough", "no space", "abort"}

// hintByKeywords is the fallback when Siril produced no locator (e.g. it crashed mid-run): the
// last few keyword-matching lines, preferring lines that are not known recovery noise.
func hintByKeywords(lines []string) string {
	var hits, noise []string
	for _, l := range lines {
		low := strings.ToLower(l)
		if !hasErrorKeyword(low) {
			continue
		}
		if isSirilNoise(l) {
			noise = append(noise, l)
			continue
		}
		hits = append(hits, l)
	}
	if len(hits) == 0 {
		hits = noise // last resort: better a noisy hint than "exit status 1"
	}
	if len(hits) > 3 {
		hits = hits[len(hits)-3:]
	}
	return strings.Join(hits, "; ")
}

func hasErrorKeyword(low string) bool {
	for _, kw := range errorKeywords {
		if strings.Contains(low, kw) {
			return true
		}
	}
	return false
}

// isSirilNoise reports log lines that look like errors but are routine recovery/boilerplate —
// "progress:" status lines (live Siril reports a failed script as "progress: Script execution
// failed., 100.00%", an outcome marker, never a cause) and success summaries whose counters
// contain the word "failed" ("Conversion succeeded, … 0 failed" shadowed a real disk-full cause).
func isSirilNoise(l string) bool {
	low := strings.ToLower(l)
	return strings.HasPrefix(low, "progress:") ||
		strings.HasPrefix(low, "reading sequence failed") ||
		strings.HasPrefix(low, "searching for sequences") ||
		strings.HasPrefix(low, "finalizing sequence processing failed") || // generic wrapper, cause precedes it
		strings.Contains(low, "succeeded") ||
		strings.Contains(low, "script execution failed")
}

func parsePercent(line string) int {
	if m := percentRe.FindStringSubmatch(line); m != nil {
		n := 0
		for _, c := range m[1] {
			n = n*10 + int(c-'0')
		}
		if n > 100 {
			return -1
		}
		return n
	}
	return -1
}

// apply injects Siril's setcpu/setmem throttle commands into a script, right after the leading
// `requires` line (Siril requires that directive first). A zero-valued Limits, or a script without a
// `requires` line (e.g. an external eval_ssf snippet), is returned unchanged rather than risk an
// invalid command order — throttling is best-effort and must never produce a broken script.
func (l Limits) apply(script string) string {
	header := l.header()
	if header == "" {
		return script
	}
	if strings.HasPrefix(script, "requires ") {
		if i := strings.IndexByte(script, '\n'); i >= 0 {
			return script[:i+1] + header + script[i+1:]
		}
	}
	return script
}

// header builds the setcpu/setmem lines for the configured limits (empty when neither is set).
func (l Limits) header() string {
	var b strings.Builder
	if l.MaxCPUs > 0 {
		fmt.Fprintf(&b, "setcpu %d\n", l.MaxCPUs)
	}
	if l.MemRatio > 0 {
		fmt.Fprintf(&b, "setmem %.2f\n", l.MemRatio)
	}
	return b.String()
}
