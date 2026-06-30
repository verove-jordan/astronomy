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
	percentRe = regexp.MustCompile(`(\d+)\s?%`)
	logPrefix = regexp.MustCompile(`^log:\s?`)
)

// Available reports whether the siril-cli binary can be found and executed.
func (r *Runner) Available(ctx context.Context) error {
	if r.bin == "" {
		return fmt.Errorf("siril binary path is empty (set SIRIL_BIN)")
	}
	out, err := exec.CommandContext(ctx, r.bin, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("run %s --version: %w", r.bin, err)
	}
	if !strings.Contains(strings.ToLower(string(out)), "siril") {
		return fmt.Errorf("%s did not report a Siril version", r.bin)
	}
	return nil
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
		return res, fmt.Errorf("siril script failed (exit %d): %w", res.ExitCode, err)
	}
	return res, nil
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
