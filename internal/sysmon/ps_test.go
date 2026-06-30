package sysmon

import (
	"testing"
	"time"
)

func TestParseCPUTime(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"0:00.00", 0},
		{"00:38.50", 38*time.Second + 500*time.Millisecond},
		{"1:02.00", 62 * time.Second},
		{"01:02:03", time.Hour + 2*time.Minute + 3*time.Second},
		{"2-03:04:05", 2*24*time.Hour + 3*time.Hour + 4*time.Minute + 5*time.Second},
		{"garbage", 0},
	}
	for _, c := range cases {
		if got := parseCPUTime(c.in); got != c.want {
			t.Errorf("parseCPUTime(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestParseAndSumTree(t *testing.T) {
	// 100 (root) → 200, 300; 300 → 400. 999 is unrelated. Header is suppressed by `ps -o name=`.
	out := `  100     1  1024  0:10.00
  200   100  2048  0:05.00
  300   100  4096  1:00.00
  400   300  8192  0:30.00
  999     1 16384  9:00.00
`
	procs, children := parsePS(out)
	if len(procs) != 5 {
		t.Fatalf("parsePS got %d procs, want 5", len(procs))
	}
	cpu, rssKiB := sumTree(100, procs, children)
	// 10 + 5 + 60 + 30 = 105s of CPU; 1024+2048+4096+8192 = 15360 KiB. 999 excluded.
	if want := 105 * time.Second; cpu != want {
		t.Errorf("subtree cpu = %v, want %v", cpu, want)
	}
	if want := int64(15360); rssKiB != want {
		t.Errorf("subtree rss = %d KiB, want %d", rssKiB, want)
	}
}

func TestSumTreeMissingRoot(t *testing.T) {
	procs, children := parsePS("100 1 1024 0:01.00\n")
	cpu, rss := sumTree(42, procs, children) // root not present (already exited)
	if cpu != 0 || rss != 0 {
		t.Errorf("missing root = (%v, %d), want (0, 0)", cpu, rss)
	}
}

func TestParsePSSkipsMalformed(t *testing.T) {
	procs, _ := parsePS("not a row\n100 1 1024 0:01.00\nx y z w\n")
	if len(procs) != 1 {
		t.Fatalf("parsePS kept %d rows, want 1 (malformed skipped)", len(procs))
	}
}
