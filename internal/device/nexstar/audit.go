package nexstar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/verove-jordan/astronomy/internal/device"
	"github.com/verove-jordan/astronomy/internal/guide"
)

// What is actually stored in this mount, and how to put back the parts of it this app can write.
//
// Everything else in this package either reads a value to use it or writes one to move the
// telescope. This file exists for a different question, asked after a night that went wrong: WHAT IS
// IN THERE. It is worth a file of its own because the answer is otherwise unobtainable — the hand
// controller has no menu that shows you its stored periodic-error table, and no menu that erases it
// either.
//
// Two facts about a Celestron shape all of this, and both are easy to get backwards:
//
//   - The settings live on TWO boards. Site, clock, tracking mode, the alignment model and the
//     anti-backlash menu belong to the HAND CONTROLLER. The periodic-error table, the autoguide
//     rates and the GoTo approach belong to the MOTOR CONTROLLERS, one per axis, and survive a hand
//     controller being swapped. `Menu > Utilities > Factory Settings` clears the motor boards too,
//     but only from hand-controller firmware 4.15 onwards — which is precisely the kind of "should
//     have" that this file replaces with a reading.
//   - Anti-backlash is NOT here, and its absence is not an oversight. Celestron's serial protocol has
//     no command for it, and neither does any third-party driver. It is hand-controller-only, so an
//     audit that reported it would be inventing it.
//
// Audit never moves the mount: no index seek (that turns RA by up to two degrees), no sync, no GoTo.
// It is safe to run mid-session. Restore only ever writes values it then reads back.
//
// Both take a device.Mount and reach the rest through optional interfaces, the same way devsrv
// already asks a mount whether it can set its own site. That is not generality for its own sake: the
// simulator implements the periodic-error table and the autoguide rate but has no hand controller and
// therefore no stored site or clock, and a report that refused to run at all against it could never
// be developed against anything but hardware on a clear night. A capability that is missing is
// reported as missing.

// The optional pieces of a mount. Each is asked for by shape rather than required, so a driver that
// lacks one reports a gap instead of failing the whole reading.
type (
	identityReader interface {
		Model() string
		Firmware() string
	}
	modelCodeReader interface{ ModelCode() byte }
	siteReader      interface {
		Site(context.Context) (Site, error)
	}
	siteWriter interface {
		SetSite(context.Context, Site) (Site, error)
	}
	clockReader interface {
		Clock(context.Context) (Clock, error)
	}
	clockWriter interface {
		SetClock(context.Context, time.Time) (Clock, error)
	}
	guideRatesReader interface {
		GuideRates(context.Context) (float64, float64, error)
	}
	motorFirmwareReader interface {
		MotorFirmware(context.Context) (string, string, error)
	}
	healthReader interface{ Health() LinkHealth }
	pathReader   interface{ Path() string }
)

// AuditReport is everything the wire can tell us about what this mount is holding.
//
// Each section carries its own Read/Err rather than the whole call failing, because a mount that
// answers five questions out of six is exactly the thing you want to look at — and because the
// simulator legitimately implements only some of them.
type AuditReport struct {
	// AtMs is when this was read, in milliseconds, per the house convention for stored times.
	AtMs int64  `json:"at_ms"`
	Port string `json:"port,omitempty"`

	Identity IdentityAudit `json:"identity"`
	Site     SiteAudit     `json:"site"`
	Clock    ClockAudit    `json:"clock"`
	Drive    DriveAudit    `json:"drive"`
	Guide    GuideAudit    `json:"guide"`
	PEC      PECAudit      `json:"pec"`
	Link     LinkHealth    `json:"link"`

	// Notes are the things that need saying in words: what could not be read, and what a reading
	// does NOT prove.
	Notes []string `json:"notes,omitempty"`
}

// IdentityAudit names the three separate pieces of firmware in a Celestron mount.
type IdentityAudit struct {
	Model     string `json:"model"`
	ModelCode int    `json:"model_code"`
	// Firmware is the HAND CONTROLLER's version — the one the 4.15 factory-reset rule turns on.
	Firmware string `json:"firmware"`
	// RAMotorFirmware and DecMotorFirmware are the motor controllers' own, read straight from the
	// boards that hold the PEC table and the autoguide rates.
	RAMotorFirmware  string `json:"ra_motor_firmware,omitempty"`
	DecMotorFirmware string `json:"dec_motor_firmware,omitempty"`
	MotorErr         string `json:"motor_err,omitempty"`
}

