package job

import (
	"reflect"
	"testing"
	"time"
)

// TestStaleWorkRuns pins the sweep's selection rules: only run_<stamp> names, only parseable
// stamps, only older than the grace window — everything else is left alone.
func TestStaleWorkRuns(t *testing.T) {
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.Local)
	names := []string{
		"run_20260717_161540", // 3h50 old → stale
		"run_20260716_230820", // yesterday → stale
		"run_20260717_190000", // 1h old → inside the grace window, kept
		"run_notastamp",       // unparseable → never touched
		"live_s3",             // not a run dir → never touched
		"cal_master_FLAT_L",   // not a run dir → never touched
	}
	got := staleWorkRuns(names, now, 2*time.Hour)
	want := []string{"run_20260717_161540", "run_20260716_230820"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("staleWorkRuns = %v, want %v", got, want)
	}
	if out := staleWorkRuns(names, now, 12*time.Hour); len(out) != 1 || out[0] != "run_20260716_230820" {
		t.Fatalf("a 12h grace should sweep only yesterday's run (20.9h old), got %v", out)
	}
}
