package graxpert

// A manual, env-gated live probe of the REAL host GraXpert (never runs in CI/normal suites):
//   ASTRO_TEST_GRAXPERT_LIVE=1 go test ./internal/graxpert -run TestLiveHostProbe -v
import (
	"context"
	"os"
	"testing"
	"time"
)

func TestLiveHostProbe(t *testing.T) {
	if os.Getenv("ASTRO_TEST_GRAXPERT_LIVE") == "" {
		t.Skip("set ASTRO_TEST_GRAXPERT_LIVE=1 to probe the real host GraXpert")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	start := time.Now()
	err := New("graxpert").Healthy(ctx)
	t.Logf("host graxpert Healthy: err=%v (took %s)", err, time.Since(start).Round(time.Second))
}