// SiteAudit is where the hand controller believes it is standing.
type SiteAudit struct {
	Read   bool    `json:"read"`
	LatDeg float64 `json:"lat_deg"`
	LonDeg float64 `json:"lon_deg"`
	Err    string  `json:"err,omitempty"`
}

// ClockAudit is what the hand controller believes the time is, and how far that is from this Mac.
type ClockAudit struct {
	Read        bool      `json:"read"`
	UTC         time.Time `json:"utc"`
	OffsetHours int       `json:"offset_hours"`
	DST         bool      `json:"dst"`
	// SkewSec is the mount's clock minus the host's. Fifteen arcseconds of sky per second, so a
	// minute here is a quarter of a degree of pointing error and nothing in an image says why.
	SkewSec float64 `json:"skew_sec"`
	Err     string  `json:"err,omitempty"`
}

// DriveAudit is what the drive is doing right now.
type DriveAudit struct {
	Read         bool    `json:"read"`
	Tracking     bool    `json:"tracking"`
	TrackingRate string  `json:"tracking_rate"`
	Aligned      bool    `json:"aligned"`
	PierSide     string  `json:"pier_side,omitempty"`
	RADeg        float64 `json:"ra_deg"`
	DecDeg       float64 `json:"dec_deg"`
	Err          string  `json:"err,omitempty"`
}

// GuideAudit is the autoguide rate each motor controller is configured for.
//
// Both axes are read, not one. GuideRate() reads only right ascension on the grounds that the two are
// always set together — which is a reasonable assumption for a driver and exactly the assumption an
// audit exists to test. A pair that disagree is one of the very few durable settings that can make
// one axis behave differently from the other.
type GuideAudit struct {
	Read        bool    `json:"read"`
	RAUnits     int     `json:"ra_units"`
	DecUnits    int     `json:"dec_units"`
	RAFraction  float64 `json:"ra_fraction"`
	DecFraction float64 `json:"dec_fraction"`
	// BothAxes says whether the declination motor could be read at all. A driver that only exposes
	// the single-axis GuideRate() answers for right ascension alone, and calling that "no mismatch"
	// would be the kind of quiet assumption this report exists to remove.
	BothAxes bool `json:"both_axes"`
	// Mismatch is true when the two motors are set to different rates. Meaningless unless BothAxes.
	Mismatch bool   `json:"mismatch"`
	Err      string `json:"err,omitempty"`
}

// PECAudit is the periodic-error table itself, plus what it would actually do to the mount.
type PECAudit struct {
	Supported bool   `json:"supported"`
	Read      bool   `json:"read"`
	Err       string `json:"err,omitempty"`

	Bins            int     `json:"bins"`
	WormPeriodSec   float64 `json:"worm_period_sec"`
	BinSec          float64 `json:"bin_sec"`
	LSBArcsecPerSec float64 `json:"lsb_arcsec_per_sec"`

	Indexed    bool  `json:"indexed"`
	CurrentBin int   `json:"current_bin"`
	Curve      []int `json:"curve,omitempty"`

	// AllZero is the headline: a table of zeros corrects nothing, which is the state a mount leaves
	// the factory in.
	AllZero bool `json:"all_zero"`
	// PeakUnits is the largest single correction in raw table units (the range is -128..127).
	PeakUnits int `json:"peak_units"`
	// PeakRateArcsecPerSec is that same peak expressed as a rate.
	PeakRateArcsecPerSec float64 `json:"peak_rate_arcsec_per_sec"`
	// SwingArcsec is the peak-to-peak POSITION this table moves right ascension through over one worm
	// revolution — the number that is comparable with a measured periodic error.
	SwingArcsec float64 `json:"swing_arcsec"`
	// NetArcsecPerRev is the table's DC term: how far it pushes right ascension over a whole
	// revolution rather than returning it. A correction should average to zero; anything else is a
	// constant rate error bolted onto sidereal, and it is the one shape of bad table that makes an
	// otherwise healthy mount trail steadily in one direction.
	NetArcsecPerRev float64 `json:"net_arcsec_per_rev"`

	// PlaybackCommanded is what THIS DRIVER last asked for. The mount cannot be asked whether it is
	// replaying the table, so this is not a reading, and it is meaningless after a power cycle — which
	// is also the reassuring part: a Celestron always comes up with playback off.
	PlaybackCommanded bool `json:"playback_commanded"`
}

