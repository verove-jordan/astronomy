// Package sim is a complete simulated observatory: a camera, a filter wheel and a mount that behave
// like the real thing, sharing one simulated telescope state.
//
// It is not a stub. It renders REAL star fields from the bundled catalogue at whatever the simulated
// mount is pointing at, with seeing, defocus, sky glow, read noise and periodic error — so the focus
// meter, the plate-solve centring loop, the dither feedback and the whole capture sequencer can be
// developed and regression-tested end to end with no hardware attached. It also stays useful after
// the hardware arrives: it is the demo mode, and the only way to reproduce a night on demand.
package sim

import (
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/filters"
	"github.com/verove-jordan/astronomy/internal/polaralign"
)

// Config are the simulated observing conditions. The zero value is a reasonable clear night.
type Config struct {
	Seed          int64   // deterministic rendering; 0 → 1
	SeeingArcsec  float64 // FWHM of the atmospheric PSF; 0 → 2.5
	SkyMagPerAsec float64 // sky brightness, mag/arcsec²; 0 → 20.5 (suburban)
	ReadNoiseADU  float64 // 0 → 8
	FocusOffsetUm float64 // focuser distance from perfect focus; drives the blur circle
	// HotPixels and PEAmplitude take NEGATIVE to mean "none". Zero means "use the default", which
	// is the convention every other field here follows — but for these two, zero is also a value a
	// caller legitimately wants (a test needs a clean sensor, or no periodic error), so asking for
	// none has to be expressible. Getting this wrong is not cosmetic: 200 unrequested hot pixels
	// outshine the stars in a small frame.
	// FaintStarsPerDeg2 is the synthetic faint-star density (see faint.go). The bundled catalogue
	// stops near magnitude 9, which is far too sparse for Siril to plate-solve; this fills the field
	// in. Follows the same convention: <0 → none, 0 → the default density.
	FaintStarsPerDeg2 float64

	// WheelSlots is how many slots the simulated filter wheel has; 0 → 7, so a full narrowband
	// sequence (L R G B Ha OIII SII) can be exercised with no hardware — render.go carries per-filter
	// throughput for OIII and SII precisely for that.
	//
	// It is a DEVICE property, which is the point: a real wheel's slot count is fixed by the hardware,
	// and naming a slot cannot change it. Deriving the count from the names instead let a 7-filter
	// configuration turn a 5-slot wheel into a 7-slot one.
	WheelSlots int

	// FlatPanelADUPerSec > 0 covers the aperture with a flat panel of that brightness instead of
	// showing the sky. Zero means "no panel" — unlike the other fields, there is no sensible default
	// brightness, because a panel is either fitted or it is not.
	FlatPanelADUPerSec float64

	HotPixels int // <0 → none, 0 → 200
	// PEAmplitude is the peak-to-peak periodic error of the RA worm, as an error of the AXIS — the
	// physical primitive. What lands on the sensor is that times cos(dec), which is why a correction
	// measured in sky arcsec has to be divided by cos(dec) before it can be written to a mount.
	// Modelling it the other way round would let code that forgets that division still pass.
	// <0 → none, 0 → 12.
	PEAmplitude float64
	PEPeriodSec float64 // RA worm period; 0 → 478
	// PEHarmonics are further components at multiples of the worm frequency. A real worm is not a
	// pure sinusoid, and a simulator that pretends otherwise flatters any correction fitted to it —
	// in particular it hides how a correction behaves near the table's Nyquist limit.
	PEHarmonics []PEHarmonic
	// PEJitterArcsec is the part of the tracking error that does NOT repeat from one worm cycle to
	// the next, and which PEC therefore can never remove. Without it a simulated mount is perfectly
	// coherent, and the repeatability gate that decides whether PEC is worth writing at all never
	// gets exercised. <0 → none, 0 → a modest default.
	PEJitterArcsec float64
	// PECRateScale is the divisor that turns one table unit into a fraction of the sidereal rate,
	// exactly as a real motor controller's does. 0 → 1024, which is what current Celestrons use.
	//
	// It is configurable because a correction is a RATE: a test that compresses the worm period to
	// keep its runtime sane multiplies every rate by the same factor, and a table scaled for an
	// eight-minute worm cannot express the correction for a six-second one. Turning the scale down
	// gives such a mount a coarser, wider-ranging table — and, incidentally, proves nothing in the
	// pipeline has assumed 1024.
	PECRateScale float64
	// PECInvertSign makes the simulated motor controller apply the table the opposite way round,
	// modelling a firmware whose sign convention is not the one we assumed. This is not a hypothetical:
	// the table's sign is undocumented, and getting it backwards does not fail — the mount simply
	// tracks about twice as badly, quietly, all night. It exists so the verify-and-revert safety net
	// can be tested against the failure it is there to catch.
	PECInvertSign bool
	FocalMM       float64 // 0 → 740 (FC-100 DF)
	ApertureMM    float64 // 0 → 100
	PixelUm       float64 // 0 → 3.8
	SensorW       int     // 0 → 4656
	SensorH       int     // 0 → 3520
	StartRADeg    float64 // where the mount is parked at boot
	StartDecDeg   float64
	LatDeg        float64
	LonDeg        float64

	// SlewRateDegPerSec is how fast the simulated mount moves; 0 → 3 (an AVX-like slew). Tests
	// raise it so a cross-sky GoTo does not cost them 20 seconds of wall clock.
	SlewRateDegPerSec float64

	// SyntheticStars are extra stars injected into every frame, on top of the real catalogue. A
	// test that needs a guaranteed bright star in the field (focus metrics, centroiding) plants one
	// here instead of hoping the sky cooperates at the chosen pointing.
	SyntheticStars []SyntheticStar

	// PolarErrorAltArcmin and PolarErrorAzArcmin put the simulated mount's polar axis off the pole:
	// too high by the first, east of the pole's meridian by the second (as the azimuth adjuster's own
	// angle). Both default to zero, which is EXACTLY a no-op — a simulated observatory that was not
	// asked for a misalignment behaves precisely as it always did.
	//
	// This is what lets polar alignment from the camera be developed and demonstrated indoors. The
	// error is applied where the telescope's actual pointing is decided, so the mount reports it, the
	// camera draws it, and a measurement run against it recovers the numbers dialled in.
	PolarErrorAltArcmin float64
	PolarErrorAzArcmin  float64

	// DecBacklashArcsec is how far the declination axis turns, on REVERSING, before the load follows.
	// <0 → none, 0 → 4.
	//
	// It is directional on purpose, and that is the difference between a useful model and a decorative
	// one. Nudge applies a flat penalty to every small declination move, which is enough to give dither
	// feedback something to correct; a guider's problem is different and specifically about direction.
	// A servo that reverses on one noisy sample spends the whole take-up, gets nothing for it, and then
	// reverses back — so an autoguider is judged on how it handles the FIRST move after a reversal, and
	// a model that also penalised the ninth would let a guider that reverses constantly look fine.
	DecBacklashArcsec float64
}

