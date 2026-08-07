package nexstar

// A manual probe of the REAL USB bus and any hand controller attached to this machine. It never
// runs in a normal suite — it is the thing you run at the telescope, or on a desk with the mount
// powered, when the question is "does this Mac see the hand controller at all".
//
//	ASTRO_TEST_MOUNT_LIVE=1 go test ./internal/device/nexstar -run TestLiveDoctor -v
//
// Add ASTRO_TEST_MOUNT_PROBE=1 to also OPEN each candidate port and ask the mount to identify
// itself. That is off by default because opening a serial port takes it exclusively on macOS, which
// would knock a running `astrostack device` off its mount mid-session.

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLiveDoctor(t *testing.T) {
	if os.Getenv("ASTRO_TEST_MOUNT_LIVE") == "" {
		t.Skip("set ASTRO_TEST_MOUNT_LIVE=1 to diagnose the real USB bus")
	}
	d := Diagnose(context.Background(), os.Getenv("ASTRO_TEST_MOUNT_PROBE") != "")
	t.Logf("\n%s", d.String())

	// The diagnosis itself must always be well formed, whatever is or is not plugged in — an empty
	// verdict would mean the doctor fell through its own decision table.
	assert.NotEmpty(t, d.Verdict)
	assert.NotEmpty(t, d.Detail)
}