// Audit reads everything the protocol can reach. It writes nothing and moves nothing.
func Audit(ctx context.Context, m device.Mount) (AuditReport, error) {
	if m == nil || !m.Connected() {
		return AuditReport{}, device.ErrNotConnected
	}
	r := AuditReport{AtMs: time.Now().UnixMilli()}
	if p, ok := m.(pathReader); ok {
		r.Port = p.Path()
	}
	if id, ok := m.(identityReader); ok {
		r.Identity.Model, r.Identity.Firmware = id.Model(), id.Firmware()
	}
	if mc, ok := m.(modelCodeReader); ok {
		r.Identity.ModelCode = int(mc.ModelCode())
	}

	if st, err := m.State(ctx); err != nil {
		r.Drive.Err = err.Error()
	} else {
		r.Drive = DriveAudit{
			Read: true, Tracking: st.Tracking, TrackingRate: st.TrackingRate,
			Aligned: st.Aligned, PierSide: st.PierSide, RADeg: st.RADeg, DecDeg: st.DecDeg,
		}
		if r.Identity.Model == "" {
			r.Identity.Model, r.Identity.Firmware = st.Model, st.Firmware
		}
	}

	r.Site = auditSite(ctx, m)
	r.Clock = auditClock(ctx, m)
	r.Guide = auditGuide(ctx, m)
	r.PEC = auditPEC(ctx, m)
	if h, ok := m.(healthReader); ok {
		r.Link = h.Health()
	}

	// The motor-controller versions go LAST and are followed by a re-proof of synchronisation. The
	// reply length of this one command is the only one in the package we cannot be certain of, and a
	// version string is not worth risking any reading above it.
	if mf, ok := m.(motorFirmwareReader); ok {
		if raV, decV, err := mf.MotorFirmware(ctx); err != nil {
			r.Identity.MotorErr = err.Error()
			if p, ok := m.(interface{ Ping(context.Context) error }); ok {
				_ = p.Ping(ctx)
			}
		} else {
			r.Identity.RAMotorFirmware, r.Identity.DecMotorFirmware = raV, decV
		}
	}

	r.Notes = auditNotes(r)
	return r, nil
}

func auditSite(ctx context.Context, m device.Mount) SiteAudit {
	sr, ok := m.(siteReader)
	if !ok {
		return SiteAudit{Err: "this driver has no stored site"}
	}
	s, err := sr.Site(ctx)
	if err != nil {
		return SiteAudit{Err: err.Error()}
	}
	return SiteAudit{Read: true, LatDeg: s.LatDeg, LonDeg: s.LonDeg}
}

func auditClock(ctx context.Context, m device.Mount) ClockAudit {
	cr, ok := m.(clockReader)
	if !ok {
		return ClockAudit{Err: "this driver has no stored clock"}
	}
	c, err := cr.Clock(ctx)
	if err != nil {
		return ClockAudit{Err: err.Error()}
	}
	return ClockAudit{
		Read: true, UTC: c.UTC, OffsetHours: c.OffsetHours, DST: c.DST,
		SkewSec: c.UTC.Sub(time.Now().UTC()).Seconds(),
	}
}

// auditGuide prefers the two-axis read and falls back to the single-axis one, saying which it got.
func auditGuide(ctx context.Context, m device.Mount) GuideAudit {
	if gr, ok := m.(guideRatesReader); ok {
		ra, dec, err := gr.GuideRates(ctx)
		if err != nil {
			return GuideAudit{Err: err.Error()}
		}
		return GuideAudit{
			Read: true, BothAxes: true,
			RAUnits: units(ra), DecUnits: units(dec),
			RAFraction: ra, DecFraction: dec,
			Mismatch: math.Abs(ra-dec) > 0.5/autoguideRateScale,
		}
	}
	gm, ok := m.(device.GuideMount)
	if !ok {
		return GuideAudit{Err: "this driver has no autoguide rate"}
	}
	ra, err := gm.GuideRate(ctx)
	if err != nil {
		return GuideAudit{Err: err.Error()}
	}
	return GuideAudit{Read: true, RAUnits: units(ra), RAFraction: ra}
}