// SyntheticStar is a test-injected point source.
type SyntheticStar struct {
	RADeg, DecDeg float64
	Mag           float64
}

// PEHarmonic is one periodic-error component at K times the worm frequency.
type PEHarmonic struct {
	K               int     // 1 is the worm itself
	AmplitudeArcsec float64 // peak-to-peak, as an axis error
	PhaseRad        float64
}

// simPECBins is how many bins the simulated mount's table holds — the same 88 every Celestron uses.
// Nothing outside the simulator may assume this number: it is reported through PECCaps precisely so
// the rest of the code has to read it.
const simPECBins = 88

// simPECRateScale is the default rate scale — what current Celestrons use. Config.PECRateScale
// overrides it.
const simPECRateScale = 1024

func (c Config) withDefaults() Config {
	out := c
	if out.Seed == 0 {
		out.Seed = 1
	}
	if out.SeeingArcsec <= 0 {
		out.SeeingArcsec = 2.5
	}
	if out.WheelSlots <= 0 {
		out.WheelSlots = len(filters.List())
	}
	if out.FaintStarsPerDeg2 == 0 {
		out.FaintStarsPerDeg2 = defaultFaintPerDeg2
	}
	if out.SkyMagPerAsec <= 0 {
		out.SkyMagPerAsec = 20.5
	}
	if out.ReadNoiseADU <= 0 {
		out.ReadNoiseADU = 8
	}
	switch {
	case out.HotPixels < 0:
		out.HotPixels = 0
	case out.HotPixels == 0:
		out.HotPixels = 200
	}
	switch {
	case out.PEAmplitude < 0:
		out.PEAmplitude = 0
	case out.PEAmplitude == 0:
		out.PEAmplitude = 12
	}
	switch {
	case out.DecBacklashArcsec < 0:
		out.DecBacklashArcsec = 0
	case out.DecBacklashArcsec == 0:
		out.DecBacklashArcsec = 4
	}
	switch {
	case out.PEJitterArcsec < 0:
		out.PEJitterArcsec = 0
	case out.PEJitterArcsec == 0:
		out.PEJitterArcsec = 1.2
	}
	if out.PEPeriodSec == 0 {
		out.PEPeriodSec = 478
	}
	if out.PECRateScale <= 0 {
		out.PECRateScale = simPECRateScale
	}
	if out.FocalMM <= 0 {
		out.FocalMM = 740
	}
	if out.ApertureMM <= 0 {
		out.ApertureMM = 100
	}
	if out.PixelUm <= 0 {
		out.PixelUm = 3.8
	}
	if out.SensorW <= 0 {
		out.SensorW = 4656
	}
	if out.SensorH <= 0 {
		out.SensorH = 3520
	}
	if out.StartDecDeg == 0 && out.StartRADeg == 0 {
		out.StartRADeg, out.StartDecDeg = 10.6847, 41.2687 // M31: something is always in frame
	}
	if out.LatDeg == 0 && out.LonDeg == 0 {
		out.LatDeg, out.LonDeg = 48.85, 2.35
	}
	if out.SlewRateDegPerSec <= 0 {
		out.SlewRateDegPerSec = 3
	}
	return out
}

