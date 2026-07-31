package nexstar

import (
	"context"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A soak that cannot fail proves nothing, so these run the real runner against a link that is
// broken on purpose and assert it says so — in words that name the number and the limit.

// soakRig builds a connected driver on real time (the soak measures wall-clock, so its clock is not
// injected) over a port whose faults the test controls.
func soakRig(t *testing.T) (*Mount, *faultyPort) {
	t.Helper()
	fp := newFaultyPort(newFakeHC())
	m := New("/dev/fake", func(string) (Port, error) { return fp, nil })
	m.now = func() time.Time { return time.Date(2026, 7, 30, 22, 0, 0, 0, time.UTC) }
	m.rnd = rand.New(rand.NewSource(17))
	m.sleep = func(time.Duration) {}
	m.candidates = func() []PortInfo { return nil }
	// A real reply timeout is Celestron's 3.5 s, and a test that injects a dozen dropped replies would
	// then take a minute. The timeout is what is shortened, not the logic: every path still runs.
	m.timeout = 40 * time.Millisecond
	require.NoError(t, m.Connect(context.Background()))
	t.Cleanup(func() { _ = m.Close() })
	return m, fp
}

func shortSoak() SoakConfig {
	return SoakConfig{
		Duration:         400 * time.Millisecond,
		PollInterval:     10 * time.Millisecond,
		PingInterval:     20 * time.Millisecond,
		IdentityInterval: 50 * time.Millisecond,
	}
}

func TestSoak_PassesOnAHealthyLink(t *testing.T) {
	m, _ := soakRig(t)

	cfg := shortSoak()
	r, err := Soak(context.Background(), m, cfg)
	require.NoError(t, err)
	r.JudgeReconnects(0)
	r.JudgeCoverage(cfg)

	assert.True(t, r.Pass(), "a healthy link must pass: %s", strings.Join(r.Failures, "; "))
	assert.Positive(t, r.Polls)
	assert.Positive(t, r.Pings)
	assert.Equal(t, "Advanced VX", r.Model)
	assert.Contains(t, r.String(), "PASS")
}

func TestSoak_FailsOnTheFaultThatBrokeTheNight(t *testing.T) {
	tests := []struct {
		name   string
		break_ func(m *Mount, fp *faultyPort)
		// want lists the failure lines that would be a correct diagnosis. More than one is allowed
		// because a mount that stops answering shows up first as a read that failed and then, once the
		// resynchronisation echo goes unanswered too, as a stream that could not be put back in step.
		want []string
	}{
		{
			// A mount that stops answering entirely: the link is open, the hand controller is not
			// talking. Both the poll and its one retry fail, which is what "unrecovered" means.
			name: "the mount stops answering",
			break_: func(_ *Mount, fp *faultyPort) {
				many := make([]fault, 200)
				for i := range many {
					many[i] = fault{drop: true}
				}
				fp.inject(many...)
			},
			want: []string{"state reads failed", "wrong command", "resynchronisation"},
		},
		{
			// The cable goes and nothing can be reopened. Every command after that is unrecovered.
			name:   "the adapter is unplugged",
			break_: func(_ *Mount, fp *faultyPort) { fp.kill() },
			want:   []string{"state reads failed", "neither a resynchronisation nor a reconnect"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, fp := soakRig(t)
			tt.break_(m, fp)

			cfg := shortSoak()
			r, err := Soak(context.Background(), m, cfg)
			require.NoError(t, err, "a failing link is a report, not an error — the numbers are the point")
			r.JudgeReconnects(0)

			require.False(t, r.Pass())
			joined := strings.Join(r.Failures, "\n")
			matched := false
			for _, w := range tt.want {
				if strings.Contains(joined, w) {
					matched = true
					break
				}
			}
			assert.True(t, matched, "no expected diagnosis in:\n%s", joined)
			assert.Contains(t, r.String(), "FAIL")
			for _, f := range r.Failures {
				assert.Contains(t, f, "limit", "a failure line must name the limit it breached")
			}
		})
	}
}

func TestSoak_FailsWhenItQuietlyStoppedPolling(t *testing.T) {
	m, _ := soakRig(t)

	// A run with no failures but almost no work must not pass: that is what a poller that died at
	// midnight looks like from the report alone.
	r := SoakReport{Started: time.Now().Add(-8 * time.Hour), Finished: time.Now(), Polls: 12}
	r.JudgeCoverage(SoakConfig{PollInterval: 2 * time.Second})

	require.False(t, r.Pass())
	assert.Contains(t, strings.Join(r.Failures, "\n"), "stopped polling")
	_ = m
}

func TestSoak_CountsAReconnectAgainstTheRunUnlessItWasAsked(t *testing.T) {
	r := SoakReport{Link: LinkHealth{Reconnects: 1}}
	r.JudgeReconnects(0)
	require.False(t, r.Pass())
	assert.Contains(t, strings.Join(r.Failures, "\n"), "any reconnect is a fault")

	// The unplug drill deliberately causes one, and then it is data rather than a defect.
	drill := SoakReport{Link: LinkHealth{Reconnects: 1}}
	drill.JudgeReconnects(1)
	assert.True(t, drill.Pass())
}

func TestSoak_RefusesToRunWithoutAMount(t *testing.T) {
	m := New("/dev/fake", func(string) (Port, error) { return newFaultyPort(newFakeHC()), nil })
	_, err := Soak(context.Background(), m, shortSoak())
	require.Error(t, err, "an unconnected soak would report a perfect night from no evidence")
}

func TestSoak_StopsWhenTheContextIsCancelled(t *testing.T) {
	m, _ := soakRig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()

	start := time.Now()
	r, err := Soak(ctx, m, SoakConfig{Duration: time.Hour, PollInterval: 10 * time.Millisecond})
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 10*time.Second, "an interrupted soak must return, not run its full hour")
	assert.Contains(t, strings.Join(r.Notes, "\n"), "cancelled")
}

func TestSoak_ReportJSONRoundTrips(t *testing.T) {
	m, _ := soakRig(t)
	r, err := Soak(context.Background(), m, SoakConfig{Duration: 50 * time.Millisecond, PollInterval: 10 * time.Millisecond})
	require.NoError(t, err)

	b, err := r.JSON()
	require.NoError(t, err)
	// The morning after a bad night, the text says what broke and the JSON says everything else.
	assert.Contains(t, string(b), `"link"`)
	assert.Contains(t, string(b), `"latency_p99_ms"`)
}