func units(fraction float64) int { return int(math.Round(fraction * autoguideRateScale)) }

// errNoPECTable is the refusal for a mount with no worm table. Named because it is a capability gap
// rather than a failure, and the difference matters to what the UI shows.
var errNoPECTable = errors.New("this mount has no periodic-error correction table")

func auditPEC(ctx context.Context, m device.Mount) PECAudit {
	pm, ok := m.(device.PECMount)
	if !ok {
		return PECAudit{Err: errNoPECTable.Error()}
	}
	var out PECAudit

	caps, err := pm.PECCaps(ctx)
	if err != nil {
		out.Err = err.Error()
		return out
	}
	out.Supported = true
	out.Bins, out.WormPeriodSec = caps.Bins, caps.WormPeriodSec
	out.BinSec, out.LSBArcsecPerSec = caps.BinSec, caps.LSBArcsecPerSec

	// Status before the curve: it is two frames rather than eighty-eight, so a mount that is going to
	// refuse says so quickly.
	if st, err := pm.PECStatus(ctx); err == nil {
		out.Indexed, out.CurrentBin = st.Indexed, st.CurrentBin
		// Playing is what the DRIVER last commanded, not a reading — the protocol has no way to ask.
		out.PlaybackCommanded = st.Playing
	}

	curve, err := pm.PECReadCurve(ctx)
	if err != nil {
		out.Err = err.Error()
		return out
	}
	out.Read = true
	out.Curve = make([]int, len(curve))
	out.AllZero = true
	for i, v := range curve {
		out.Curve[i] = int(v)
		if v != 0 {
			out.AllZero = false
		}
		if abs := absInt(int(v)); abs > out.PeakUnits {
			out.PeakUnits = abs
		}
	}
	out.PeakRateArcsecPerSec = float64(out.PeakUnits) * caps.LSBArcsecPerSec
	out.SwingArcsec, out.NetArcsecPerRev = pecExcursionArcsec(curve, caps.LSBArcsecPerSec, caps.BinSec)
	return out
}

// pecExcursionArcsec integrates the table's rates into the POSITION it drives right ascension
// through, because a table is a list of rate corrections and a rate says nothing on its own about
// how far the mount moves.
//
// It returns the peak-to-peak swing over one worm revolution and the net displacement across the
// whole revolution. The second one is the interesting failure: a correction is supposed to come back
// to where it started, so a non-zero net is a constant rate error added to sidereal — a mount that
// trails steadily in one direction all night with nothing in the image to say why.
func pecExcursionArcsec(bins []int8, lsbArcsecPerSec, binSec float64) (swing, net float64) {
	if len(bins) == 0 {
		return 0, 0
	}
	pos, lo, hi := 0.0, 0.0, 0.0
	for _, v := range bins {
		pos += float64(v) * lsbArcsecPerSec * binSec
		lo, hi = math.Min(lo, pos), math.Max(hi, pos)
	}
	return hi - lo, pos
}

