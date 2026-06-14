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
)

// Runner executes Siril scripts via the siril-cli binary.
type Runner struct {
	bin string
}

// New returns a Runner for the given siril-cli path.
func New(bin string) *Runner { return &Runner{bin: bin} }

// Progress is one line of Siril output, with any embedded percentage extracted.
type Progress struct {
	Line    string
	Percent int // -1 when the line carried no percentage
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
	if err := os.WriteFile(scriptPath, []byte(script), 0o644); err != nil {
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

	var log strings.Builder
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		log.WriteString(line)
		log.WriteByte('\n')
		clean := logPrefix.ReplaceAllString(line, "")
		if onProgress != nil {
			onProgress(Progress{Line: clean, Percent: parsePercent(clean)})
		}
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
