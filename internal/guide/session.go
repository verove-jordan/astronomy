package guide

import (
	"fmt"
	"math"
)

// Session-level defaults.
const (
	// defaultMaxLostFrames is how many consecutive starless frames are tolerated before the reference
	// is declared untrustworthy. The periodic-error trainer uses 90 at roughly one frame a second;
	// the same order of magnitude is right here, and the policy is identical — keep the clock running,
	// keep looking where the star was, never move the mount to hunt for it.
	defaultMaxLostFrames = 90
	// defaultSettleArcsec is the error below which the star counts as settled after a dither or slew.
	defaultSettleArcsec = 1.0
	// defaultSettleSamples is how many consecutive settled samples end the settling phase. More than
	// one, because a star crossing the target on its way past is not a settled star.
	defaultSettleSamples = 3
	// defaultRunawayArcsec is the hard limit. Because the reference is set to wherever the star was at
	// start, the error begins near zero by construction, so an excursion this large is not a big slew
	// or a bad polar alignment — it is something wrong with the loop itself.
	defaultRunawayArcsec = 30.0
	// defaultRunawaySamples requires the excursion to persist, so one cosmic ray on the centroid
	// cannot stop a good session.
	defaultRunawaySamples = 3
	// defaultDivergenceFactor is how much the recent error may grow relative to the start of guiding
	// before the loop is presumed to be making things worse.
	defaultDivergenceFactor = 4.0
	// defaultDivergenceWindow is how many samples each side of that comparison averages over.
	defaultDivergenceWindow = 10
	// maxRetainedSamples bounds the in-memory history. An hour at one sample a second; samples are a
	// few dozen bytes each, so this is kilobytes, not megabytes. Frames are never retained here —
	// see the package's callers for that budget.
	maxRetainedSamples = 3600
)

// Config is the whole tuning of a guide session.
type Config struct {
	Mode Mode       `json:"mode"`
	RA   AxisConfig `json:"ra"`
	Dec  AxisConfig `json:"dec"`

	MaxLostFrames  int     `json:"max_lost_frames"`
	SettleArcsec   float64 `json:"settle_arcsec"`
	SettleSamples  int     `json:"settle_samples"`
	RunawayArcsec  float64 `json:"runaway_arcsec"`
	RunawaySamples int     `json:"runaway_samples"`

	DivergenceFactor float64 `json:"divergence_factor"`
	DivergenceWindow int     `json:"divergence_window"`
}

// DefaultConfig returns a usable session configuration for a mode.
func DefaultConfig(mode Mode) Config {
	c := Config{
		Mode:             mode,
		RA:               DefaultAxisConfig(AxisRA),
		Dec:              DefaultAxisConfig(AxisDec),
		MaxLostFrames:    defaultMaxLostFrames,
		SettleArcsec:     defaultSettleArcsec,
		SettleSamples:    defaultSettleSamples,
		RunawayArcsec:    defaultRunawayArcsec,
		RunawaySamples:   defaultRunawaySamples,
		DivergenceFactor: defaultDivergenceFactor,
		DivergenceWindow: defaultDivergenceWindow,
	}
	if mode == ModeSelfGuide {
		// One sample per sub, minutes apart. Carrying part of a correction across that gap smooths
		// nothing useful and only adds lag, and a handful of starless subs is already a long time —
		// so the lost-frame budget counts subs, not seconds.
		c.RA.Hysteresis = 0
		c.MaxLostFrames = 5
	}
	return c
}

func (c Config) withDefaults() Config {
	if c.MaxLostFrames <= 0 {
		c.MaxLostFrames = defaultMaxLostFrames
	}
	if c.SettleArcsec <= 0 {
		c.SettleArcsec = defaultSettleArcsec
	}
	if c.SettleSamples <= 0 {
		c.SettleSamples = defaultSettleSamples
	}
	if c.RunawayArcsec <= 0 {
		c.RunawayArcsec = defaultRunawayArcsec
	}
	if c.RunawaySamples <= 0 {
		c.RunawaySamples = defaultRunawaySamples
	}
	if c.DivergenceFactor <= 0 {
		c.DivergenceFactor = defaultDivergenceFactor
	}
	if c.DivergenceWindow <= 0 {
		c.DivergenceWindow = defaultDivergenceWindow
	}
	return c
}