// auditNotes says in words the things a table of numbers gets wrong: what was not read, and what a
// reading does not prove.
func auditNotes(r AuditReport) []string {
	var notes []string
	if r.PEC.Read {
		if r.PEC.AllZero {
			notes = append(notes, "The periodic-error table is all zeros: the mount is applying no worm correction at all.")
		} else {
			notes = append(notes, fmt.Sprintf(
				"The periodic-error table is NOT empty: peak %d units (%.2f\"/s), which swings right ascension by %.1f\" over one worm revolution.",
				r.PEC.PeakUnits, r.PEC.PeakRateArcsecPerSec, r.PEC.SwingArcsec))
			if math.Abs(r.PEC.NetArcsecPerRev) > 1 {
				notes = append(notes, fmt.Sprintf(
					"That table does not average to zero — it adds %.1f\" of drift per worm revolution (%.2f\"/min) on top of sidereal. A correction should return to where it started.",
					r.PEC.NetArcsecPerRev, r.PEC.NetArcsecPerRev/(r.PEC.WormPeriodSec/60)))
			}
		}
		notes = append(notes,
			"Whether the mount is REPLAYING that table cannot be read back over the link. A Celestron always powers up with playback off, so unless something switched it on this session, it is off.")
	}
	if r.Guide.Read && !r.Guide.BothAxes {
		notes = append(notes, "Only the right-ascension motor's autoguide rate could be read; this driver does not expose the declination one.")
	}
	if r.Guide.Read && r.Guide.Mismatch {
		notes = append(notes, fmt.Sprintf(
			"The two motors are set to DIFFERENT autoguide rates (RA %d/256, Dec %d/256). Every hand controller sets them together, so this was set by software.",
			r.Guide.RAUnits, r.Guide.DecUnits))
	}
	if r.Drive.Read && !r.Drive.Tracking {
		notes = append(notes, "The drive is not tracking.")
	}
	if r.Clock.Read && math.Abs(r.Clock.SkewSec) > 60 {
		notes = append(notes, fmt.Sprintf(
			"The mount's clock is %.0f s away from this Mac's — that is %.2f° of sky.",
			r.Clock.SkewSec, math.Abs(r.Clock.SkewSec)*15/3600))
	}
	notes = append(notes,
		"Anti-backlash is not shown because Celestron's serial protocol has no command for it: it can only be seen and changed in the hand controller's own menus.")
	return notes
}

// String renders the report the way `mount doctor` and `mount soak` render theirs: for a terminal at
// three in the morning, in sentences.
func (r AuditReport) String() string {
	var b strings.Builder
	f := func(label, format string, args ...any) {
		fmt.Fprintf(&b, "%-14s%s\n", label, fmt.Sprintf(format, args...))
	}

	f("mount", "%s (model %d)  hand controller firmware %s", orUnknown(r.Identity.Model), r.Identity.ModelCode, orUnknown(r.Identity.Firmware))
	switch {
	case r.Identity.MotorErr != "":
		f("motors", "firmware unavailable: %s", r.Identity.MotorErr)
	case r.Identity.RAMotorFirmware != "":
		f("motors", "RA firmware %s   Dec firmware %s", r.Identity.RAMotorFirmware, r.Identity.DecMotorFirmware)
	}
	if r.Port != "" {
		f("port", "%s", r.Port)
	}

	b.WriteString("\nhand controller\n")
	if r.Site.Read {
		f("  site", "%.4f°, %.4f°", r.Site.LatDeg, r.Site.LonDeg)
	} else {
		f("  site", "not readable (%s)", orUnknown(r.Site.Err))
	}
	if r.Clock.Read {
		f("  clock", "%s  (UTC%+d%s)  %+.0f s vs this Mac",
			r.Clock.UTC.Format(time.RFC3339), r.Clock.OffsetHours, dstSuffix(r.Clock.DST), r.Clock.SkewSec)
	} else {
		f("  clock", "not readable (%s)", orUnknown(r.Clock.Err))
	}
	if r.Drive.Read {
		f("  drive", "tracking=%v (%s)  aligned=%v  pier=%s",
			r.Drive.Tracking, orUnknown(r.Drive.TrackingRate), r.Drive.Aligned, orUnknown(r.Drive.PierSide))
	} else {
		f("  drive", "not readable (%s)", orUnknown(r.Drive.Err))
	}

	b.WriteString("\nmotor controllers\n")
	switch {
	case r.Guide.Read && r.Guide.BothAxes:
		disagree := ""
		if r.Guide.Mismatch {
			disagree = "   ← THEY DISAGREE"
		}
		f("  guide rate", "RA %d/256 (%.0f%% sidereal)   Dec %d/256 (%.0f%% sidereal)%s",
			r.Guide.RAUnits, r.Guide.RAFraction*100, r.Guide.DecUnits, r.Guide.DecFraction*100, disagree)
	case r.Guide.Read:
		f("  guide rate", "RA %d/256 (%.0f%% sidereal); this driver cannot read the declination motor",
			r.Guide.RAUnits, r.Guide.RAFraction*100)
	default:
		f("  guide rate", "not readable (%s)", orUnknown(r.Guide.Err))
	}
	switch {
	case !r.PEC.Supported:
		f("  PEC", "this mount has no periodic-error table (%s)", orUnknown(r.PEC.Err))
	case !r.PEC.Read:
		f("  PEC", "not readable (%s)", orUnknown(r.PEC.Err))
	case r.PEC.AllZero:
		f("  PEC", "%d bins, ALL ZERO — no correction stored", r.PEC.Bins)
	default:
		f("  PEC", "%d bins, peak %d units = %.2f\"/s; swing %.1f\", net %+.1f\"/rev",
			r.PEC.Bins, r.PEC.PeakUnits, r.PEC.PeakRateArcsecPerSec, r.PEC.SwingArcsec, r.PEC.NetArcsecPerRev)
	}
	if r.PEC.Supported {
		f("  PEC index", "found=%v  current bin %d  worm %.0f s", r.PEC.Indexed, r.PEC.CurrentBin, r.PEC.WormPeriodSec)
		f("  PEC playback", "last commanded by this driver: %v (the mount cannot be asked)", r.PEC.PlaybackCommanded)
	}

	if r.Link.Connected {
		b.WriteString("\n")
		f("link", "%d commands, %d errors, %d resyncs, %d desyncs, p50 %dms p99 %dms",
			r.Link.Commands, r.Link.Errors, r.Link.Resyncs, r.Link.Desyncs, r.Link.LatencyP50Ms, r.Link.LatencyP99Ms)
	}

	if len(r.Notes) > 0 {
		b.WriteString("\n")
		for _, n := range r.Notes {
			fmt.Fprintf(&b, "  - %s\n", n)
		}
	}
	return b.String()
}

