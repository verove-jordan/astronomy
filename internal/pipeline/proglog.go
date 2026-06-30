package pipeline

import (
	"strings"

	"github.com/verove-jordan/astronomy/internal/siril"
)

// sirilLines returns a Siril progress callback that forwards each non-empty log line to the
// pipeline's reporter under step, so live Siril output reaches the job log tail and SSE stream.
// Passing this (instead of nil) to Runner.Run is what makes a Siril failure diagnosable.
func (o Options) sirilLines(step string) func(siril.Progress) {
	return func(p siril.Progress) {
		if p.Line != "" || p.Sample != nil {
			o.report(Progress{Step: step, Line: p.Line, Sample: p.Sample})
		}
	}
}

// tailLog returns the last n lines of s (trailing newline trimmed); "" when s is empty.
func tailLog(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// sirilTail extracts the tail of a Siril run's log for error context (nil-safe), so a wrapped
// error carries the real Siril message rather than a bare "exit status 1".
func sirilTail(res *siril.Result) string {
	if res == nil {
		return ""
	}
	return tailLog(res.Log, 40)
}