// Observation is one frame's measurement, as the caller's star tracker reported it. The package takes
// plain numbers rather than a guidestar.Star so it stays free of image handling and can be tested
// with arithmetic alone.
type Observation struct {
	TSec float64
	// Found is false when no usable star was measured. Pass the observation anyway: the session needs
	// to count the gap, and silently skipping it would make cloud look like good guiding.
	Found bool
	X, Y  float64 // measured star position, pixels
	SNR   float64
	HFD   float64
	// DecDeg is where the mount is pointed now, used to re-scale the RA axis for declination. Leave it
	// at zero to keep the calibration's own declination.
	DecDeg float64
	// HasDec distinguishes "pointed at the celestial equator" from "not supplied".
	HasDec bool
}

// Metrics summarises a session. Errors are axis arcseconds; the pixel figures are derived through the
// declination axis scale, which is the sky scale, and are there for the UI rather than for control.
type Metrics struct {
	Phase   Phase `json:"phase"`
	Samples int   `json:"samples"`
	Valid   int   `json:"valid"`
	Lost    int   `json:"lost"`
	// LostRun is the current unbroken run of starless frames.
	LostRun     int `json:"lost_run"`
	Corrections int `json:"corrections"`
	// Suppressed counts samples where a correction was computed but withheld — deadband, direction
	// guard. A high count is not a fault; it is usually a well-behaved mount.
	Suppressed int `json:"suppressed"`
	Clamped    int `json:"clamped"`

	RMSRAArcsec    float64 `json:"rms_ra_arcsec"`
	RMSDecArcsec   float64 `json:"rms_dec_arcsec"`
	RMSTotalArcsec float64 `json:"rms_total_arcsec"`
	RMSTotalPx     float64 `json:"rms_total_px"`
	PeakArcsec     float64 `json:"peak_arcsec"`

	LastSNR float64 `json:"last_snr,omitempty"`
	LastHFD float64 `json:"last_hfd,omitempty"`

	// RetainedBytes is what the caller is holding in memory for this session — the frame ring, the
	// reference cutout. Reported here so the budget is visible in the UI instead of being a promise.
	RetainedBytes int64 `json:"retained_bytes"`
}

// Session runs the servo for one guiding run. It is stateful and not safe for concurrent use; the
// caller serialises calls to Update.
type Session struct {
	cfg Config
	cal Calibration

	ra  *Axis
	dec *Axis

	phase Phase

	refX, refY float64
	hasRef     bool

	samples []Sample

	// Running accumulators, reset when guiding (re)starts so the figures describe the current run
	// rather than including the settle.
	sumRA2, sumDec2 float64
	n               int
	peak            float64

	valid, lost, lostRun    int
	corrections, suppressed int
	clamped                 int
	settledRun              int
	runawayRun              int

	// Divergence baseline: the mean total error over the first window of samples after guiding began.
	baseline      float64
	baselineCount int
	recent        []float64

	retainedBytes int64
	lastSNR       float64
	lastHFD       float64
}

// NewSession builds a guide session. A calibration is required whenever the mode actually drives the
// mount: guiding on an unknown pixel→axis mapping is worse than not guiding, because the direction is
// a guess and half of the guesses make it worse.
func NewSession(cfg Config, cal Calibration) (*Session, error) {
	if !cfg.Mode.Valid() {
		return nil, fmt.Errorf("unknown guide mode %q", cfg.Mode)
	}
	if cfg.Mode.Guiding() && !cal.Valid() {
		return nil, ErrNotCalibrated
	}
	cfg = cfg.withDefaults()
	return &Session{
		cfg:   cfg,
		cal:   cal,
		ra:    NewAxis(AxisRA, cfg.RA),
		dec:   NewAxis(AxisDec, cfg.Dec),
		phase: PhaseIdle,
	}, nil
}

// Phase reports the current state.
func (s *Session) Phase() Phase { return s.phase }

// Calibration returns the mapping in force.
func (s *Session) Calibration() Calibration { return s.cal }

// Reference returns the position the star is being held at.
func (s *Session) Reference() (x, y float64, ok bool) { return s.refX, s.refY, s.hasRef }

// SetReference locks the position the star will be held at, and starts the settling phase. Called
// once with the first good measurement of a run, and again whenever the star is deliberately moved.
func (s *Session) SetReference(x, y float64) {
	s.refX, s.refY, s.hasRef = x, y, true
	s.ra.Reset()
	s.dec.Reset()
	s.phase = PhaseSettling
	s.settledRun = 0
	s.runawayRun = 0
	s.resetStats()
}