// JSON is the same report for the UI and for a file kept beside a night's data.
func (r AuditReport) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func dstSuffix(dst bool) string {
	if dst {
		return " +DST"
	}
	return ""
}

// --- putting it back ----------------------------------------------------------------------------

// RestoreOptions says which settings to return to a neutral value. Nothing happens unless asked:
// this writes to hardware, and a default that reset something the user had spent an evening
// recording would be indefensible.
type RestoreOptions struct {
	// PEC writes a table of zeros. A zero rate in every bin IS the erase — the hand controller has no
	// menu item for it — and unlike the menu this can be proven bin by bin afterwards.
	PEC bool
	// PECPlayback stops the mount replaying whatever table it holds.
	PECPlayback bool
	// GuideRate sets both motors to GuideRateFraction.
	GuideRate bool
	// GuideRateFraction defaults to guide.DefaultGuideRateFraction — half sidereal, which is what
	// mounts ship with. It is a named constant rather than a number invented here.
	GuideRateFraction float64
	// Site and Clock rewrite the hand controller's idea of where and when it is.
	Site                   bool
	SiteLatDeg, SiteLonDeg float64
	Clock                  bool
	ClockTime              time.Time // zero means now, in this machine's zone

	// Tracking sets the drive mode. TrackingRate follows SetTracking's vocabulary.
	Tracking     bool
	TrackingOn   bool
	TrackingRate string

	// BackupDir is where the pre-change audit is written. It is REQUIRED and not defaultable: the
	// table already in the mount may be the only copy of an hour somebody spent with a hand
	// controller, and it exists nowhere else in the world.
	BackupDir string
	// DryRun reports what would be sent and sends nothing.
	DryRun bool
}

// RestoreAction is one thing Restore did, or would have done.
type RestoreAction struct {
	Item    string `json:"item"`
	Detail  string `json:"detail"`
	Applied bool   `json:"applied"`
	Err     string `json:"err,omitempty"`
}

// RestoreResult is the before, the after, and what happened in between.
type RestoreResult struct {
	DryRun     bool            `json:"dry_run"`
	BackupPath string          `json:"backup_path,omitempty"`
	Before     AuditReport     `json:"before"`
	After      AuditReport     `json:"after,omitempty"`
	Actions    []RestoreAction `json:"actions"`
}

