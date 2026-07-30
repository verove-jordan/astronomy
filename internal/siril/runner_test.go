package siril

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSirilErrorHint(t *testing.T) {
	tests := []struct{ name, log, want string }{
		{
			// The real M92 failure shape: the true cause carries no error keyword at all and sits
			// between recovery noise and the locator — the hint must be cause + locator, noise gone.
			"locator with keyword-less cause above recovery noise",
			"log: Searching for sequences...\n" +
				"log: Reading sequence failed: r_light.seq\n" +
				"log: Provided filtering options do not allow at least two images to be processed.\n" +
				"log: Error in line 12: 'stack r_pp_light_ rej g 0.3 0.05'.\n" +
				"log: Script execution failed.\n",
			"Provided filtering options do not allow at least two images to be processed.; " +
				"Error in line 12: 'stack r_pp_light_ rej g 0.3 0.05'.",
		},
		{
			"no locator falls back to keywords, prefers real lines over noise",
			"log: Reading sequence failed: r_light.seq\n" +
				"log: cannot open image light_00002.fits\n",
			"cannot open image light_00002.fits",
		},
		{
			"noise-only log still yields a hint as last resort",
			"log: Reading sequence failed: r_light.seq\n",
			"Reading sequence failed: r_light.seq",
		},
		{
			// Live-observed shape: no locator at all, cause + a progress status line.
			"progress status lines are never the cause",
			"log: Provided filtering options do not allow at least two images to be processed.\n" +
				"progress: Script execution failed., 100.00%\n",
			"Provided filtering options do not allow at least two images to be processed.",
		},
		{
			// Live-observed disk-full registration failure: the success summary's "0 failed"
			// counter must not shadow the real cause, and "Not enough …/aborted" must match.
			"disk-full cause beats the succeeded-summary false positive",
			"log: Conversion succeeded, 10 file(s) created for 10 input file(s) (10 image(s) converted, 0 failed)\n" +
				"log: Not enough free disk space to perform this operation: 237.4 MiB available for 625.3 MiB needed\n" +
				"log: Not enough space to save the output images, aborting\n" +
				"log: Registration aborted.\n" +
				"log: Finalizing sequence processing failed.\n" +
				"progress: Script execution failed., 100.00%\n",
			"Not enough free disk space to perform this operation: 237.4 MiB available for 625.3 MiB needed; " +
				"Not enough space to save the output images, aborting; Registration aborted.",
		},
		{
			"clean log yields nothing",
			"log: 10 images loaded\n",
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, sirilErrorHint(tt.log))
		})
	}
}

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