// Dither moves the reference by a pixel offset and re-enters settling.
//
// The mount is not commanded here. Shifting the target and letting the servo walk the star there is
// what makes a dither self-verifying: the loop already measures whether it arrived, so there is no
// open-loop nudge to be swallowed by backlash and believed anyway.
func (s *Session) Dither(dx, dy float64) {
	if !s.hasRef {
		return
	}
	s.refX += dx
	s.refY += dy
	s.ra.Reset()
	s.dec.Reset()
	s.phase = PhaseDithering
	s.settledRun = 0
}

// Settled reports whether the star is holding within the settle threshold. The sequencer waits on
// this before opening the shutter after a dither.
func (s *Session) Settled() bool { return s.phase == PhaseGuiding }

// Samples returns the retained history, oldest first. The slice is owned by the session; copy it
// before handing it outside.
func (s *Session) Samples() []Sample { return s.samples }

// SetRetainedBytes records what the caller is holding in memory for this session, so Metrics can
// report the figure the frame budget is judged against.
func (s *Session) SetRetainedBytes(n int64) { s.retainedBytes = n }

// Update folds in one observation and returns the sample, including the corrections to command.
//
// A non-nil error is terminal: the caller stops guiding, leaves the mount tracking, and reports it.
// The sample is still returned so the failure is visible in the history rather than being a gap.
func (s *Session) Update(obs Observation) (Sample, error) {
	sample := Sample{TSec: obs.TSec, SNR: obs.SNR, HFD: obs.HFD}

	if !obs.Found {
		s.lost++
		s.lostRun++
		s.record(sample)
		if s.phase == PhaseGuiding || s.phase == PhaseSettling || s.phase == PhaseDithering {
			s.phase = PhaseStarLost
		}
		if s.lostRun >= s.cfg.MaxLostFrames {
			s.phase = PhaseFailed
			return sample, fmt.Errorf("%w: %d consecutive frames", ErrStarLost, s.lostRun)
		}
		return sample, nil
	}

	// A star again after a gap: resume where we left off rather than re-referencing, because the
	// reference is what the whole run is measured against.
	if s.phase == PhaseStarLost {
		s.phase = PhaseSettling
		s.settledRun = 0
	}
	s.lostRun = 0
	s.lastSNR, s.lastHFD = obs.SNR, obs.HFD

	if !s.hasRef {
		s.SetReference(obs.X, obs.Y)
		sample.Valid = true
		// Counted as seen but not tallied into the statistics: by definition this frame has zero error,
		// and folding a guaranteed zero into the RMS would flatter every run by one sample.
		s.valid++
		s.record(sample)
		return sample, nil
	}

	sample.Valid = true
	sample.DX = obs.X - s.refX
	sample.DY = obs.Y - s.refY

	cal := s.cal
	if obs.HasDec {
		cal = cal.AtDec(obs.DecDeg)
	}
	raErr, decErr, ok := cal.Axes(sample.DX, sample.DY)
	switch {
	case ok:
		sample.RAErrArcsec, sample.DecErrArcsec = raErr, decErr
	case s.cfg.Mode.Guiding():
		// Steering on a mapping that cannot be inverted is the one case that must stop the run.
		s.phase = PhaseFailed
		s.record(sample)
		return sample, ErrCalibrationDegenerate
	default:
		// ModeOff is "watch, do not touch". Without a calibration there are no axis arcseconds to
		// report, but the pixel offsets are still worth recording — that is the whole point of running
		// uncalibrated: seeing how the mount behaves before trusting anything to steer it.
	}

	if s.cfg.Mode.Guiding() {
		sample.RACorrArcsec, sample.RAWhy = s.ra.Next(sample.RAErrArcsec)
		sample.DecCorrArcsec, sample.DecWhy = s.dec.Next(sample.DecErrArcsec)
	} else {
		sample.RAWhy, sample.DecWhy = WhyDeadband, WhyDeadband
	}

	s.tally(sample)
	s.record(sample)

	if err := s.guard(sample); err != nil {
		s.phase = PhaseFailed
		return sample, err
	}
	s.advancePhase(sample)
	return sample, nil
}

