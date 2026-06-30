package siril

import "testing"

func TestLimitsApply(t *testing.T) {
	script := "requires 1.2.0\nsetext fits\nstack r_ rej 3 3\n"

	got := Limits{MaxCPUs: 10, MemRatio: 0.5}.apply(script)
	want := "requires 1.2.0\nsetcpu 10\nsetmem 0.50\nsetext fits\nstack r_ rej 3 3\n"
	if got != want {
		t.Errorf("apply with limits:\n got %q\nwant %q", got, want)
	}

	if got := (Limits{}).apply(script); got != script {
		t.Errorf("apply with zero limits changed the script: %q", got)
	}

	// A script without a leading `requires` is left untouched (can't safely place the throttle).
	if got := (Limits{MaxCPUs: 8}).apply("load light\n"); got != "load light\n" {
		t.Errorf("apply without requires should be unchanged: got %q", got)
	}
}