// World is the shared simulated telescope: where it points, what filter is in the beam, how far the
// focuser is from focus, how cold the sensor is. The camera renders from it, the mount moves it and
// the wheel changes its filter — exactly the coupling real hardware has through the sky.
type World struct {
	mu  sync.Mutex
	cfg Config
	rng *rand.Rand

	raDeg, decDeg float64 // current pointing, J2000
	targetRA      float64
	targetDec     float64
	// The coordinates last COMMANDED (as opposed to where the mount actually landed). Sync needs
	// them: the correction to learn is "how far the real pointing is from what I asked for".
	cmdRA, cmdDec float64
	hasCmd        bool
	slewUntil     time.Time
	tracking      bool
	trackingRate  string
	aligned       bool

	// Declination backlash state. decGuideDir is the direction the axis was last driven in, and
	// decTakeUp is how much of the gear slack is still to be wound out before the load will follow.
	// Both are meaningless until a guide pulse has been issued, which is why zero is the right start.
	decGuideDir int
	decTakeUp   float64
	// guideRate is the configured autoguide rate as a fraction of sidereal. Seeded in NewWorld rather
	// than left to the zero value, because zero is a rate a caller can legitimately ask for: treating
	// it as "never set" made SetGuideRate(0) read back as the default.
	guideRate float64

	filterSlot  int
	filterNames []string
	wheelUntil  time.Time

	tempMilliC  int
	targetTempC float64
	coolerOn    bool
	cameraPADeg float64
	bootAt      time.Time

	// The worm only turns while the drive runs, so its phase is accumulated TRACKED time — not wall
	// time since boot. Stopping tracking to calibrate must pause the worm, not teleport it.
	wormAccum time.Duration
	wormFrom  time.Time
	// driftFrom is when the sky started sliding, i.e. when tracking was last switched off. It is a
	// separate concern from worm phase, and conflating the two was how toggling tracking used to
	// reset the periodic error.
	driftFrom time.Time

	// The mount's PEC table and its state. pecTable holds signed rate corrections, one per bin.
	// pecPlayFromWorm is the worm time playback started at: the correction is the integral from
	// there, so enabling playback mid-run adds nothing instantaneously.
	pecTable        []int8
	pecPlaying      bool
	pecIndexed      bool
	pecPlayFromWorm float64

	// clock is injectable so tests can drive exposure/slew/cooling timing deterministically.
	clock func() time.Time
}

