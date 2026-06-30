package sysmon

import (
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// procStat is one row of `ps` output: a process, its parent, resident memory and cumulative CPU.
type procStat struct {
	ppid   int
	rssKiB int64
	cpu    time.Duration
}

// sampleTree returns the aggregate cumulative CPU-time and resident memory (bytes) of the process
// subtree rooted at pid. A vanished root (already exited) yields zero values and no error. ctx makes
// the ps call cancellable, so a hung ps can't wedge the monitor goroutine (and thus Monitor.Stop).
func sampleTree(ctx context.Context, pid int) (cpu time.Duration, rssBytes int64, err error) {
	// Suppressed headers (trailing `=`) keep the output as bare, space-separated columns.
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,rss=,time=").Output()
	if err != nil {
		return 0, 0, err
	}
	procs, children := parsePS(string(out))
	cpu, rssKiB := sumTree(pid, procs, children)
	return cpu, rssKiB * 1024, nil
}

// parsePS turns raw `ps -axo pid=,ppid=,rss=,time=` output into a pid→stat map and a ppid→children
// adjacency list. Malformed rows are skipped.
func parsePS(out string) (map[int]procStat, map[int][]int) {
	procs := map[int]procStat{}
	children := map[int][]int{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		rss, err := strconv.ParseInt(fields[2], 10, 64)
		if err != nil {
			continue
		}
		procs[pid] = procStat{ppid: ppid, rssKiB: rss, cpu: parseCPUTime(fields[3])}
		children[ppid] = append(children[ppid], pid)
	}
	return procs, children
}

// sumTree walks the subtree rooted at root (breadth-first, cycle-safe) and totals CPU-time and
// resident memory (KiB).
func sumTree(root int, procs map[int]procStat, children map[int][]int) (cpu time.Duration, rssKiB int64) {
	visited := map[int]bool{}
	queue := []int{root}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if visited[pid] {
			continue
		}
		visited[pid] = true
		p, ok := procs[pid]
		if !ok {
			continue
		}
		cpu += p.cpu
		rssKiB += p.rssKiB
		queue = append(queue, children[pid]...)
	}
	return cpu, rssKiB
}

// parseCPUTime parses a `ps`-style cumulative CPU duration: "MM:SS.ss", "HH:MM:SS" or
// "DD-HH:MM:SS". An unparseable value yields 0.
func parseCPUTime(s string) time.Duration {
	days := 0
	if i := strings.IndexByte(s, '-'); i >= 0 {
		if d, err := strconv.Atoi(s[:i]); err == nil {
			days = d
		}
		s = s[i+1:]
	}
	parts := strings.Split(s, ":")
	secs := float64(days) * 86400
	mult := 1.0
	for i := len(parts) - 1; i >= 0; i-- {
		v, err := strconv.ParseFloat(parts[i], 64)
		if err != nil {
			return 0
		}
		secs += v * mult
		mult *= 60
	}
	return time.Duration(secs * float64(time.Second))
}