// tally folds one measured sample into the running statistics.
func (s *Session) tally(sample Sample) {
	s.valid++
	s.n++
	s.sumRA2 += sample.RAErrArcsec * sample.RAErrArcsec
	s.sumDec2 += sample.DecErrArcsec * sample.DecErrArcsec
	if t := sample.TotalErrArcsec(); t > s.peak {
		s.peak = t
	}
	for _, why := range [...]string{sample.RAWhy, sample.DecWhy} {
		switch why {
		case WhyApplied:
			s.corrections++
		case WhyClamped:
			s.corrections++
			s.clamped++
		case WhyDeadband, WhyResist, WhyInvalid:
			s.suppressed++
		}
	}
}

// guard is the safety net: a hard excursion limit that is always armed, plus a growth test that only
// runs once guiding has settled and a baseline exists.
func (s *Session) guard(sample Sample) error {
	total := sample.TotalErrArcsec()

	if total > s.cfg.RunawayArcsec {
		s.runawayRun++
		if s.runawayRun >= s.cfg.RunawaySamples {
			return fmt.Errorf("%w: error reached %.1f″ over %d samples; the calibration sign is the usual cause",
				ErrDiverging, total, s.runawayRun)
		}
	} else {
		s.runawayRun = 0
	}

	if s.phase != PhaseGuiding {
		return nil
	}
	s.recent = append(s.recent, total)
	if len(s.recent) > s.cfg.DivergenceWindow {
		s.recent = s.recent[1:]
	}
	if s.baselineCount < s.cfg.DivergenceWindow {
		s.baseline += total
		s.baselineCount++
		return nil
	}
	if len(s.recent) < s.cfg.DivergenceWindow {
		return nil
	}
	base := s.baseline / float64(s.baselineCount)
	// A baseline at or below the deadband carries no information — everything is a large multiple of
	// nearly nothing — so the growth test stays out of it and the hard limit above does the work.
	if base < s.cfg.RA.MinMoveArcsec {
		return nil
	}
	var sum float64
	for _, v := range s.recent {
		sum += v
	}
	if now := sum / float64(len(s.recent)); now > base*s.cfg.DivergenceFactor {
		return fmt.Errorf("%w: error grew from %.2f″ to %.2f″ under correction", ErrDiverging, base, now)
	}
	return nil
}

// advancePhase moves settling → guiding once the star has held still for long enough.
func (s *Session) advancePhase(sample Sample) {
	if s.phase != PhaseSettling && s.phase != PhaseDithering {
		return
	}
	if sample.TotalErrArcsec() <= s.cfg.SettleArcsec {
		s.settledRun++
		if s.settledRun >= s.cfg.SettleSamples {
			s.phase = PhaseGuiding
			// The statistics describe the guiding run, not the settle that preceded it — otherwise a
			// large dither recovery is averaged into the RMS forever.
			s.resetStats()
		}
		return
	}
	s.settledRun = 0
}

func (s *Session) resetStats() {
	s.sumRA2, s.sumDec2, s.n, s.peak = 0, 0, 0, 0
	s.baseline, s.baselineCount = 0, 0
	s.recent = s.recent[:0]
}

// record appends to the bounded history.
func (s *Session) record(sample Sample) {
	s.samples = append(s.samples, sample)
	if len(s.samples) > maxRetainedSamples {
		s.samples = s.samples[len(s.samples)-maxRetainedSamples:]
	}
}

// Metrics returns the current summary.
func (s *Session) Metrics() Metrics {
	m := Metrics{
		Phase:         s.phase,
		Samples:       s.valid + s.lost,
		Valid:         s.valid,
		Lost:          s.lost,
		LostRun:       s.lostRun,
		Corrections:   s.corrections,
		Suppressed:    s.suppressed,
		Clamped:       s.clamped,
		PeakArcsec:    s.peak,
		LastSNR:       s.lastSNR,
		LastHFD:       s.lastHFD,
		RetainedBytes: s.retainedBytes,
	}
	if s.n > 0 {
		m.RMSRAArcsec = math.Sqrt(s.sumRA2 / float64(s.n))
		m.RMSDecArcsec = math.Sqrt(s.sumDec2 / float64(s.n))
		m.RMSTotalArcsec = math.Hypot(m.RMSRAArcsec, m.RMSDecArcsec)
		if scale := s.cal.ScaleArcsecPerPx(); scale > 0 {
			m.RMSTotalPx = m.RMSTotalArcsec / scale
		}
	}
	return m
}
