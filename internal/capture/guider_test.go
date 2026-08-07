package capture

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/verove-jordan/astronomy/internal/fits/fitstest"
	"github.com/verove-jordan/astronomy/internal/guide"
)

// pulseLog records what a guider asked the mount to do, and — when a test needs the loop to be
// physically consistent — remembers the effect those corrections had.
//
// That second job matters more than it looks. A test whose frames ignore the corrections is testing a
// mount that does not respond, and the drift fit would then be regressing a trajectory no real mount
// could produce. Anything it concluded would be an artefact.
type pulseLog struct {
	mu     sync.Mutex
	pulses []pulseRecord
	fail   bool
	// movedPx is the net displacement the pulses have produced, at the test's 2″ per pixel.
	movedPx float64
}

// starAt is where the star lands after n steps of drift, given what the guider has corrected so far.
func (p *pulseLog) starAt(originPx, driftPxPerStep, steps float64) float64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return originPx + driftPxPerStep*steps + p.movedPx
}

type pulseRecord struct {
	RAArcsec         float64 `json:"ra_arcsec"`
	DecArcsec        float64 `json:"dec_arcsec"`
	RateArcsecPerSec float64 `json:"rate_arcsec_per_sec"`
}

func (p *pulseLog) all() []pulseRecord {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]pulseRecord(nil), p.pulses...)
}

func (p *pulseLog) totalRA() float64 {
	var sum float64
	for _, r := range p.all() {
		sum += r.RAArcsec
	}
	return sum
}