// NewWorld builds a simulated observatory in its parked state.
func NewWorld(cfg Config) *World {
	c := cfg.withDefaults()
	w := &World{
		cfg:          c,
		rng:          rand.New(rand.NewSource(c.Seed)),
		raDeg:        c.StartRADeg,
		decDeg:       c.StartDecDeg,
		tracking:     true,
		trackingRate: "sidereal",
		aligned:      true, // a simulated mount is always "aligned"; the real one gates GoTo on it
		filterSlot:   1,
		// Fitted at construction so the names can never describe more slots than the wheel has.
		filterNames: device.FitFilterNames(filters.List(), c.WheelSlots),
		guideRate:   simGuideRate,
		tempMilliC:  20000,
		targetTempC: -15,
		pecTable:    make([]int8, simPECBins),
		clock:       time.Now,
	}
	w.bootAt = w.clock()
	w.wormFrom = w.bootAt
	w.driftFrom = w.bootAt
	return w
}

// SetClock replaces the simulated clock, so a test can drive exposure, slew, cooling and worm phase
// deterministically instead of sleeping. The field was always injectable; it just had no setter.
func (w *World) SetClock(clock func() time.Time) {
	if clock == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	now := clock()
	w.clock = clock
	w.bootAt, w.wormFrom, w.driftFrom = now, now, now
	w.wormAccum = 0
}

// Config returns the (defaulted) simulated conditions.
func (w *World) Config() Config {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cfg
}

// SetFocusOffset moves the simulated focuser. This is what makes the focus meter testable: the blur
// circle is |offset| / focal-ratio, so a known offset has a known HFD.
func (w *World) SetFocusOffset(um float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cfg.FocusOffsetUm = um
}

// SetSeeing changes the atmospheric FWHM mid-session.
func (w *World) SetSeeing(arcsec float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if arcsec > 0 {
		w.cfg.SeeingArcsec = arcsec
	}
}

// SetSyntheticStars replaces the injected stars. It exists because a small test sensor at a long
// focal length covers a few arcminutes of sky, where the real catalogue may hold no stars at all —
// and because the demo mode needs a guaranteed-pretty field.
func (w *World) SetSyntheticStars(stars []SyntheticStar) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cfg.SyntheticStars = append([]SyntheticStar(nil), stars...)
}

// SetHotPixels changes how many sensor defects are simulated (0 for a clean sensor).
// SetFaintStars sets the synthetic faint-star density. Negative turns the population off, for tests
// that need a field containing only the stars they planted.
func (w *World) SetFaintStars(perDeg2 float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if perDeg2 < 0 {
		perDeg2 = -1 // the renderer's "none"
	}
	w.cfg.FaintStarsPerDeg2 = perDeg2
}

// SetFlatPanel puts a flat panel over the aperture (adiPerSec > 0) or takes it away (0), so the
// calibration wizard can be exercised without one.
func (w *World) SetFlatPanel(aduPerSec float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if aduPerSec < 0 {
		aduPerSec = 0
	}
	w.cfg.FlatPanelADUPerSec = aduPerSec
}

func (w *World) SetHotPixels(n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if n < 0 {
		n = 0
	}
	w.cfg.HotPixels = n
}

// Pointing is where the simulated telescope currently looks — used to place synthetic stars in view.
func (w *World) Pointing() (raDeg, decDeg float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pointingAt(w.now())
}