// Restore returns the mount to a state in which nothing this application wrote is still in effect.
//
// It is deliberately NOT called a factory reset. A factory reset is a hand-controller menu item that
// also clears things no serial command can reach, and it tells you nothing about what it did. This
// does less and proves all of it: every write is followed by a read-back, and the whole pre-change
// state is on disk before the first byte goes out.
func Restore(ctx context.Context, m device.Mount, opts RestoreOptions) (RestoreResult, error) {
	if m == nil || !m.Connected() {
		return RestoreResult{}, device.ErrNotConnected
	}
	if opts.BackupDir == "" {
		return RestoreResult{}, fmt.Errorf("a backup directory is required: the table in the mount may be the only copy there is")
	}

	before, err := Audit(ctx, m)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("reading the mount before changing it: %w", err)
	}
	res := RestoreResult{DryRun: opts.DryRun, Before: before}

	path, err := writeBackup(before, opts.BackupDir)
	if err != nil {
		// Refusing here rather than warning is the whole point of the rule.
		return res, fmt.Errorf("backing up the mount's current settings: %w", err)
	}
	res.BackupPath = path

	fraction := opts.GuideRateFraction
	if fraction <= 0 {
		fraction = guide.DefaultGuideRateFraction
	}
	pm, hasPEC := m.(device.PECMount)
	gm, hasGuide := m.(device.GuideMount)

	// Playback goes off BEFORE the table is rewritten. The other order replays a half-written table
	// for the couple of seconds the write takes, against a worm position nobody chose.
	if opts.PECPlayback {
		res.act(opts.DryRun, "pec_playback", "stop replaying the stored table", func() error {
			if !hasPEC {
				return errNoPECTable
			}
			return pm.PECPlayback(ctx, false)
		})
	}
	if opts.PEC {
		bins := before.PEC.Bins
		if bins <= 0 {
			bins = len(before.PEC.Curve)
		}
		detail := fmt.Sprintf("write %d zero bins and verify each one", bins)
		res.act(opts.DryRun, "pec", detail, func() error {
			if !hasPEC {
				return errNoPECTable
			}
			if bins <= 0 {
				return fmt.Errorf("the mount did not report how many bins its table has")
			}
			return pm.PECWriteCurve(ctx, make([]int8, bins))
		})
	}
	if opts.GuideRate {
		detail := fmt.Sprintf("set both motors to %.0f%% of sidereal (%d/256)", fraction*100, units(fraction))
		res.act(opts.DryRun, "guide_rate", detail, func() error {
			if !hasGuide {
				return fmt.Errorf("this driver has no autoguide rate to set")
			}
			return gm.SetGuideRate(ctx, fraction)
		})
	}
	if opts.Site {
		detail := fmt.Sprintf("set the site to %.4f\u00b0, %.4f\u00b0", opts.SiteLatDeg, opts.SiteLonDeg)
		res.act(opts.DryRun, "site", detail, func() error {
			sw, ok := m.(siteWriter)
			if !ok {
				return fmt.Errorf("this driver has no stored site to set")
			}
			_, err := sw.SetSite(ctx, Site{LatDeg: opts.SiteLatDeg, LonDeg: opts.SiteLonDeg})
			return err
		})
	}
	if opts.Clock {
		when := opts.ClockTime
		if when.IsZero() {
			when = time.Now()
		}
		res.act(opts.DryRun, "clock", "set the clock from this machine", func() error {
			cw, ok := m.(clockWriter)
			if !ok {
				return fmt.Errorf("this driver has no stored clock to set")
			}
			_, err := cw.SetClock(ctx, when)
			return err
		})
	}
	if opts.Tracking {
		rate := opts.TrackingRate
		if rate == "" {
			rate = "sidereal"
		}
		detail := fmt.Sprintf("set tracking on=%v rate=%s", opts.TrackingOn, rate)
		res.act(opts.DryRun, "tracking", detail, func() error {
			return m.SetTracking(ctx, opts.TrackingOn, rate)
		})
	}

	if !opts.DryRun {
		if after, err := Audit(ctx, m); err == nil {
			res.After = after
		}
	}
	return res, nil
}

// act records an action, and performs it unless this is a dry run.
func (r *RestoreResult) act(dryRun bool, item, detail string, do func() error) {
	a := RestoreAction{Item: item, Detail: detail}
	if !dryRun {
		if err := do(); err != nil {
			a.Err = err.Error()
		} else {
			a.Applied = true
		}
	}
	r.Actions = append(r.Actions, a)
}

