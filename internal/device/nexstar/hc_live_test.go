package nexstar

// The bench protocol, against a real Celestron hand controller.
//
// Nothing here runs in a normal suite. It is what you run with the mount powered, the hand
// controller booted past its splash screen, and its mini-USB socket cabled to the Mac:
//
//	# stages A–C: identity, an echo storm, and deliberate desynchronisation. Nothing moves.
//	ASTRO_TEST_MOUNT_LIVE=1 go test ./internal/device/nexstar -run TestLiveHandController -v
//
//	# stage D: adds tiny reversible moves and a jog that is deliberately left unrenewed.
//	ASTRO_TEST_MOUNT_LIVE=1 ASTRO_TEST_MOUNT_MOTION=1 go test ./internal/device/nexstar -run TestLiveHandController -v
//
//	# stage F: the endurance run. -timeout 0 because Go's default would kill it after ten minutes.
//	ASTRO_TEST_MOUNT_LIVE=1 ASTRO_TEST_MOUNT_SOAK=8h go test ./internal/device/nexstar \
//	    -run TestLiveHandControllerSoak -v -timeout 0
//
// `astrostack device` must not be running: macOS gives a serial port to one process at a time.
//
// A bench mount is not aligned, so GoTo, the alignment-gated paths and pier side cannot be proven
// here. The suite asserts the REFUSAL instead of pretending, and says so in its output.

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/device"
)