// fakeMountServer stands in for the device server, so a guider test asserts on exactly what reached the
// mount without a simulator in the way.
func fakeMountServer(t *testing.T) (*Client, *pulseLog) {
	t.Helper()
	log := &pulseLog{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mount/guide" {
			http.Error(w, `{"error":"unexpected"}`, http.StatusNotFound)
			return
		}
		var rec pulseRecord
		_ = json.NewDecoder(r.Body).Decode(&rec)
		log.mu.Lock()
		log.pulses = append(log.pulses, rec)
		shouldFail := log.fail
		if !shouldFail {
			// A correction of −6″ moves the star three pixels back at 2″ per pixel.
			log.movedPx += rec.RAArcsec / 2
		}
		log.mu.Unlock()
		if shouldFail {
			http.Error(w, `{"error":"serial link died","code":"io"}`, http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"applied":{}}`))
	}))
	t.Cleanup(ts.Close)
	return NewClient(strings.TrimPrefix(ts.URL, "http://")), log
}

// starFrame writes a FITS file with one Gaussian star at (cx, cy).
func starFrame(t *testing.T, dir, name string, cx, cy float64) string {
	t.Helper()
	const (
		w, h  = 96, 96
		sky   = 800
		peak  = 20000
		sigma = 2.0
	)
	// Deterministic noise, so the background has a real spread. A perfectly noiseless frame gives the
	// detector a MAD of zero, which is a degenerate case no camera produces and no test should rely on.
	rng := rand.New(rand.NewSource(1))
	pix := make([]uint16, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			v := sky + peak*math.Exp(-(dx*dx+dy*dy)/(2*sigma*sigma)) + rng.NormFloat64()*12
			pix[y*w+x] = uint16(math.Max(0, math.Min(v, 65535)))
		}
	}
	return fitstest.WritePixels(t, dir, name, w, h, pix, nil)
}

// guiderFor builds a self-guider over a 2″/px square calibration.
func guiderFor(t *testing.T, client *Client, opts GuiderOptions) *Guider {
	t.Helper()
	cal := guide.Calibration{
		RAArcsecPerPx: 2, DecArcsecPerPx: 2,
		RAUnitX: 1, RAUnitY: 0,
		DecUnitX: 0, DecUnitY: 1,
		Orthogonality: 1,
	}
	cfg := guide.DefaultConfig(guide.ModeSelfGuide)
	cfg.RA = guide.AxisConfig{Aggressiveness: 1, MinMoveArcsec: 0.1, MaxMoveArcsec: 1000}
	cfg.Dec = cfg.RA
	s, err := guide.NewSession(cfg, cal)
	require.NoError(t, err)
	if opts.SettleSec == 0 {
		opts.SettleSec = 0.001 // tests should not sit through a real settle
	}
	return NewGuider(client, s, opts)
}

func TestNewGuider_NilWithoutItsDependencies(t *testing.T) {
	client, _ := fakeMountServer(t)
	s, err := guide.NewSession(guide.DefaultConfig(guide.ModeOff), guide.Calibration{})
	require.NoError(t, err)

	assert.Nil(t, NewGuider(nil, s, GuiderOptions{}))
	assert.Nil(t, NewGuider(client, nil, GuiderOptions{}))

	// And a nil guider must be safe to use, so the sequencer can attach one unconditionally.
	var g *Guider
	assert.NotPanics(t, func() {
		g.Observe(context.Background(), "nowhere.fits", time.Now())
		g.Compensate(context.Background(), time.Second)()
		_, ok := g.Stats()
		assert.False(t, ok)
	})
}

func TestGuider_CorrectsTheMeasuredOffset(t *testing.T) {
	dir := t.TempDir()
	client, log := fakeMountServer(t)
	g := guiderFor(t, client, GuiderOptions{RateArcsecPerSec: 8})
	base := time.Now()

	// The first frame establishes the reference and commands nothing.
	g.Observe(context.Background(), starFrame(t, dir, "a.fits", 48, 48), base)
	assert.Empty(t, log.all(), "the first frame is the reference, not an error")

	// Three pixels right at 2″ per pixel is a 6″ RA error, so it must be undone with −6″.
	g.Observe(context.Background(), starFrame(t, dir, "b.fits", 51, 48), base.Add(time.Minute))

	pulses := log.all()
	require.Len(t, pulses, 1)
	assert.InDelta(t, -6, pulses[0].RAArcsec, 0.3)
	assert.InDelta(t, 0, pulses[0].DecArcsec, 0.3)
	assert.InDelta(t, 8, pulses[0].RateArcsecPerSec, 1e-9)
}

func TestGuider_CorrectsBothAxes(t *testing.T) {
	dir := t.TempDir()
	client, log := fakeMountServer(t)
	g := guiderFor(t, client, GuiderOptions{})
	base := time.Now()

	g.Observe(context.Background(), starFrame(t, dir, "a.fits", 48, 48), base)
	g.Observe(context.Background(), starFrame(t, dir, "b.fits", 46, 50), base.Add(time.Minute))

	pulses := log.all()
	require.Len(t, pulses, 1)
	assert.InDelta(t, 4, pulses[0].RAArcsec, 0.3, "two pixels left is a −4″ error, corrected with +4″")
	assert.InDelta(t, -4, pulses[0].DecArcsec, 0.3)
}

func TestGuider_ASteadyStarIsLeftAlone(t *testing.T) {
	dir := t.TempDir()
	client, log := fakeMountServer(t)
	g := guiderFor(t, client, GuiderOptions{})
	base := time.Now()

	for i := 0; i < 4; i++ {
		g.Observe(context.Background(), starFrame(t, dir, fmt.Sprintf("f%d.fits", i), 48, 48),
			base.Add(time.Duration(i)*time.Minute))
	}

	assert.Empty(t, log.all(), "a mount that is tracking perfectly must not be nudged at all")
}

func TestGuider_SurvivesAFrameWithNoStar(t *testing.T) {
	dir := t.TempDir()
	client, log := fakeMountServer(t)
	g := guiderFor(t, client, GuiderOptions{})
	base := time.Now()

	g.Observe(context.Background(), starFrame(t, dir, "a.fits", 48, 48), base)
	// A flat grey frame: clouded out.
	blank := fitstest.Write(t, dir, "cloud.fits", 96, 96, 900, nil)
	g.Observe(context.Background(), blank, base.Add(time.Minute))

	assert.Empty(t, log.all(), "no star means no correction — never a guess at where it went")

	// And it picks up again afterwards, still against the original reference.
	g.Observe(context.Background(), starFrame(t, dir, "c.fits", 51, 48), base.Add(2*time.Minute))
	require.Len(t, log.all(), 1)
	assert.InDelta(t, -6, log.all()[0].RAArcsec, 0.3)
}

func TestGuider_SurvivesAnUnreadableFrame(t *testing.T) {
	client, log := fakeMountServer(t)
	g := guiderFor(t, client, GuiderOptions{})

	g.Observe(context.Background(), "/nonexistent/frame.fits", time.Now())

	assert.Empty(t, log.all())
	st, ok := g.Stats()
	require.True(t, ok)
	assert.Contains(t, st.LastError, "frame.fits", "the failure is recorded rather than swallowed silently")
}

func TestGuider_SurvivesAMountThatRefusesThePulse(t *testing.T) {
	dir := t.TempDir()
	client, log := fakeMountServer(t)
	log.fail = true
	g := guiderFor(t, client, GuiderOptions{})
	base := time.Now()

	g.Observe(context.Background(), starFrame(t, dir, "a.fits", 48, 48), base)
	// Losing a night's frames to a guiding problem would be a poor trade, so this must not panic or
	// propagate — only be recorded.
	assert.NotPanics(t, func() {
		g.Observe(context.Background(), starFrame(t, dir, "b.fits", 51, 48), base.Add(time.Minute))
	})

	st, _ := g.Stats()
	assert.Contains(t, st.LastError, "guide pulse")
	assert.Zero(t, st.Corrected, "a pulse that failed must not be counted as applied")
}

// driveFrames runs n frames one second apart, with the star drifting driftPxPerFrame each time and the
// guider's corrections really moving it. One second rather than one minute so a fitted rate is large
// enough per second for a sub-second test exposure to predict something worth acting on.
func driveFrames(t *testing.T, g *Guider, log *pulseLog, dir string, n int, driftPxPerFrame float64) time.Time {
	t.Helper()
	base := time.Now()
	for i := 0; i < n; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		x := log.starAt(48, driftPxPerFrame, float64(i))
		g.Observe(context.Background(), starFrame(t, dir, fmt.Sprintf("f%d.fits", i), x, 48), at)
	}
	return base
}

func TestGuider_FitsTheDriftRate(t *testing.T) {
	dir := t.TempDir()
	client, log := fakeMountServer(t)
	// Compensation off: this test is about the fit, not what is done with it.
	g := guiderFor(t, client, GuiderOptions{})

	// One pixel of drift per second is 2" per second at this scale. The guider cancels it each frame, so
	// the raw trajectory only reappears once the corrections are taken back out.
	driveFrames(t, g, log, dir, 8, 1)

	st, ok := g.Stats()
	require.True(t, ok)
	assert.InDelta(t, 2.0*60, st.DriftRAArcsecPerMin, 12,
		"the fit must describe the mount, not the servo that has been cancelling it")
	assert.InDelta(t, 0, st.DriftDecArcsecPerMin, 12)
	assert.GreaterOrEqual(t, st.FitSamples, 5)
}

func TestGuider_FitStaysCorrectWhileTheGuiderIsWorking(t *testing.T) {
	// The trap this guards: once corrections start landing, the MEASURED error stops growing. Regressing
	// that instead of the uncorrected trajectory would report a drift heading for zero — or, with the
	// correction added rather than subtracted, one of the wrong sign entirely.
	dir := t.TempDir()
	client, log := fakeMountServer(t)
	g := guiderFor(t, client, GuiderOptions{})

	driveFrames(t, g, log, dir, 10, 1)

	st, _ := g.Stats()
	assert.Positive(t, st.DriftRAArcsecPerMin, "a mount drifting east must not be reported as drifting west")
	assert.Positive(t, log.totalRA()*-1, "and the corrections must have been pushing back against it")
}

func TestGuider_CompensationWaitsForEnoughSamples(t *testing.T) {
	dir := t.TempDir()
	client, log := fakeMountServer(t)
	g := guiderFor(t, client, GuiderOptions{Compensate: true, MinFitSamples: 5})

	g.Observe(context.Background(), starFrame(t, dir, "a.fits", 48, 48), time.Now())
	before := len(log.all())

	stop := g.Compensate(context.Background(), 200*time.Millisecond)
	time.Sleep(250 * time.Millisecond)
	stop()

	assert.Len(t, log.all(), before, "one sample is not a drift rate; guessing would be worse than waiting")
	st, _ := g.Stats()
	assert.Zero(t, st.Compensated)
}

func TestGuider_CompensationCancelsThePredictedDrift(t *testing.T) {
	dir := t.TempDir()
	client, log := fakeMountServer(t)
	g := guiderFor(t, client, GuiderOptions{Compensate: true, MinFitSamples: 5})

	// Two pixels a second is 4" a second, so a one-second exposure should be given about 4" of
	// compensation, in the opposite direction.
	driveFrames(t, g, log, dir, 6, 2)
	recentring := log.totalRA()

	stop := g.Compensate(context.Background(), time.Second)
	time.Sleep(1300 * time.Millisecond)
	stop()

	train := log.totalRA() - recentring
	assert.InDelta(t, -4, train, 1.5, "the train must cancel the drift predicted for the exposure")

	st, _ := g.Stats()
	assert.Equal(t, 1, st.Compensated)
}

func TestGuider_CompensationIsSpreadAcrossTheExposure(t *testing.T) {
	dir := t.TempDir()
	client, log := fakeMountServer(t)
	g := guiderFor(t, client, GuiderOptions{Compensate: true, MinFitSamples: 5})

	driveFrames(t, g, log, dir, 6, 2)
	before := len(log.all())

	// A single lump at one instant would be a step in the middle of the frame; the point is to match the
	// drift as it happens, so it has to arrive in pieces.
	stop := g.Compensate(context.Background(), time.Second)
	time.Sleep(1300 * time.Millisecond)
	stop()

	assert.Greater(t, len(log.all())-before, 3, "the compensation must arrive as a train, not a lump")
}

func TestGuider_CompensationStopsWhenTheExposureDoes(t *testing.T) {
	dir := t.TempDir()
	client, log := fakeMountServer(t)
	g := guiderFor(t, client, GuiderOptions{Compensate: true, MinFitSamples: 5})

	driveFrames(t, g, log, dir, 6, 2)

	stop := g.Compensate(context.Background(), 10*time.Second)
	time.Sleep(200 * time.Millisecond)
	stop()
	settled := len(log.all())

	// An aborted exposure must not leave a train still pulsing into the next one.
	time.Sleep(400 * time.Millisecond)
	assert.Equal(t, settled, len(log.all()), "stopping must actually stop it")
}

func TestGuider_CompensationRefusesAnUnsteadyDrift(t *testing.T) {
	dir := t.TempDir()
	client, log := fakeMountServer(t)
	g := guiderFor(t, client, GuiderOptions{Compensate: true, MinFitSamples: 5})
	base := time.Now()

	// The star wanders instead of drifting: wind, bad seeing, a loose clutch. A line through this
	// predicts nothing, and acting on it would add exactly as much trailing as a good prediction removes.
	for i, x := range []float64{48, 54, 45, 53, 46, 52, 47} {
		g.Observe(context.Background(), starFrame(t, dir, fmt.Sprintf("w%d.fits", i), x, 48),
			base.Add(time.Duration(i)*time.Second))
	}
	before := len(log.all())

	stop := g.Compensate(context.Background(), time.Second)
	time.Sleep(300 * time.Millisecond)
	stop()

	assert.Len(t, log.all(), before, "an unfittable trajectory must fall back to feedback alone")
	st, _ := g.Stats()
	assert.Zero(t, st.Compensated)
}

func TestGuider_CompensationOffLeavesRecentringAlone(t *testing.T) {
	dir := t.TempDir()
	client, log := fakeMountServer(t)
	g := guiderFor(t, client, GuiderOptions{}) // Compensate not set

	driveFrames(t, g, log, dir, 6, 2)
	before := len(log.all())
	assert.Positive(t, before, "recentring between frames is worth having on its own")

	stop := g.Compensate(context.Background(), 200*time.Millisecond)
	time.Sleep(250 * time.Millisecond)
	stop()

	assert.Len(t, log.all(), before)
}

func TestIsLight_OnlyFramesWithSkyInThem(t *testing.T) {
	tests := []struct {
		stepType string
		want     bool
	}{
		{"light", true},
		{"LIGHT", true},
		{"", true},
		{"dark", false},
		{"flat", false},
		{"bias", false},
		{"darkflat", false},
	}
	for _, tt := range tests {
		t.Run(tt.stepType, func(t *testing.T) {
			assert.Equal(t, tt.want, isLight(tt.stepType))
		})
	}
}