// writeBackup saves the pre-change state, raw bins included, next to the night's other output.
func writeBackup(r AuditReport, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	b, err := r.JSON()
	if err != nil {
		return "", err
	}
	// Never overwrite. The stamp has second resolution and a preview followed by an apply lands
	// inside the same second, so a plain write would replace the earlier backup with the later one —
	// and the earlier one is always the more valuable of the two.
	stamp := time.Now().UTC().Format("20060102T150405Z")
	for n := 0; ; n++ {
		name := fmt.Sprintf("mount-restore-%s.json", stamp)
		if n > 0 {
			name = fmt.Sprintf("mount-restore-%s-%d.json", stamp, n)
		}
		path := filepath.Join(dir, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		defer f.Close()
		if _, err := f.Write(b); err != nil {
			return "", err
		}
		return path, nil
	}
}

// String renders what happened, for a terminal.
func (r RestoreResult) String() string {
	var b strings.Builder
	if r.DryRun {
		b.WriteString("DRY RUN — nothing was sent to the mount. Re-run with -apply to do it.\n\n")
	}
	if r.BackupPath != "" {
		fmt.Fprintf(&b, "backup       %s\n\n", r.BackupPath)
	}
	for _, a := range r.Actions {
		switch {
		case a.Err != "":
			fmt.Fprintf(&b, "  FAILED  %-14s %s: %s\n", a.Item, a.Detail, a.Err)
		case a.Applied:
			fmt.Fprintf(&b, "  done    %-14s %s\n", a.Item, a.Detail)
		default:
			fmt.Fprintf(&b, "  would   %-14s %s\n", a.Item, a.Detail)
		}
	}
	if len(r.Actions) == 0 {
		b.WriteString("  nothing selected — name at least one thing to restore.\n")
	}
	if !r.DryRun && r.After.AtMs != 0 {
		b.WriteString("\nafter:\n\n")
		b.WriteString(r.After.String())
	}
	return b.String()
}

// --- the reads the audit needed and the driver did not already have -----------------------------

// GuideRates reports the autoguide rate configured on BOTH motor controllers, as fractions of
// sidereal.
//
// GuideRate() reads only right ascension, on the stated grounds that the two are always set
// together. That is true of every hand controller and of SetGuideRate — which is exactly why reading
// both is worth doing when the question is whether something has been set that should not have been.
func (m *Mount) GuideRates(ctx context.Context) (ra, dec float64, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return 0, 0, device.ErrNotConnected
	}
	for _, axis := range []int{axisAzmRA, axisAltDec} {
		// Read by LENGTH, never by delimiter: the perfectly ordinary rate value 35 IS '#'.
		reply, rerr := m.rawBinaryLocked(getGuideRateCommand(axis), 1)
		if rerr != nil {
			return 0, 0, fmt.Errorf("read autoguide rate on motor %d: %w", axis, rerr)
		}
		if len(reply) < 1 {
			return 0, 0, fmt.Errorf("empty autoguide rate reply from motor %d", axis)
		}
		if axis == axisAzmRA {
			ra = float64(reply[0]) / autoguideRateScale
		} else {
			dec = float64(reply[0]) / autoguideRateScale
		}
	}
	return ra, dec, nil
}

// MotorFirmware reports each motor controller's own firmware version.
//
// These are separate boards from the hand controller, with separate flash, and they are where the
// periodic-error table and the autoguide rates actually live — so "which firmware" has two different
// answers and conflating them is how a reset gets believed that never happened.
func (m *Mount) MotorFirmware(ctx context.Context) (ra, dec string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.port == nil {
		return "", "", device.ErrNotConnected
	}
	for _, axis := range []int{axisAzmRA, axisAltDec} {
		reply, rerr := m.rawBinaryLocked(passthrough(byte(axis), mcGetVersion, nil, 2), 2)
		if rerr != nil {
			return "", "", fmt.Errorf("read firmware of motor %d: %w", axis, rerr)
		}
		if len(reply) < 2 {
			return "", "", fmt.Errorf("short firmware reply from motor %d", axis)
		}
		v := fmt.Sprintf("%d.%02d", reply[0], reply[1])
		if axis == axisAzmRA {
			ra = v
		} else {
			dec = v
		}
	}
	return ra, dec, nil
}

// ModelCode is the raw model byte the mount answered `m` with.
//
// Model() renders it as a name and is lossy — an unrecognised mount becomes the string "model 42",
// which cannot be mapped back — and the byte is what the periodic-error rate scale and worm length
// branch on. An audit that could not report it would be hiding the one number that decides whether
// the arcseconds beside the table are right.
func (m *Mount) ModelCode() byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.modelCode
}