// SetCameraAngle sets the rotation of the sensor on the sky (degrees east of north).
func (w *World) SetCameraAngle(paDeg float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cameraPADeg = paDeg
}

// now is the simulated clock.
func (w *World) now() time.Time { return w.clock() }

// pointingAt returns where the telescope is actually looking at instant t: the RA worm's periodic
// error, whatever the PEC table is correcting, and the drift when the drive is off.
//
// Everything the worm does is an error of the AXIS, so it enters the RA COORDINATE directly. What
// lands on the sensor is that times cos(dec) — which is precisely the factor a correction measured
// from pixels must undo before it can be written back to a mount. Modelling it the other way round
// (as this used to) would let code that forgets the division still pass its tests.
func (w *World) pointingAt(t time.Time) (raDeg, decDeg float64) {
	ra, dec := w.raDeg, w.decDeg

	ra += (w.periodicErrorArcsecLocked(t) + w.pecCorrectionArcsecLocked(t)) / 3600

	if !w.tracking {
		// Untracked, the sky slides west at the sidereal rate.
		ra += device.SiderealArcsecPerSec * t.Sub(w.driftFrom).Seconds() / 3600
	}
	return w.misalignLocked(normRA(ra), dec, t)
}

// misalignLocked bends the pointing by the configured polar-alignment error.
//
// Everything above this line describes a mount whose axis is exactly on the pole; this is where that
// stops being assumed. It is applied at the very end, to the single place the telescope's real pointing
// is decided, so the mount readout, the rendered star field and any plate solve of it all agree — one
// consistently misaligned telescope rather than three views of different ones.
//
// The drift a badly aligned mount suffers falls out of this rather than being modelled: the error is a
// rotation fixed to the GROUND, so applying it to a pointing that is fixed in the SKY (which is what
// tracking maintains) makes the result creep, at the rate and in the direction it really would.
//
// Caller holds w.mu.
func (w *World) misalignLocked(raDeg, decDeg float64, t time.Time) (float64, float64) {
	if w.cfg.PolarErrorAltArcmin == 0 && w.cfg.PolarErrorAzArcmin == 0 {
		return raDeg, decDeg
	}
	return polaralign.MisalignPointing(raDeg, decDeg,
		polaralign.Site{LatDeg: w.cfg.LatDeg, LonDeg: w.cfg.LonDeg}, t,
		w.cfg.PolarErrorAltArcmin/60, w.cfg.PolarErrorAzArcmin/60)
}

// SetPolarError knocks the simulated mount's polar axis off the pole while it runs, in arcminutes:
// altArcmin too high, azArcmin east of the pole's meridian. This is the knob a developer turns to watch
// the alignment panel find it again, and then to watch the marker converge as it is dialled back out.
func (w *World) SetPolarError(altArcmin, azArcmin float64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.cfg.PolarErrorAltArcmin = altArcmin
	w.cfg.PolarErrorAzArcmin = azArcmin
}

// wormElapsedLocked is how long the worm has been turning: accumulated TRACKED time, not wall time.
// A real worm stops when the drive stops, so stopping tracking to calibrate pauses the periodic
// error rather than teleporting its phase.
func (w *World) wormElapsedLocked(t time.Time) float64 {
	total := w.wormAccum
	if w.tracking {
		total += t.Sub(w.wormFrom)
	}
	return total.Seconds()
}

// periodicErrorArcsecLocked is the worm's own error, in axis arcseconds.
func (w *World) periodicErrorArcsecLocked(t time.Time) float64 {
	if w.cfg.PEPeriodSec <= 0 {
		return 0
	}
	elapsed := w.wormElapsedLocked(t)
	var arcsec float64
	if w.cfg.PEAmplitude > 0 {
		arcsec += (w.cfg.PEAmplitude / 2) * math.Sin(2*math.Pi*elapsed/w.cfg.PEPeriodSec)
	}
	for _, h := range w.cfg.PEHarmonics {
		if h.K <= 0 || h.AmplitudeArcsec <= 0 {
			continue
		}
		arcsec += (h.AmplitudeArcsec / 2) *
			math.Sin(2*math.Pi*float64(h.K)*elapsed/w.cfg.PEPeriodSec+h.PhaseRad)
	}
	return arcsec + w.wormJitterArcsecLocked(elapsed)
}