func liveMount(t *testing.T) *Mount {
	t.Helper()
	if os.Getenv("ASTRO_TEST_MOUNT_LIVE") == "" {
		t.Skip("set ASTRO_TEST_MOUNT_LIVE=1 to talk to a real hand controller")
	}
	path := os.Getenv("ASTRO_TEST_MOUNT_PORT")
	if path == "" {
		path = DefaultPort()
	}
	if path == "" {
		d := Diagnose(context.Background(), false)
		t.Skipf("no hand controller found:\n%s", d.String())
	}
	m := New(path, nil)
	if err := m.Connect(context.Background()); err != nil {
		d := Diagnose(context.Background(), false)
		t.Fatalf("could not connect on %s: %v\n%s", path, err, d.String())
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestLiveHandController(t *testing.T) {
	m := liveMount(t)
	ctx := context.Background()

	t.Run("stage B: identity", func(t *testing.T) {
		assert.NotEmpty(t, m.Model(), "the mount must name itself")
		assert.NotEmpty(t, m.Firmware())
		t.Logf("connected to %s, firmware %s, on %s", m.Model(), m.Firmware(), m.Path())

		st, err := m.State(ctx)
		require.NoError(t, err)
		t.Logf("RA %.4f°  Dec %.4f°  alt %.1f°  aligned=%v  tracking=%v (%s)  pier=%s",
			st.RADeg, st.DecDeg, st.AltDeg, st.Aligned, st.Tracking, st.TrackingRate, st.PierSide)
		if !st.Aligned {
			t.Log("the mount is not aligned — GoTo is expected to be refused, which stage B checks below")
		}
	})

	t.Run("stage B: an unaligned mount refuses to slew", func(t *testing.T) {
		st, err := m.State(ctx)
		require.NoError(t, err)
		if st.Aligned {
			t.Skip("this mount is aligned, so the refusal cannot be exercised")
		}
		err = m.GotoRADec(ctx, 83.8221, -5.3911) // the Orion Nebula
		require.Error(t, err)
		assert.True(t, err == ErrNotAligned || err == ErrAlignmentLost,
			"an unaligned GoTo must be refused before a single byte of it reaches the motors, got %v", err)
	})

	t.Run("stage B: five hundred echoes with nothing going wrong", func(t *testing.T) {
		before := m.Health()
		start := time.Now()
		for i := 0; i < 500; i++ {
			require.NoErrorf(t, m.Ping(ctx), "echo %d of 500", i+1)
		}
		h := m.Health()
		t.Logf("500 echoes in %s — p50 %dms  p99 %dms  max %dms",
			time.Since(start).Round(time.Millisecond), h.LatencyP50Ms, h.LatencyP99Ms, h.LatencyMaxMs)

		assert.Equal(t, before.Errors, h.Errors, "a healthy link answers five hundred echoes without one error")
		assert.Equal(t, before.Resyncs, h.Resyncs, "and without needing to be put back in step")
		assert.LessOrEqual(t, h.LatencyP99Ms, int64(soakP99CeilingMs))
	})

	t.Run("stage C: resynchronisation torture", func(t *testing.T) {
		// Deliberately abandon a reply — write a command and never read its answer — then prove the
		// next command still gets the right one. This is the failure that otherwise ends a night
		// silently, and the only way to be sure of the recovery is to cause it on purpose.
		for i := 0; i < 20; i++ {
			m.mu.Lock()
			_, werr := m.port.Write([]byte("e"))
			m.mu.Unlock()
			require.NoError(t, werr)
			time.Sleep(50 * time.Millisecond) // let the reply arrive and sit there unread

			// The first ping MUST report the desynchronisation rather than absorb it. Detection is the
			// whole point: a mis-attributed reply has no acceptable rate, so Ping repairs the stream and
			// then says so, and the counter it bumps is what fails a soak in the morning. A ping that
			// quietly returned nil here would leave the one detectable failure invisible.
			err := m.Ping(ctx)
			require.Errorf(t, err, "round %d: an abandoned reply must be DETECTED, not silently swallowed", i+1)
			assert.ErrorIs(t, err, ErrDesynchronised)

			// Having reported it, the link must be back in step immediately — no second round of
			// recovery, no lingering lag.
			require.NoErrorf(t, m.Ping(ctx), "round %d: the stream must be in step again", i+1)
			st, err := m.State(ctx)
			require.NoErrorf(t, err, "round %d", i+1)
			assert.NotZero(t, st.RADeg+st.DecDeg)
		}
		h := m.Health()
		t.Logf("detected and recovered from 20 abandoned replies — desyncs %d, resyncs %d, unrecovered %d",
			h.Desyncs, h.Resyncs, h.Unrecovered)
		assert.GreaterOrEqual(t, h.Desyncs, uint64(20), "every abandoned reply must have been caught")
		assert.Zero(t, h.Unrecovered)
	})

	t.Run("stage D: motion", func(t *testing.T) {
		if os.Getenv("ASTRO_TEST_MOUNT_MOTION") == "" {
			t.Skip("set ASTRO_TEST_MOUNT_MOTION=1 to let the test move the mount (watch it while it does)")
		}
		before, err := m.State(ctx)
		require.NoError(t, err)

		require.NoError(t, m.SetTracking(ctx, false, ""))
		require.NoError(t, m.SetTracking(ctx, true, "sidereal"))

		// Out and straight back, so the mount ends where it started whatever happens in between.
		require.NoError(t, m.Nudge(ctx, 10, 0))
		require.NoError(t, m.Nudge(ctx, -10, 0))
		after, err := m.State(ctx)
		require.NoError(t, err)
		t.Logf("nudged ±10\": RA %.5f° -> %.5f°", before.RADeg, after.RADeg)

		// The deadman: start the gentlest slew there is and then say nothing. Rate 1 does not even
		// override equatorial tracking, so this is the smallest real motion the mount can make.
		require.NoError(t, m.Jog(ctx, device.DirNorth, 1))
		require.Equal(t, 1, m.stopsPending())
		deadline := time.Now().Add(m.deadmanWindow() + 5*time.Second)
		for time.Now().Before(deadline) && m.stopsPending() > 0 {
			time.Sleep(200 * time.Millisecond)
			m.expireStops()
		}
		assert.Zero(t, m.stopsPending(), "an unrenewed jog must stop itself, or a closed browser tab walks the mount away")
	})

	t.Run("the night's evidence", func(t *testing.T) {
		h := m.Health()
		t.Logf("commands %d  errors %d  retries %d  resyncs %d  desyncs %d  reconnects %d  unrecovered %d",
			h.Commands, h.Errors, h.Retries, h.Resyncs, h.Desyncs, h.Reconnects, h.Unrecovered)
		// Desyncs are NOT asserted to be zero here: stage C created them on purpose. What must be zero
		// is anything the link could not put right.
		assert.Zero(t, h.Unrecovered)
		assert.Zero(t, h.Reconnects, "nothing here unplugs anything, so a reconnect would be a real fault")
	})
}

func TestLiveHandControllerSoak(t *testing.T) {
	if os.Getenv("ASTRO_TEST_MOUNT_LIVE") == "" {
		t.Skip("set ASTRO_TEST_MOUNT_LIVE=1 to talk to a real hand controller")
	}
	spec := os.Getenv("ASTRO_TEST_MOUNT_SOAK")
	if spec == "" {
		t.Skip("set ASTRO_TEST_MOUNT_SOAK=8h (or 20m for a pre-flight) to run the endurance test")
	}
	d, err := time.ParseDuration(spec)
	require.NoError(t, err, "ASTRO_TEST_MOUNT_SOAK must be a Go duration, e.g. 8h")

	m := liveMount(t)
	cfg := SoakConfig{Duration: d, Progress: testWriter{t}}
	if os.Getenv("ASTRO_TEST_MOUNT_MOTION") != "" {
		cfg.NudgeInterval, cfg.NudgeArcsec, cfg.JogCheckInterval = 10*time.Minute, 10, time.Hour
	}
	if v := os.Getenv("ASTRO_TEST_MOUNT_ALLOW_RECONNECTS"); v != "" {
		// The unplug drill: pull the cable during the run and plug it into a different socket.
		cfg.AllowReconnects = len(v)
	}

	r, err := Soak(context.Background(), m, cfg)
	require.NoError(t, err)
	r.JudgeReconnects(cfg.AllowReconnects)
	r.JudgeCoverage(cfg)

	t.Logf("\n%s", r.String())
	assert.True(t, r.Pass(), "the soak did not meet its thresholds")
}

// testWriter routes the soak's progress lines into the test log, so an overnight `go test -v` shows
// it is alive rather than looking hung for eight hours.
type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Logf("%s", p)
	return len(p), nil
}
