package skypano

import (
	"os"
	"testing"
)

// The zz_* files are a development harness for one real session's panels, not part of the suite.
// They read absolute paths under the scratchpad and take minutes, so they stay off unless asked for:
//
//	ASTRO_PANO_HARNESS=1 go test ./internal/skypano/ -run TestZZ -v
const scratch = "/private/tmp/claude-501/-Users-jordanverove-projects-perso-astronomy/be7181f0-9673-4b1f-98a9-5ac0b0801742/scratchpad/"

func requireHarness(t *testing.T) {
	t.Helper()
	if os.Getenv("ASTRO_PANO_HARNESS") == "" {
		t.Skip("set ASTRO_PANO_HARNESS=1 to run the panorama harness")
	}
}