// wormJitterArcsecLocked is the part that does not repeat: three sinusoids at periods deliberately
// incommensurate with the worm, so they are never in the same place twice and folding cannot
// correct them. It is what keeps the simulated mount from being perfectly correctable — and so what
// makes the repeatability gate testable at all.
func (w *World) wormJitterArcsecLocked(elapsed float64) float64 {
	if w.cfg.PEJitterArcsec <= 0 {
		return 0
	}
	p := w.cfg.PEPeriodSec
	a := w.cfg.PEJitterArcsec / 2 / math.Sqrt(3)
	return a * (math.Sin(2*math.Pi*elapsed/(p*0.3713)) +
		math.Sin(2*math.Pi*elapsed/(p*1.7211)+1.1) +
		math.Sin(2*math.Pi*elapsed/(p*0.1279)+2.7))
}

// pecCorrectionArcsecLocked is the position the played-back table has accumulated by time t.
//
// The table holds RATES, so its effect on position is their integral — taken over ALL elapsed worm
// time, not folded back into one cycle. That distinction is the point: a table whose values do not
// sum to zero makes a real mount drift a little further every revolution, and a simulator that
// silently reset the integral each cycle would hide exactly that bug.
func (w *World) pecCorrectionArcsecLocked(t time.Time) float64 {
	if !w.pecPlaying || len(w.pecTable) == 0 || w.cfg.PEPeriodSec <= 0 {
		return 0
	}
	// Integrating from when playback started, not from worm time zero, is what makes switching it on
	// mid-run continuous: the mount begins correcting now, it does not retroactively catch up.
	return w.pecIntegralArcsecLocked(w.wormElapsedLocked(t)) -
		w.pecIntegralArcsecLocked(w.pecPlayFromWorm)
}

// pecIntegralArcsecLocked is the position the table would have accumulated after `elapsed` seconds
// of worm time, measured from worm time zero.
func (w *World) pecIntegralArcsecLocked(elapsed float64) float64 {
	bins := len(w.pecTable)
	lsb := device.SiderealArcsecPerSec / w.cfg.PECRateScale
	if w.cfg.PECInvertSign {
		lsb = -lsb
	}
	binSec := w.cfg.PEPeriodSec / float64(bins)

	cycles := math.Floor(elapsed / w.cfg.PEPeriodSec)
	rem := elapsed - cycles*w.cfg.PEPeriodSec

	var perCycle float64
	for _, v := range w.pecTable {
		perCycle += float64(v) * lsb * binSec
	}
	total := cycles * perCycle

	whole := int(rem / binSec)
	for i := 0; i < whole && i < bins; i++ {
		total += float64(w.pecTable[i]) * lsb * binSec
	}
	if whole < bins {
		total += float64(w.pecTable[whole]) * lsb * (rem - float64(whole)*binSec)
	}
	return total
}

// pecBinLocked is the bin the worm is turning through. It is derived from the same phase the
// correction is applied at, so folding measurements on the mount's reported index is self-consistent
// by construction — any constant offset between the two cancels.
func (w *World) pecBinLocked(t time.Time) int {
	if w.cfg.PEPeriodSec <= 0 || len(w.pecTable) == 0 {
		return 0
	}
	bins := len(w.pecTable)
	rem := math.Mod(w.wormElapsedLocked(t), w.cfg.PEPeriodSec)
	if rem < 0 {
		rem += w.cfg.PEPeriodSec
	}
	if b := int(rem / (w.cfg.PEPeriodSec / float64(bins))); b < bins {
		return b
	}
	return bins - 1
}

func normRA(ra float64) float64 {
	r := math.Mod(ra, 360)
	if r < 0 {
		r += 360
	}
	return r
}
